package webhook

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/money"
)

func TestSettledWebhookGoldenBody(t *testing.T) {
	event := Event{
		EventID: "evt_01", EventType: "payment.settled", SchemaVersion: "1", Sequence: 3,
		OccurredAt: time.Date(2026, 8, 10, 12, 34, 56, 123_000_000, time.UTC), MerchantID: "m_01", Livemode: false,
		PaymentIntent: PaymentIntentSnapshot{ID: "pi_01", MerchantOrderID: "order-01", Status: "settled", AmountMinor: money.MustParse("3813"), Currency: "USD"},
		Settlement: &Settlement{SettlementID: "set_01", AssetID: "usdt-ethereum", Network: "ethereum", ExpectedRaw: money.MustParse("38130000"),
			ReceivedRaw: money.MustParse("38130000"), CreditedRaw: money.MustParse("38130000"), TransactionHash: "0xabc", EventIndex: "trace:0,2,1",
			BlockHeight: "21900123", BlockTime: time.Date(2026, 8, 10, 12, 34, 50, 0, time.UTC), Finality: "finalized", ManualResolution: false},
	}
	body, err := CanonicalBody(event)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"event_id":"evt_01","event_type":"payment.settled","schema_version":"1","sequence":3,"occurred_at":"2026-08-10T12:34:56.123Z","merchant_id":"m_01","livemode":false,"payment_intent":{"id":"pi_01","merchant_order_id":"order-01","status":"settled","amount_minor":"3813","currency":"USD"},"settlement":{"settlement_id":"set_01","asset_id":"usdt-ethereum","network":"ethereum","expected_raw":"38130000","received_raw":"38130000","credited_raw":"38130000","transaction_hash":"0xabc","event_index":"trace:0,2,1","block_height":"21900123","block_time":"2026-08-10T12:34:50Z","finality":"finalized","manual_resolution":false}}`
	if string(body) != want {
		t.Fatalf("webhook schema drifted\nwant: %s\n got: %s", want, body)
	}
	if err := ValidateAcknowledgement([]byte(`{"acknowledged_event_id":"evt_01"}`), "evt_01"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateAcknowledgement([]byte(`{"acknowledged_event_id":"evt_other"}`), "evt_01"); err == nil {
		t.Fatal("mismatched acknowledgement passed")
	}
	for _, invalid := range []string{``, `{}`, `{"acknowledged_event_id":"evt_01","acknowledged_event_id":"evt_01"}`, `{"acknowledged_event_id":"evt_01","extra":true}`, `{"acknowledged_event_id":"evt_01"}{}`} {
		if err := ValidateAcknowledgement([]byte(invalid), "evt_01"); err == nil {
			t.Fatalf("invalid acknowledgement passed: %s", invalid)
		}
	}
}

func TestPublicEventJSONSchemaUsesStableNames(t *testing.T) {
	data, err := os.ReadFile("../../../contracts/events/payment-event-v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Required   []string `json:"required"`
		Properties map[string]struct {
			Enum []string `json:"enum"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	required := map[string]bool{}
	for _, key := range schema.Required {
		required[key] = true
	}
	for _, key := range []string{"event_id", "event_type", "schema_version", "sequence", "occurred_at", "merchant_id", "payment_intent"} {
		if !required[key] {
			t.Fatalf("public event schema is missing %q", key)
		}
	}
	if _, ok := schema.Properties["id"]; ok {
		t.Fatal("generic id/type fields drifted into the public event schema")
	}
	if _, ok := schema.Properties["type"]; ok {
		t.Fatal("generic id/type fields drifted into the public event schema")
	}
	if strings.Join(schema.Properties["event_type"].Enum, "\x1f") != strings.Join(SupportedEventTypes, "\x1f") {
		t.Fatalf("JSON Schema event taxonomy drifted: %v", schema.Properties["event_type"].Enum)
	}
	openAPI, err := os.ReadFile("../../../contracts/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	openAPIEnum := "enum: [" + strings.Join(SupportedEventTypes, ", ") + "]"
	if !bytes.Contains(openAPI, []byte(openAPIEnum)) {
		t.Fatalf("OpenAPI event taxonomy drifted from runtime: %s", openAPIEnum)
	}
	asyncAPI, err := os.ReadFile("../../../contracts/asyncapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(asyncAPI, []byte("./events/payment-event-v1.schema.json")) {
		t.Fatal("AsyncAPI no longer references the versioned public event schema")
	}
	for _, marker := range []string{"Merchant-Webhook-Signature", "Merchant-Delivery-Id", "Content-Digest", "base64url HMAC-SHA256 without padding", "may arrive out of order"} {
		if !bytes.Contains(asyncAPI, []byte(marker)) {
			t.Fatalf("AsyncAPI is missing runtime webhook contract marker %q", marker)
		}
	}
	for _, stale := range []string{"Webhook-Id", "Webhook-Timestamp", "v1=<hex"} {
		if bytes.Contains(asyncAPI, []byte(stale)) {
			t.Fatalf("AsyncAPI still contains stale webhook header %q", stale)
		}
	}
}

