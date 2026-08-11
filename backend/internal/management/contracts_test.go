package management

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/calmshv-star/ocrypt/backend/internal/webhook"
	"gopkg.in/yaml.v3"
)

func TestManagementWebhookEventAllowlistMatchesCanonicalContract(t *testing.T) {
	if !knownWebhookEvent("*") {
		t.Fatal("wildcard subscription must remain supported")
	}
	for _, eventType := range webhook.SupportedEventTypes {
		if !knownWebhookEvent(eventType) {
			t.Fatalf("canonical webhook event is not configurable: %s", eventType)
		}
	}
	for _, stale := range []string{"payment.detected", "resolution.updated"} {
		if knownWebhookEvent(stale) {
			t.Fatalf("stale webhook alias remains configurable: %s", stale)
		}
	}
	var contract map[string]any
	if err := yaml.Unmarshal([]byte(mustRead(t, "../../../contracts/management-openapi.yaml")), &contract); err != nil {
		t.Fatal(err)
	}
	schemas := contract["components"].(map[string]any)["schemas"].(map[string]any)
	selection := schemas["WebhookEventTypeSelection"].(map[string]any)
	if selection["maxItems"] != 32 {
		t.Fatalf("OpenAPI webhook event bound drifted: %v", selection["maxItems"])
	}
	items := selection["items"].(map[string]any)["enum"].([]any)
	contractEvents := make(map[string]bool, len(items))
	for _, item := range items {
		contractEvents[item.(string)] = true
	}
	if !contractEvents["*"] || len(contractEvents) != len(webhook.SupportedEventTypes)+1 {
		t.Fatalf("OpenAPI webhook event selection differs from runtime: %v", contractEvents)
	}
	for _, eventType := range webhook.SupportedEventTypes {
		if !contractEvents[eventType] {
			t.Fatalf("OpenAPI cannot select canonical webhook event: %s", eventType)
		}
	}
}

func TestManagementOpenAPICoversEveryHTTPRoute(t *testing.T) {
	source := mustRead(t, "../../../contracts/management-openapi.yaml")
	for _, path := range []string{
		"/v1/public/payment-links/{token}",
		"/v1/public/payment-links/{token}/redeem",
		"/v1/checkout-sessions/{token}",
		"/v1/checkout-sessions/{token}/select-route",
		"/v1/checkout-sessions/{token}/receipt",
		"/v1/management/payment-links",
		"/v1/management/payment-links/{id}/disable",
		"/v1/management/checkout-sessions",
		"/v1/payment-links",
		"/v1/payment-links/{id}",
		"/v1/payment-links/{id}/disable",
		"/v1/checkout-sessions",
		"/v1/management/webhook-endpoints",
		"/v1/management/webhook-endpoints/{id}/rotate-secret",
		"/v1/management/webhook-endpoints/{id}/disable-requests",
		"/v1/management/webhook-deliveries/{id}/retry",
		"/v1/management/api-clients",
		"/v1/management/api-clients/{id}/revoke",
		"/v1/management/api-clients/{id}/revoke-requests",
		"/v1/management/action-requests/{category}/{id}/approve",
		"/v1/management/action-requests/{category}/{id}/reject",
		"/v1/management/audit",
	} {
		if !strings.Contains(source, "  "+path+":") {
			t.Fatalf("OpenAPI route missing: %s", path)
		}
	}
	for _, invariant := range []string{"maxItems: 1", "Idempotency-Key", "ManagementAdmin", "Cache-Control", "no-store", "success_url", "cancel_url"} {
		if !strings.Contains(source, invariant) {
			t.Fatalf("OpenAPI invariant missing: %s", invariant)
		}
	}
}

func TestContractYAMLHasNoDuplicateBlockMappingKeys(t *testing.T) {
	files, err := filepath.Glob("../../../contracts/*api.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		t.Run(filepath.Base(file), func(t *testing.T) {
			raw, readErr := os.Open(file)
			if readErr != nil {
				t.Fatal(readErr)
			}
			defer raw.Close()
			decoder := yaml.NewDecoder(raw)
			for {
				var document any
				err := decoder.Decode(&document)
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatalf("strict YAML decode: %v", err)
				}
			}
		})
	}
}

