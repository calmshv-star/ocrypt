package postgres

import (
	"os"
	"strings"
	"testing"
)

func TestCredentialLookupSQLFailsClosedForTenantStatusAndRevocation(t *testing.T) {
	migration, err := os.ReadFile("../../../migrations/000001_platform.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(migration)
	for _, guard := range []string{"c.revoked_at IS NULL", "c.valid_from<=clock_timestamp()", "c.valid_until>clock_timestamp()", "t.status='active'", "m.status='active'", "m.tenant_id=c.tenant_id", "c.algorithm='hmac-sha256'"} {
		if !strings.Contains(sql, guard) {
			t.Fatalf("credential lookup lost guard %q", guard)
		}
	}
	if !strings.Contains(sql, "REVOKE ALL ON FUNCTION lookup_api_credential(text) FROM PUBLIC") {
		t.Fatal("credential lookup function is executable by PUBLIC")
	}
}
