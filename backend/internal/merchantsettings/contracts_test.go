package merchantsettings

import (
	"os"
	"strings"
	"testing"
)

func TestMigrationSecurityAndDeliveryContracts(t *testing.T) {
	raw, err := os.ReadFile("../../migrations/000009_merchant_team_settings.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(raw)
	required := []string{"BEGIN;", "COMMIT;", "ALTER TABLE %I FORCE ROW LEVEL SECURITY", "app.merchant_id", "merchant_last_owner_guard", "merchant_last_owner_member_guard", "pg_advisory_xact_lock", "merchant_security_payload_hash", "merchant invitation identity and grant are immutable", "merchant security request payload is immutable", "requested_session_id", "approved_session_id<>requested_session_id", "consume_merchant_session_revocations(integer)", "claim_merchant_invitation_delivery(uuid,integer)", "complete_merchant_invitation_delivery(uuid,uuid,text)", "fail_merchant_invitation_delivery(uuid,uuid,text,integer,integer)", "attempt_count integer", "provider_delivery_id", "FOR UPDATE SKIP LOCKED", "merchant_invitation_delivery_keys_admitted", "expires_at<clock_timestamp()-interval '24 hours'", "last_seen_at<clock_timestamp()-interval '7 days'", "invitation.delivery_dead_letter", "invitation.delivery_expired", "lookup_merchant_invitation(bytea)", "token_hash bytea", "token_key_id text", "append_merchant_settings_audit", "merchant_settings_audit_immutable", "merchant_settings_versions_immutable", "support_email text CHECK", "status='pending_delivery'"}
	for _, fragment := range required {
		if !strings.Contains(sql, fragment) {
			t.Errorf("missing migration invariant %q", fragment)
		}
	}
	for _, forbidden := range []string{"invite_token text", "raw_token", "encrypted_token", "private_key", "api_secret"} {
		if strings.Contains(sql, forbidden) {
			t.Errorf("unsafe invitation/secret persistence %q", forbidden)
		}
	}
	returnsStart := strings.Index(sql, "RETURNS TABLE(invitation_id uuid")
	returnsEnd := strings.Index(sql[returnsStart:], ")\nLANGUAGE plpgsql")
	if returnsStart < 0 || returnsEnd < 0 {
		t.Fatal("claim return contract missing")
	}
	returns := sql[returnsStart : returnsStart+returnsEnd]
	for _, column := range []string{"invitation_id", "tenant_id", "merchant_id", "email", "expires_at", "token_hash", "token_key_id", "lease_token", "attempt_count"} {
		if !strings.Contains(returns, column) {
			t.Errorf("claim omits %s", column)
		}
	}
}
func TestClosedPermissionMatrix(t *testing.T) {
	raw, err := os.ReadFile("../../migrations/000009_merchant_team_settings.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(raw)
	expected := []string{"('owner','team:security_approve')", "('security_admin','team:security_approve')", "('admin','settings:write')", "('developer','settings:write')", "('support','settings:read')", "('viewer','team:read')"}
	for _, binding := range expected {
		if !strings.Contains(sql, binding) {
			t.Errorf("permission matrix missing %s", binding)
		}
	}
	for _, forbidden := range []string{"('viewer','settings:write')", "('developer','team:manage')", "('support','team:invite')"} {
		if strings.Contains(sql, forbidden) {
			t.Errorf("permission matrix overgrant %s", forbidden)
		}
	}
}
func TestDownMigrationDependencyOrder(t *testing.T) {
	raw, err := os.ReadFile("../../migrations/000009_merchant_team_settings.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(raw)
	complete := strings.Index(sql, "DROP FUNCTION IF EXISTS complete_merchant_invitation_delivery")
	audit := strings.Index(sql, "DROP FUNCTION IF EXISTS append_merchant_settings_audit")
	jobs := strings.Index(sql, "DROP TABLE IF EXISTS merchant_invitation_delivery_jobs")
	invites := strings.Index(sql, "DROP TABLE IF EXISTS merchant_member_invitations")
	if complete < 0 || audit < 0 || complete > audit || jobs < 0 || invites < 0 || jobs > invites {
		t.Fatal("unsafe down migration dependency order")
	}
}
func TestOpenAPIExactRuntimeSurface(t *testing.T) {
	raw, err := os.ReadFile("../../../contracts/merchant-settings-openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	contract := string(raw)
	for _, fragment := range []string{"type: apiKey", "name: Authorization", "MerchantSettingsAdmin <payload.signature>", "readOnly: true", "/v1/merchant-cabinet/invitations/accept:", "security_approval_required", "additionalProperties: false", "enum: [en, zh-CN, es, fr, de, ru]"} {
		if !strings.Contains(contract, fragment) {
			t.Errorf("contract missing %q", fragment)
		}
	}
	if strings.Contains(contract, "writeOnly: true") {
		t.Fatal("first-response token incorrectly declared write-only")
	}
}
func TestPostgresAttributionAndNullSupportContract(t *testing.T) {
	raw, err := os.ReadFile("postgres.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	for _, fragment := range []string{"action.RequestedBy, p.UserID", "role, invitedBy, now", "nullif($6,'')", "nullif($4,'')", "requestedSession == p.SessionID", "bytes.Equal(storedHash, expectedHash[:])"} {
		if !strings.Contains(source, fragment) {
			t.Errorf("repository invariant missing %q", fragment)
		}
	}
}
