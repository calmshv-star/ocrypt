package legacycompat

import (
	"os"
	"strings"
	"testing"
)

func TestMigrationRequiresIdentityBackedFourEyesAndContiguousCursor(t *testing.T) {
	body, err := os.ReadFile("../../migrations/000018_legacy_compatibility.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	for _, required := range []string{"pg_has_role(session_user,'legacy_compat_admission_requester','member')", "pg_has_role(session_user,'legacy_compat_admission_approver','member')", "request_row.requested_by=session_user", "request_legacy_compat_config_admission(requested_request_id uuid,requested_manifest jsonb)", "approve_legacy_compat_config_admission(requested_request_id uuid,approved_manifest jsonb)", "requested_sequence<>current_sequence+1", "frozen_body=requested_body", "m.notify_url=requested_target", "v.callback_key_id=requested_key_id", "lease_expired_attempt_limit", "requested_status<>200", "a.chain_id=request_row.chain_id", "m.status='active'", "clock_timestamp()", "interval '30 minutes'", "lease_until>authoritative_at", "pg_has_role(session_user,'legacy_compat_runtime','member')", "000018_legacy_compatibility.up.sql", "has_table_privilege(session_user"} {
		if !strings.Contains(sql, required) {
			t.Fatalf("migration missing %q", required)
		}
	}
	if strings.Contains(sql, "octet_length(requested_approval_signature)") {
		t.Fatal("length-only signature admission remains")
	}
}

func TestForwardMigrationBindsCallbackKeyIntoAdmissionManifest(t *testing.T) {
	raw, err := os.ReadFile("../../migrations/000026_legacy_callback_manifest_fix.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	if !strings.Contains(source, "'callback_key_id',requested_callback_key_id") || !strings.Contains(source, "requested_manifest<>canonical_manifest") {
		t.Fatal("legacy admission manifest does not bind the callback signing key")
	}
}