func TestObservationAndResolutionWebhookShapesAreBounded(t *testing.T) {
	now := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)
	base := Event{EventID: "evt_observed", EventType: "payment.observed", SchemaVersion: "1", Sequence: 8, OccurredAt: now, MerchantID: "merchant-1", Livemode: true, PaymentIntent: PaymentIntentSnapshot{ID: "intent-1", MerchantOrderID: "order-1", Status: "observed", AmountMinor: money.MustParse("789"), Currency: "USD"}}
	if _, err := CanonicalBody(base); err == nil {
		t.Fatal("observation lifecycle event passed without an observation")
	}
	base.Observation = &Observation{ObservationID: "obs-1", PaymentRouteID: "route-1", Network: "eip155:1", AssetID: "usdt-ethereum", TransactionHash: "0xabc", EventIndex: "log:0", FromAddress: "0xfrom", ToAddress: "0xto", AmountRaw: money.MustParse("7890000"), AssetDecimals: 6, BlockHeight: "123", BlockHash: "0xblock", BlockTime: now, Confirmations: 2, RequiredConfirmations: 12, Finality: "observed", EvidenceHash: strings.Repeat("a", 64)}
	body, err := CanonicalBody(base)
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{`"observation_id":"obs-1"`, `"confirmations":2`, `"required_confirmations":12`, `"evidence_sha256":"` + strings.Repeat("a", 64) + `"`} {
		if !bytes.Contains(body, []byte(marker)) {
			t.Fatalf("observation body missing %s: %s", marker, body)
		}
	}
	resolution := base
	resolution.EventID, resolution.EventType, resolution.Observation = "evt-resolution", "payment.resolution.updated", nil
	if _, err = CanonicalBody(resolution); err == nil {
		t.Fatal("resolution event passed without a resolution object")
	}
	resolution.Resolution = &Resolution{ResolutionID: "resolution-1", UnmatchedPaymentID: "unmatched-1", TransferEventID: "transfer-1", PaymentRouteID: "route-1", Status: "verification_requested", Version: 2, ApprovalRequired: true, Approved: true, AcceptShortfall: true}
	body, err = CanonicalBody(resolution)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"requested_by", "approved_by", "human_reason", "last_error"} {
		if bytes.Contains(body, []byte(forbidden)) {
			t.Fatalf("resolution body leaked %q", forbidden)
		}
	}
	settled := base
	settled.EventID, settled.EventType = "evt-settled", "payment.settled"
	settled.Settlement = &Settlement{SettlementID: "settlement-1"}
	if _, err = CanonicalBody(settled); err == nil {
		t.Fatal("settled event accepted both settlement and observation")
	}
}
