package providerops

import (
	"os"
	"strings"
	"testing"
)

func TestProviderHealthProbeProjectionNormalizesNullableScopeFields(t *testing.T) {
	data, err := os.ReadFile("postgres.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	for _, required := range []string{
		"COALESCE(tenant_id::text,'')",
		"COALESCE(merchant_id::text,'')",
		"COALESCE(chain_id,'')",
		"COALESCE(config_logical_key,'')",
		"COALESCE(platform_snapshot_id::text,'')",
		"COALESCE(probe_reference,'')",
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("provider-health projection does not normalize nullable field: %s", required)
		}
	}
}
