package eventbus

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/outbox"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

const (
	StreamName = "MERCHANT_EVENTS_V1"
	Subject    = "merchant.events.v1"
)

type Config struct {
	Environment     string
	URLs            []string
	CAFile          string
	ServerName      string
	ClientCertFile  string
	ClientKeyFile   string
	CredentialsFile string
	TokenFile       string
	ConnectTimeout  time.Duration
	PublishTimeout  time.Duration
	ReconnectWait   time.Duration
	MaxReconnects   int
	MaxRetryDelay   time.Duration
	Stream          StreamPolicy
}

type StreamPolicy struct {
	MaxAge          time.Duration
	MaxBytes        int64
	MaxMessages     int64
	MaxMessageBytes int32
	DuplicateWindow time.Duration
	Replicas        int
}

type Publisher struct {
	connection  *nats.Conn
	publish     func(context.Context, *nats.Msg, ...jetstream.PublishOpt) (*jetstream.PubAck, error)
	streamInfo  func(context.Context) (*jetstream.StreamInfo, error)
	flush       func(context.Context) error
	policy      StreamPolicy
	environment string
	timeout     time.Duration
}

func NewPublisher(ctx context.Context, config Config) (*Publisher, error) {
	if err := validateConfig(config); err != nil {
		return nil, err
	}
	tlsConfig, err := loadTLSConfig(config)
	if err != nil {
		return nil, err
	}
	options := []nats.Option{
		nats.Name("merchant-platform-outbox-v1"),
		nats.Secure(tlsConfig),
		nats.Timeout(config.ConnectTimeout),
		nats.ReconnectWait(config.ReconnectWait),
		nats.MaxReconnects(config.MaxReconnects),
		nats.NoEcho(),
	}
	if config.CredentialsFile != "" {
		if err := requireRegularFile(config.CredentialsFile); err != nil {
			return nil, errors.New("NATS credentials file is unavailable")
		}
		options = append(options, nats.UserCredentials(config.CredentialsFile))
	} else {
		token, err := readSecretFile(config.TokenFile, 4096)
		if err != nil {
			return nil, errors.New("NATS token file is invalid")
		}
		options = append(options, nats.Token(token))
	}
	connection, err := nats.Connect(strings.Join(config.URLs, ","), options...)
	if err != nil {
		// Connection errors can contain server URLs; do not pass them to logs.
		return nil, errors.New("NATS JetStream connection failed")
	}
	js, err := jetstream.New(connection)
	if err != nil {
		connection.Close()
		return nil, errors.New("NATS JetStream initialization failed")
	}
	publisher := &Publisher{
		connection: connection,
		publish:    js.PublishMsg,
		streamInfo: func(check context.Context) (*jetstream.StreamInfo, error) {
			stream, lookupErr := js.Stream(check, StreamName)
			if lookupErr != nil {
				return nil, lookupErr
			}
			return stream.Info(check)
		},
		flush:       connection.FlushWithContext,
		policy:      config.Stream,
		environment: config.Environment,
		timeout:     config.PublishTimeout,
	}
	if err := publisher.Ready(ctx); err != nil {
		connection.Close()
		return nil, err
	}
	return publisher, nil
}

func (p *Publisher) Publish(ctx context.Context, message outbox.Message) error {
	data, err := outbox.CanonicalJSON(message)
	if err != nil {
		return err
	}
	if len(data) > int(p.policy.MaxMessageBytes) {
		return errors.New("canonical event envelope exceeds the admitted JetStream message limit")
	}
	publishCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	ack, err := p.publish(publishCtx, &nats.Msg{
		Subject: Subject,
		Header:  nats.Header{"Content-Type": []string{"application/json"}},
		Data:    data,
	}, jetstream.WithMsgID(message.EventID), jetstream.WithExpectStream(StreamName))
	if err != nil {
		return errors.New("JetStream publish was not acknowledged")
	}
	if ack == nil || ack.Stream != StreamName || ack.Sequence == 0 {
		return errors.New("JetStream publish acknowledgement is invalid")
	}
	// Duplicate=true is a successful acknowledgement of the stable event ID.
	return nil
}

func (p *Publisher) Ready(ctx context.Context) error {
	if p == nil || p.publish == nil || p.streamInfo == nil || p.flush == nil {
		return errors.New("JetStream publisher is not initialized")
	}
	readyCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	if err := p.flush(readyCtx); err != nil {
		return errors.New("JetStream connection is not ready")
	}
	info, err := p.streamInfo(readyCtx)
	if err != nil || info == nil {
		return errors.New("JetStream stream is not ready")
	}
	return validateStream(info.Config, p.policy, p.environment)
}

