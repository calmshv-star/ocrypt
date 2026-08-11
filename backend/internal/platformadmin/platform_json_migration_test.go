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
