package eventbus

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/outbox"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

func TestPublisherRequiresMatchingAcknowledgementAndAcceptsDuplicate(t *testing.T) {
	tests := []struct {
		name string
		ack  *jetstream.PubAck
		want bool
	}{
		{name: "accepted", ack: &jetstream.PubAck{Stream: StreamName, Sequence: 42}},
		{name: "duplicate accepted", ack: &jetstream.PubAck{Stream: StreamName, Sequence: 42, Duplicate: true}},
		{name: "wrong stream rejected", ack: &jetstream.PubAck{Stream: "OTHER", Sequence: 42}, want: true},
		{name: "zero sequence rejected", ack: &jetstream.PubAck{Stream: StreamName}, want: true},
		{name: "missing ack rejected", want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var got *nats.Msg
			publisher := &Publisher{
				publish: func(_ context.Context, message *nats.Msg, _ ...jetstream.PublishOpt) (*jetstream.PubAck, error) {
					got = message
					return test.ack, nil
				},
				policy:  testPolicy(),
				timeout: time.Second,
			}
			err := publisher.Publish(t.Context(), eventFixture())
			if (err != nil) != test.want {
				t.Fatalf("err=%v wantError=%v", err, test.want)
			}
			if got == nil || got.Subject != Subject || got.Header.Get("Content-Type") != "application/json" || len(got.Header) != 1 {
				t.Fatalf("unexpected published message: %+v", got)
			}
		})
	}
}

func TestPublisherRejectsOversizedCanonicalEnvelopeBeforeNetwork(t *testing.T) {
	called := false
	policy := testPolicy()
	policy.MaxMessageBytes = 32
	publisher := &Publisher{
		publish: func(context.Context, *nats.Msg, ...jetstream.PublishOpt) (*jetstream.PubAck, error) {
			called = true
			return nil, errors.New("must not publish")
		},
		policy: policy, timeout: time.Second,
	}
	if err := publisher.Publish(t.Context(), eventFixture()); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected size rejection, got %v", err)
	}
	if called {
		t.Fatal("oversized event reached JetStream")
	}
}

func TestPublisherReadinessAlwaysUsesBoundedContext(t *testing.T) {
	publisher := &Publisher{
		publish: func(context.Context, *nats.Msg, ...jetstream.PublishOpt) (*jetstream.PubAck, error) { return nil, nil },
		flush: func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		},
		streamInfo: func(context.Context) (*jetstream.StreamInfo, error) { return nil, errors.New("not reached") },
		policy:     testPolicy(), timeout: 10 * time.Millisecond,
	}
	started := time.Now()
	if err := publisher.Ready(context.Background()); err == nil {
		t.Fatal("expected bounded readiness failure")
	}
	if time.Since(started) > time.Second {
		t.Fatal("readiness ignored its configured bound")
	}
}

func TestValidateStreamRejectsPolicyDriftIncludingMessageSize(t *testing.T) {
	policy := testPolicy()
	actual := streamFixture(policy)
	if err := validateStream(actual, policy, "production"); err != nil {
		t.Fatal(err)
	}
	actual.MaxMsgSize++
	if err := validateStream(actual, policy, "production"); err == nil {
		t.Fatal("expected MaxMsgSize drift rejection")
	}
}

func TestValidateConfigRejectsPlaintextHTTPRedirectMixedAuthAndUnsafeRetryWindow(t *testing.T) {
	valid := configFixture()
	if err := validateConfig(valid); err != nil {
		t.Fatal(err)
	}
	tests := []Config{
		func() Config { c := valid; c.URLs = []string{"nats://nats.internal:4222"}; return c }(),
		func() Config { c := valid; c.URLs = []string{"https://nats.internal:443/redirect"}; return c }(),
		func() Config {
			c := valid
			c.URLs = []string{"tls://nats-a.internal:4222", "wss://nats-b.internal:443"}
			return c
		}(),
		func() Config { c := valid; c.URLs = []string{"tls://token@nats.internal:4222"}; return c }(),
		func() Config { c := valid; c.ServerName = "nats internal"; return c }(),
		func() Config { c := valid; c.CredentialsFile = "creds"; return c }(),
		func() Config { c := valid; c.TokenFile = ""; return c }(),
		func() Config { c := valid; c.Stream.DuplicateWindow = c.MaxRetryDelay - time.Second; return c }(),
		func() Config { c := valid; c.Stream.Replicas = 2; return c }(),
	}
	for index, config := range tests {
		if err := validateConfig(config); err == nil {
			t.Fatalf("case %d: expected rejection", index)
		}
	}
}

func TestReadSecretFileRejectsMultilineValue(t *testing.T) {
	path := t.TempDir() + "/token"
	if err := os.WriteFile(path, []byte("one\ntwo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readSecretFile(path, 100); err == nil {
		t.Fatal("expected multiline secret rejection")
	}
}

func testPolicy() StreamPolicy {
	return StreamPolicy{
		MaxAge: 7 * 24 * time.Hour, MaxBytes: 10 << 30, MaxMessages: 10_000_000,
		MaxMessageBytes: 1 << 20, DuplicateWindow: 30 * time.Minute, Replicas: 3,
	}
}

func configFixture() Config {
	return Config{
		Environment: "production", URLs: []string{"tls://nats.internal:4222"}, CAFile: "ca", ServerName: "nats.internal",
		ClientCertFile: "cert", ClientKeyFile: "key", TokenFile: "token", ConnectTimeout: 5 * time.Second,
		PublishTimeout: 10 * time.Second, ReconnectWait: time.Second, MaxReconnects: 60, MaxRetryDelay: 15 * time.Minute,
		Stream: testPolicy(),
	}
}

func streamFixture(policy StreamPolicy) jetstream.StreamConfig {
	return jetstream.StreamConfig{
		Name: StreamName, Subjects: []string{Subject}, Retention: jetstream.LimitsPolicy,
		MaxMsgs: policy.MaxMessages, MaxBytes: policy.MaxBytes, MaxMsgSize: policy.MaxMessageBytes,
		Discard: jetstream.DiscardOld, MaxAge: policy.MaxAge, Storage: jetstream.FileStorage,
		Replicas: policy.Replicas, Duplicates: policy.DuplicateWindow, DenyDelete: true, DenyPurge: true,
	}
}

func eventFixture() outbox.Message {
	return outbox.Message{
		EventID: "event-1", TenantID: "tenant-1", MerchantID: "merchant-1", AggregateType: "payment",
		AggregateID: "intent-1", AggregateVersion: 1, Sequence: 1, EventType: "payment.settled", SchemaVersion: "1",
		Payload: json.RawMessage(`{"amount":100}`), OccurredAt: time.Date(2026, 7, 27, 6, 0, 0, 0, time.UTC),
		RecordedAt: time.Date(2026, 7, 27, 6, 0, 1, 0, time.UTC),
	}
}