func (p *Publisher) Close() {
	if p != nil && p.connection != nil {
		p.connection.Close()
	}
}

func validateConfig(config Config) error {
	if config.Environment != "development" && config.Environment != "test" && config.Environment != "production" {
		return errors.New("APP_ENV must be development, test, or production")
	}
	if len(config.URLs) == 0 || config.CAFile == "" || config.ServerName == "" || config.ClientCertFile == "" || config.ClientKeyFile == "" {
		return errors.New("NATS TLS configuration is incomplete")
	}
	if (config.CredentialsFile == "") == (config.TokenFile == "") {
		return errors.New("exactly one NATS credentials or token file is required")
	}
	if net.ParseIP(config.ServerName) != nil || !validDNSName(config.ServerName) {
		return errors.New("NATS TLS server name must be a DNS name")
	}
	for _, raw := range config.URLs {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Scheme != "tls" || parsed.Hostname() == "" || parsed.Port() == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
			return errors.New("all NATS servers must be credential-free tls:// host:port URLs")
		}
	}
	if config.ConnectTimeout <= 0 || config.ConnectTimeout > 30*time.Second ||
		config.PublishTimeout <= 0 || config.PublishTimeout > 30*time.Second ||
		config.ReconnectWait <= 0 || config.ReconnectWait > time.Minute || config.MaxReconnects < 1 || config.MaxReconnects > 10000 ||
		config.MaxRetryDelay <= 0 || config.MaxRetryDelay > 24*time.Hour {
		return errors.New("NATS timeout or reconnect configuration is invalid")
	}
	policy := config.Stream
	if policy.MaxAge <= 0 || policy.MaxBytes <= 0 || policy.MaxMessages <= 0 || policy.MaxMessageBytes <= 0 ||
		int64(policy.MaxMessageBytes) > policy.MaxBytes || policy.DuplicateWindow < config.MaxRetryDelay ||
		policy.Replicas < 1 || policy.Replicas > 5 || config.Environment == "production" && policy.Replicas < 3 {
		return errors.New("JetStream retention policy is invalid")
	}
	return nil
}

func validDNSName(name string) bool {
	if name == "" || len(name) > 253 || strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".") {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
				character >= '0' && character <= '9' || character == '-' {
				continue
			}
			return false
		}
	}
	return true
}

func validateStream(actual jetstream.StreamConfig, expected StreamPolicy, environment string) error {
	if actual.Name != StreamName || !slices.Equal(actual.Subjects, []string{Subject}) ||
		actual.Retention != jetstream.LimitsPolicy || actual.Discard != jetstream.DiscardOld ||
		actual.Storage != jetstream.FileStorage || actual.NoAck || actual.AllowRollup ||
		!actual.DenyDelete || !actual.DenyPurge || actual.MaxAge != expected.MaxAge ||
		actual.MaxBytes != expected.MaxBytes || actual.MaxMsgs != expected.MaxMessages ||
		actual.MaxMsgSize != expected.MaxMessageBytes ||
		actual.Duplicates != expected.DuplicateWindow || actual.Replicas != expected.Replicas ||
		expected.DuplicateWindow <= 0 || environment == "production" && actual.Replicas < 3 {
		return errors.New("JetStream stream configuration differs from the admitted policy")
	}
	return nil
}

func loadTLSConfig(config Config) (*tls.Config, error) {
	caPEM, err := os.ReadFile(config.CAFile)
	if err != nil {
		return nil, errors.New("NATS CA file is unavailable")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("NATS CA file is invalid")
	}
	certificate, err := tls.LoadX509KeyPair(config.ClientCertFile, config.ClientKeyFile)
	if err != nil {
		return nil, errors.New("NATS client certificate files are invalid")
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		MaxVersion:   tls.VersionTLS13,
		RootCAs:      roots,
		ServerName:   config.ServerName,
		Certificates: []tls.Certificate{certificate},
	}, nil
}

func requireRegularFile(path string) error {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("secret path is not a regular file")
	}
	return nil
}

func readSecretFile(path string, maximum int) (string, error) {
	if path == "" {
		return "", errors.New("secret file path is required")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(raw))
	if value == "" || len(value) > maximum || strings.ContainsAny(value, "\x00\r\n") {
		return "", fmt.Errorf("secret file value is invalid")
	}
	return value, nil
}

var _ outbox.Publisher = (*Publisher)(nil)
