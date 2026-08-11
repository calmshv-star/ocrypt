package outbox_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/outbox"
)

func TestCanonicalJSONHasStableEnvelopeAndPayloadOrder(t *testing.T) {
	message := completeMessage()
	message.Payload = json.RawMessage(`{"z":2,"a":{"y":true,"x":1}}`)
	wire, err := outbox.CanonicalJSON(message)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"event_id":"event-1","tenant_id":"tenant-1","merchant_id":"merchant-1","aggregate_type":"payment","aggregate_id":"intent-1","aggregate_version":1,"sequence":1,"event_type":"payment.settled","schema_version":"1","payload":{"a":{"x":1,"y":true},"z":2},"occurred_at":"2026-07-27T06:00:00Z","recorded_at":"2026-07-27T10:00:01Z"}`
	if string(wire) != want {
		t.Fatalf("canonical event mismatch\n got: %s\nwant: %s", wire, want)
	}
	parsed, err := outbox.ParseCanonicalJSON(wire)
	if err != nil || parsed.EventID != message.EventID {
		t.Fatalf("parse canonical: event=%+v err=%v", parsed, err)
	}
}

func TestParseCanonicalJSONRejectsAlternateEncoding(t *testing.T) {
	wire, err := outbox.CanonicalJSON(completeMessage())
	if err != nil {
		t.Fatal(err)
	}
	noncanonical := []byte(strings.Replace(string(wire), `"payload":{"amount":100}`, `"payload": {"amount":100}`, 1))
	if _, err := outbox.ParseCanonicalJSON(noncanonical); err == nil {
		t.Fatal("expected alternate JSON encoding rejection")
	}
}

func TestCanonicalJSONNormalizesEquivalentNumberSpellings(t *testing.T) {
	message := completeMessage()
	message.Payload = json.RawMessage(`{"fraction":1.2300,"integer":1e2,"negative_zero":-0}`)
	wire, err := outbox.CanonicalJSON(message)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(wire), `"payload":{"fraction":1.23,"integer":100,"negative_zero":0}`) {
		t.Fatalf("numbers are not canonical: %s", wire)
	}
}

func TestCanonicalJSONRejectsUnsafeMessageIDHeaderValue(t *testing.T) {
	message := completeMessage()
	message.EventID = "event-1\r\nNats-Expected-Stream: OTHER"
	if _, err := outbox.CanonicalJSON(message); err == nil {
		t.Fatal("expected unsafe message ID rejection")
	}
}

func completeMessage() outbox.Message {
	return outbox.Message{
		EventID:          "event-1",
		TenantID:         "tenant-1",
		MerchantID:       "merchant-1",
		AggregateType:    "payment",
		AggregateID:      "intent-1",
		AggregateVersion: 1,
		Sequence:         1,
		EventType:        "payment.settled",
		SchemaVersion:    "1",
		Payload:          json.RawMessage(`{"amount":100}`),
		OccurredAt:       time.Date(2026, 7, 27, 10, 0, 0, 0, time.FixedZone("offset", 4*60*60)),
		RecordedAt:       time.Date(2026, 7, 27, 10, 0, 1, 0, time.UTC),
	}
}
