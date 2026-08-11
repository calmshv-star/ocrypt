package httpapi

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestMerchantRuntimeOpenAPITracksAuthoritativeRoutes(t *testing.T) {
	raw, err := os.ReadFile("../../../contracts/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Paths map[string]any `yaml:"paths"`
	}
	if err = yaml.Unmarshal(raw, &document); err != nil {
		t.Fatalf("strict OpenAPI YAML parse failed: %v", err)
	}
	for _, path := range []string{
		"/v1/merchant/orders",
		"/v1/merchant/orders/{id}",
		"/v1/payment-intents/{payment_intent_id}/expire",
		"/v1/payment-intents/{payment_intent_id}/metadata",
		"/v1/events/{event_id}",
		"/v1/transfers/{network}/{tx}",
		"/v1/quotes/{quote_id}",
		"/v1/reconciliation-reports",
		"/v1/reconciliation-reports/{report_id}",
		"/v1/reconciliation-reports/{report_id}/download",
	} {
		if _, exists := document.Paths[path]; !exists {
			t.Errorf("OpenAPI path missing: %s", path)
		}
	}
	contract := string(raw)
	eventsStart := strings.Index(contract, "  /v1/events:\n")
	eventsEnd := strings.Index(contract[eventsStart+1:], "\n  /v1/")
	if eventsStart < 0 || eventsEnd < 0 {
		t.Fatal("events OpenAPI section is missing")
	}
	events := contract[eventsStart : eventsStart+1+eventsEnd]
	if !strings.Contains(events, "AfterSequence") || strings.Contains(events, "AfterCursor") {
		t.Fatal("event pull recovery must be ascending after_sequence, not a UUID cursor")
	}
	for _, marker := range []string{"next_sequence", "reconciliation:read", "snapshot_ledger_sequence", "application/x-ndjson"} {
		if !strings.Contains(contract, marker) {
			t.Errorf("OpenAPI invariant missing %q", marker)
		}
	}
}
