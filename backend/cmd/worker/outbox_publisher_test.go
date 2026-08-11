package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestOutboxPublisherModeIsExplicitAndFailsClosed(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("OUTBOX_PUBLISHER", "")
	if _, err := newOutboxPublisher(t.Context()); err == nil || !strings.Contains(err.Error(), "explicitly") {
		t.Fatalf("expected explicit selector rejection, got %v", err)
	}
	t.Setenv("OUTBOX_PUBLISHER", "auto")
	if _, err := newOutboxPublisher(t.Context()); err == nil {
		t.Fatal("expected unsupported selector rejection")
	}
}

func TestHTTPSPublisherReadsTokenOnlyFromFile(t *testing.T) {
	path := t.TempDir() + "/token"
	if err := os.WriteFile(path, []byte("external-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("APP_ENV", "production")
	t.Setenv("OUTBOX_PUBLISHER", "https")
	t.Setenv("OUTBOX_PUBLISH_URL", "https://delivery.internal/events")
	t.Setenv("OUTBOX_PUBLISH_TOKEN_FILE", path)
	runtime, err := newOutboxPublisher(t.Context())
	if err != nil || runtime.publisher == nil || runtime.maxRetryDelay != 15*time.Minute {
		t.Fatalf("runtime=%+v err=%v", runtime, err)
	}
	if runtime.readiness != nil {
		t.Fatal("HTTPS mode must not pretend to have a broker readiness contract")
	}
}

func TestJetStreamConfigUsesOneRetryAndDuplicateSafetyBoundary(t *testing.T) {
	t.Setenv("OUTBOX_NATS_URLS", "tls://nats-a.internal:4222,tls://nats-b.internal:4222")
	t.Setenv("OUTBOX_NATS_CA_FILE", "/run/secrets/nats/ca.pem")
	t.Setenv("OUTBOX_NATS_SERVER_NAME", "nats.internal")
	t.Setenv("OUTBOX_NATS_CLIENT_CERT_FILE", "/run/secrets/nats/tls.crt")
	t.Setenv("OUTBOX_NATS_CLIENT_KEY_FILE", "/run/secrets/nats/tls.key")
	t.Setenv("OUTBOX_NATS_CREDS_FILE", "/run/secrets/nats/publisher.creds")
	t.Setenv("OUTBOX_NATS_TOKEN_FILE", "")
	t.Setenv("OUTBOX_JETSTREAM_DUPLICATE_WINDOW", "20m")
	maximum := 20 * time.Minute
	config, err := loadOutboxJetStreamConfig("production", maximum)
	if err != nil {
		t.Fatal(err)
	}
	if config.MaxRetryDelay != maximum || config.Stream.DuplicateWindow != maximum || config.Stream.MaxMessageBytes != 1<<20 || config.Stream.Replicas != 3 {
		t.Fatalf("retry/stream safety policy diverged: %+v", config)
	}
}
