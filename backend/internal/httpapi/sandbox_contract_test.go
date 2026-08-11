package httpapi_test

import (
	"os"
	"strings"
	"testing"
)

func TestSandboxOpenAPITracksFencedRuntime(t *testing.T) {
	raw, err := os.ReadFile("../../../contracts/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	contract := string(raw)
	for _, marker := range []string{
		"/v1/sandbox/workspace:", "/v1/sandbox/scenarios:", "/v1/sandbox/scenarios/{id}:",
		"/v1/sandbox/scenarios/{id}/actions:", "/v1/sandbox/scenarios/{id}/run:",
		"/v1/sandbox/callbacks:", "/v1/sandbox/clock/advance:", "/v1/sandbox/reset:",
		"/v1/sandbox/simulations:", "SandboxScenarioKind:", "out_of_order_callback", "reorg_recovery",
		"Exact stored bytes decoded as JSON", "const: '[REDACTED]'", "Production IDs return 404",
	} {
		if !strings.Contains(contract, marker) {
			t.Fatalf("sandbox OpenAPI is missing %q", marker)
		}
	}
	if strings.Contains(contract, "settle_then_deep_reorg") || strings.Contains(contract, "unmatched_wrong_asset") {
		t.Fatal("sandbox OpenAPI still advertises legacy one-shot scenarios")
	}
}

func TestSandboxExecutableCompositionIsPostgresOnlyAndEnvironmentFenced(t *testing.T) {
	raw, err := os.ReadFile("../../cmd/api/main.go")
	if err != nil {
		t.Fatal(err)
	}
	composition := string(raw)
	for _, marker := range []string{
		`cfg.Environment == "test" || cfg.Environment == "sandbox"`,
		`sandbox.NewPostgresRepository(pool)`,
		`SANDBOX_RESET_HMAC_KEY`,
		`apiServer.EnableSandbox(sandboxService)`,
		`readiness = append(probes, sandboxRepository)`,
	} {
		if !strings.Contains(composition, marker) {
			t.Fatalf("sandbox executable composition is missing %q", marker)
		}
	}
	if strings.Contains(composition, "sandbox.NewMemoryRepository") {
		t.Fatal("executable composition contains a sandbox memory fallback")
	}
}
