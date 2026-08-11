package httpapi_test

import (
	"os"
	"strings"
	"testing"
)

func TestCheckoutOpenAPITracksRuntimeContract(t *testing.T) {
	raw, err := os.ReadFile("../../../contracts/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	contract := string(raw)
	if strings.Contains(contract, "/v1/checkout-sessions/") {
		t.Fatal("core OpenAPI must not advertise the management-owned public checkout route")
	}
	managementRaw, err := os.ReadFile("../../../contracts/management-openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	management := string(managementRaw)
	for _, marker := range []string{"/v1/checkout-sessions/{token}:", "/v1/checkout-sessions/{token}/select-route:", "^cs_[A-Za-z0-9_-]{43}$", "const: no-store"} {
		if !strings.Contains(management, marker) {
			t.Fatalf("authoritative management OpenAPI is missing checkout marker %q", marker)
		}
	}
}
