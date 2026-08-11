package platformadmin

import (
	"os"
	"strings"
	"testing"
)

func TestPlatformAdminMigrationSecurityAndHistoryInvariants(t *testing.T) {
	data, err := os.ReadFile("../../migrations/000005_platform_admin.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(data)
	required := []string{
		"'platform_config_change_requests'",
		"EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY'",
		"ALTER TABLE %I FORCE ROW LEVEL SECURITY",
		"CHECK (payload_hash=digest(payload::text,'sha256'))",
		"FOREIGN KEY(snapshot_id,scope_id,kind,logical_key)",
		"FOREIGN KEY(rollback_of_snapshot_id,scope_id,kind,logical_key)",
		"platform_change_rollback_same_resource_fk",
		"CREATE TRIGGER platform_snapshots_immutable",
		"CREATE TRIGGER platform_activations_immutable",
		"CREATE TRIGGER platform_audit_immutable",
		"SECURITY DEFINER",
		"prior_global := current_setting('app.platform_admin_global',true)",
		"set_config('app.platform_admin_global',COALESCE(prior_global,''),true)",
		"REVOKE INSERT,UPDATE,DELETE,TRUNCATE ON platform_admin_audit FROM PUBLIC",
		"platform_exact_money_strings(payload)",
		"platform_payload_has_no_secrets(payload)",
		"('platform_config:approve'",
		"('senior_approver','platform_config:approve')",
	}
	for _, fragment := range required {
		if !strings.Contains(sql, fragment) {
			t.Errorf("missing migration invariant %q", fragment)
		}
	}
	for _, forbidden := range []string{"private_key text", "encrypted_private", "$3::interval", "platform-config:"} {
		if strings.Contains(sql, forbidden) {
			t.Errorf("unsafe migration fragment %q", forbidden)
		}
	}
	postgres, err := os.ReadFile("postgres.go")
	if err != nil {
		t.Fatal(err)
	}
	repositorySQL := string(postgres)
	for _, fragment := range []string{"$3 * interval '1 millisecond'", "claim_token=claim_token+1", "claim_token=$3 AND lease_until>=clock_timestamp()"} {
		if !strings.Contains(repositorySQL, fragment) {
			t.Errorf("missing repository lease invariant %q", fragment)
		}
	}
}

func TestDownMigrationRemovesPermissionBindingsBeforePermissions(t *testing.T) {
	data, err := os.ReadFile("../../migrations/000005_platform_admin.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(data)
	bindings := strings.Index(sql, "DELETE FROM admin_role_permissions")
	permissions := strings.Index(sql, "DELETE FROM admin_permissions")
	if bindings < 0 || permissions < 0 || bindings > permissions {
		t.Fatal("down migration permission dependency order is unsafe")
	}
}
