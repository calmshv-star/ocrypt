package main

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/eventbus"
	"github.com/calmshv-star/ocrypt/backend/internal/outbox"
)

type readinessDependency interface {
	Ready(context.Context) error
}

type outboxPublisherRuntime struct {
	publisher     outbox.Publisher
	readiness     readinessDependency
	close         func()
	maxRetryDelay time.Duration
}

func newOutboxPublisher(ctx context.Context) (outboxPublisherRuntime, error) {
	environment := os.Getenv("APP_ENV")
	if environment != "development" && environment != "test" && environment != "production" {
		return outboxPublisherRuntime{}, errors.New("APP_ENV must be development, test, or production")
	}
	maxRetryDelay, err := positiveDuration("OUTBOX_MAX_RETRY_DELAY", 15*time.Minute)
	if err != nil || maxRetryDelay > 24*time.Hour {
		return outboxPublisherRuntime{}, errors.New("OUTBOX_MAX_RETRY_DELAY must be at most 24h")
	}
	switch os.Getenv("OUTBOX_PUBLISHER") {
	case "history":
		if os.Getenv("OUTBOX_HISTORY_SINK_ACKNOWLEDGED") != "true" {
			return outboxPublisherRuntime{}, errors.New("OUTBOX_HISTORY_SINK_ACKNOWLEDGED=true is required for the local history sink")
		}
		return outboxPublisherRuntime{publisher: outbox.HistoryPublisher{}, close: func() {}, maxRetryDelay: maxRetryDelay}, nil
	case "https":
		token, err := readOutboxSecret("OUTBOX_PUBLISH_TOKEN_FILE", 4096)
		if err != nil {
			return outboxPublisherRuntime{}, err
		}
		publisher, err := outbox.NewHTTPPublisher(os.Getenv("OUTBOX_PUBLISH_URL"), token, nil)
		if err != nil {
			return outboxPublisherRuntime{}, err
		}
		return outboxPublisherRuntime{publisher: publisher, close: func() {}, maxRetryDelay: maxRetryDelay}, nil
	case "jetstream":
		config, err := loadOutboxJetStreamConfig(environment, maxRetryDelay)
		if err != nil {
			return outboxPublisherRuntime{}, err
		}
		readyCtx, cancel := context.WithTimeout(ctx, config.ConnectTimeout+config.PublishTimeout)
		defer cancel()
		publisher, err := eventbus.NewPublisher(readyCtx, config)
		if err != nil {
			return outboxPublisherRuntime{}, err
		}
		return outboxPublisherRuntime{
			publisher: publisher, readiness: publisher, close: publisher.Close, maxRetryDelay: maxRetryDelay,
		}, nil
	default:
		return outboxPublisherRuntime{}, errors.New("OUTBOX_PUBLISHER must be explicitly set to history, https, or jetstream")
	}
}

func loadOutboxJetStreamConfig(environment string, maxRetryDelay time.Duration) (eventbus.Config, error) {
	connectTimeout, err := positiveDuration("OUTBOX_NATS_CONNECT_TIMEOUT", 5*time.Second)
	if err != nil {
		return eventbus.Config{}, err
	}
	publishTimeout, err := positiveDuration("OUTBOX_NATS_PUBLISH_TIMEOUT", 10*time.Second)
	if err != nil {
		return eventbus.Config{}, err
	}
	reconnectWait, err := positiveDuration("OUTBOX_NATS_RECONNECT_WAIT", time.Second)
	if err != nil {
		return eventbus.Config{}, err
	}
	maxReconnects, err := positiveInt("OUTBOX_NATS_MAX_RECONNECTS", 60)
	if err != nil {
		return eventbus.Config{}, err
	}
	maxAge, err := positiveDuration("OUTBOX_JETSTREAM_MAX_AGE", 7*24*time.Hour)
	if err != nil {
		return eventbus.Config{}, err
	}
	duplicateWindow, err := positiveDuration("OUTBOX_JETSTREAM_DUPLICATE_WINDOW", 30*time.Minute)
	if err != nil {
		return eventbus.Config{}, err
	}
	maxBytes, err := positiveInt64("OUTBOX_JETSTREAM_MAX_BYTES", 10<<30)
	if err != nil {
		return eventbus.Config{}, err
	}
	maxMessages, err := positiveInt64("OUTBOX_JETSTREAM_MAX_MESSAGES", 10_000_000)
	if err != nil {
		return eventbus.Config{}, err
	}
	maxMessageBytes, err := positiveInt64("OUTBOX_JETSTREAM_MAX_MESSAGE_BYTES", 1<<20)
	if err != nil || maxMessageBytes > int64(^uint32(0)>>1) {
		return eventbus.Config{}, errors.New("OUTBOX_JETSTREAM_MAX_MESSAGE_BYTES is invalid")
	}
	replicas, err := positiveInt("OUTBOX_JETSTREAM_REPLICAS", 3)
	if err != nil {
		return eventbus.Config{}, err
	}
	return eventbus.Config{
		Environment: environment,
		URLs:        splitNonempty(os.Getenv("OUTBOX_NATS_URLS")),
		CAFile:      os.Getenv("OUTBOX_NATS_CA_FILE"), ServerName: os.Getenv("OUTBOX_NATS_SERVER_NAME"),
		ClientCertFile: os.Getenv("OUTBOX_NATS_CLIENT_CERT_FILE"), ClientKeyFile: os.Getenv("OUTBOX_NATS_CLIENT_KEY_FILE"),
		CredentialsFile: os.Getenv("OUTBOX_NATS_CREDS_FILE"), TokenFile: os.Getenv("OUTBOX_NATS_TOKEN_FILE"),
		ConnectTimeout: connectTimeout, PublishTimeout: publishTimeout, ReconnectWait: reconnectWait,
		MaxReconnects: maxReconnects, MaxRetryDelay: maxRetryDelay,
		Stream: eventbus.StreamPolicy{
			MaxAge: maxAge, MaxBytes: maxBytes, MaxMessages: maxMessages, MaxMessageBytes: int32(maxMessageBytes),
			DuplicateWindow: duplicateWindow, Replicas: replicas,
		},
	}, nil
}

func positiveInt64(key string, fallback int64) (int64, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 1 {
		return 0, errors.New(key + " must be a positive integer")
	}
	return value, nil
}

func readOutboxSecret(envName string, maximum int) (string, error) {
	path := os.Getenv(envName)
	if path == "" {
		return "", errors.New(envName + " must reference a secret file")
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New(envName + " secret file is unavailable")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", errors.New(envName + " secret file is unavailable")
	}
	value := strings.TrimSpace(string(raw))
	if value == "" || len(value) > maximum || strings.ContainsAny(value, "\x00\r\n") {
		return "", errors.New(envName + " secret file is invalid")
	}
	return value, nil
}