func TestPublishedRouteSelectorExamplesRequireProviderDiscriminator(t *testing.T) {
	files := []string{
		"../../../contracts/openapi.yaml",
		"../../../apps/landing/src/App.tsx",
		"../../../apps/admin/src/ManagementPages.tsx",
		"../../../sdk/fixtures/golden-vectors.json",
		"../../../tests/e2e/admin-management.spec.ts",
		"../../../tests/e2e/checkout.spec.ts",
	}
	for _, locale := range []string{"en", "de", "es", "fr", "ru", "zh-CN"} {
		files = append(files, "../../../docs/"+locale+"/guide.md")
	}
	for _, file := range files {
		source := mustRead(t, file)
		compact := strings.NewReplacer(" ", "", "\t", "", "\n", "", "\r", "").Replace(source)
		for _, stale := range []string{`allowed_routes":[{"chain_id"`, `allowed_routes:[{chain_id`, `allowed_routes:-chain_id`} {
			if strings.Contains(compact, stale) {
				t.Fatalf("provider-less route selector remains in %s", file)
			}
		}
	}
}

func TestManagementMigrationHasCapabilityAndTenantInvariants(t *testing.T) {
	source := mustRead(t, "../../migrations/000004_management.up.sql")
	required := []string{
		"jsonb_array_length(allowed_routes) = 1",
		"ALTER TABLE payment_links FORCE ROW LEVEL SECURITY",
		"ALTER TABLE management_audit_log FORCE ROW LEVEL SECURITY",
		"checkout_sessions_selected_route_fk",
		"checkout_sessions_payment_link_fk",
		"payment_link_redemption_checkout_fk",
		"REVOKE INSERT,UPDATE,DELETE ON management_audit_log FROM PUBLIC",
		"validate_management_actor(uuid,uuid,uuid,text)",
		"payment_links:write",
		"api_clients:revoke",
		"CREATE TABLE management_action_requests",
		"approved_by<>requested_by",
		"request_hash bytea NOT NULL CHECK (octet_length(request_hash)=32)",
		"ALTER TABLE management_action_requests FORCE ROW LEVEL SECURITY",
	}
	for _, fragment := range required {
		if !strings.Contains(source, fragment) {
			t.Fatalf("migration invariant missing: %s", fragment)
		}
	}
	if strings.Contains(source, "public_url text") || strings.Contains(source, "'pl_'||") {
		t.Fatal("payment-link bearer capability must never be persisted in plaintext")
	}
	lookupStart := strings.Index(source, "CREATE FUNCTION lookup_payment_link")
	lookupEnd := strings.Index(source[lookupStart:], "$$;")
	if lookupStart < 0 || lookupEnd < 0 {
		t.Fatal("payment-link lookup function missing")
	}
	lookup := source[lookupStart : lookupStart+lookupEnd]
	if strings.Contains(lookup, "use_count<") || strings.Contains(lookup, "l.status='active'") {
		t.Fatal("pre-tenant lookup must not prevent idempotent replay after one-time use")
	}
}

func TestManagementRollbackDropsRedemptionDependencyBeforeCheckoutIdentity(t *testing.T) {
	source := mustRead(t, "../../migrations/000004_management.down.sql")
	redemptions := strings.Index(source, "DROP TABLE IF EXISTS payment_link_redemptions")
	identity := strings.Index(source, "DROP CONSTRAINT IF EXISTS checkout_sessions_redemption_identity_unique")
	if redemptions < 0 || identity < 0 || redemptions > identity {
		t.Fatal("management rollback removes the checkout identity before its dependent redemption table")
	}
}

func TestManagementAPIIsAuthoritativeCheckoutRuntime(t *testing.T) {
	core := mustRead(t, "../../../contracts/openapi.yaml")
	management := mustRead(t, "../../../contracts/management-openapi.yaml")
	if strings.Contains(core, "/v1/checkout-sessions/") {
		t.Fatal("core OpenAPI still advertises the management-owned checkout route")
	}
	for _, route := range []string{"/v1/checkout-sessions/{token}:", "/v1/checkout-sessions/{token}/select-route:", "/v1/checkout-sessions/{token}/receipt:"} {
		if !strings.Contains(management, route) {
			t.Fatalf("authoritative management checkout route missing: %s", route)
		}
	}
	for _, route := range []string{"/v1/payment-links:", "/v1/payment-links/{id}:", "/v1/payment-links/{id}/disable:", "/v1/checkout-sessions:"} {
		if !strings.Contains(management, route) {
			t.Fatalf("authoritative server-to-server management route missing: %s", route)
		}
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
