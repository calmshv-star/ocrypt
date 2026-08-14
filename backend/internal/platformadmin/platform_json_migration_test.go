package platformadmin

import (
	"os"
	"strings"
	"testing"
)

func TestPlatformJSONValidationMigrationQualifiesSetReturningColumns(t *testing.T) {
	raw, err := os.ReadFile("../../migrations/000024_platform_json_validation_fix.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	for _, required := range []string{
		"jsonb_each(value) AS item(key,value)",
		"SELECT item.key,item.value",
		"jsonb_array_elements(value) AS item(value)",
		"SELECT item.value",
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("migration is missing qualified JSON projection %q", required)
		}
	}
	if strings.Contains(source, "SELECT key,value FROM jsonb_each(value)") ||
		strings.Contains(source, "SELECT value FROM jsonb_array_elements(value)") {
		t.Fatal("migration reintroduces PostgreSQL 18 ambiguous JSON column references")
	}
}

func TestProviderOperationalCountersAreNotMisclassifiedAsMoney(t *testing.T) {
	raw, err := os.ReadFile("../../migrations/000045_provider_rate_limit_validation.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	if strings.Count(source, "k NOT IN ('rate_limit','failure_threshold')") != 2 {
		t.Fatal("provider rate/failure counters are not exempted consistently from exact-money validation")
	}
	for _, moneyKey := range []string{"amount", "balance", "minimum", "maximum", "dust", "fee"} {
		if !strings.Contains(source, moneyKey) {
			t.Fatalf("money key %q lost exact-string validation", moneyKey)
		}
	}
	if !strings.Contains(source, "SELECT id,logical_key,payload INTO s") ||
		strings.Contains(source, "SELECT id,logical_key,payload,updated_at INTO s") {
		t.Fatal("RPC head synchronization still reads the non-existent snapshot updated_at column")
	}
}
