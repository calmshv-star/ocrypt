package retentionadmin

import (
	"os"
	"strings"
	"testing"
)

func retentionMigration(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile("../../migrations/000022_retention_control_plane.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func retentionDownMigration(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile("../../migrations/000022_retention_control_plane.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestDownMigrationDropsTriggerTablesBeforeTriggerFunction(t *testing.T) {
	sql := retentionDownMigration(t)
	functionDrop := strings.Index(sql, "DROP FUNCTION IF EXISTS retention_control_immutable()")
	if functionDrop < 0 {
		t.Fatal("retention immutable trigger function drop is missing")
	}
	for _, table := range []string{"retention_control_idempotency", "retention_policy_heads"} {
		tableDrop := strings.Index(sql, "DROP TABLE IF EXISTS "+table)
		if tableDrop < 0 || tableDrop > functionDrop {
			t.Fatalf("%s must be dropped before retention_control_immutable()", table)
		}
	}
}

func TestMigrationKeepsArchiveOnlyClassesAndSecondCycleGrace(t *testing.T) {
	sql := retentionMigration(t)
	markers := []string{
		"data_class='published_outbox_payload' OR NOT prune_enabled",
		"requested_class IN ('callback_event_body','event_history_payload') AND requested_prune",
		"retention_advance_prune",
		"h.released_at IS NULL AND h.expired_at IS NULL",
		"max(o.retention_until)",
		"active_holds",
	}
	for _, marker := range markers {
		if !strings.Contains(sql, marker) {
			t.Errorf("missing retention invariant %q", marker)
		}
	}
}

func TestMigrationHasFourEyesMFAFencesAndExplicitExpiry(t *testing.T) {
	sql := retentionMigration(t)
	markers := []string{
		"approved_by<>requested_by",
		"held.actor_id=requested_actor::text",
		"retention_control_mfa_valid",
		"expected_head_fence",
		"stale_policy_head",
		"scheduled_for<=now_at",
		"expires_at<=now_at",
		"retention_hold.expired",
		"created_session_id",
		"approval_window_expired",
		"expires_at>created_at AND expires_at<=created_at+interval '30 minutes'",
	}
	for _, marker := range markers {
		if !strings.Contains(sql, marker) {
			t.Errorf("missing approval/expiry invariant %q", marker)
		}
	}
}

func TestSecurityDefinersBindDatabaseIdentityTenantAndPermission(t *testing.T) {
	sql := retentionMigration(t)
	for _, marker := range []string{
		"current_setting('app.tenant_id',true)=requested_tenant::text",
		"current_setting('app.retention_actor_id',true)=requested_actor::text",
		"current_setting('app.retention_session_id',true)=requested_session",
		"rp.permission_key=requested_permission",
		"b.merchant_id IS NULL",
		"'retention:policy_request'",
		"'retention:policy_approve'",
		"'retention:hold_create'",
		"'retention:hold_release'",
		"'retention:read'",
	} {
		if !strings.Contains(sql, marker) {
			t.Errorf("missing database authorization fence %q", marker)
		}
	}
}

func TestBrowserRoleCannotRunScheduler(t *testing.T) {
	sql := retentionMigration(t)
	platformGrant := sql[strings.Index(sql, "IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='platform_admin_runtime')"):strings.Index(sql, "IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='retention_control_scheduler')")]
	if strings.Contains(platformGrant, "GRANT EXECUTE ON FUNCTION retention_control_advance_due") || !strings.Contains(platformGrant, "REVOKE EXECUTE ON FUNCTION retention_control_advance_due") {
		t.Fatal("browser/API capability can execute retention scheduler")
	}
	if !strings.Contains(sql, "GRANT EXECUTE ON FUNCTION retention_control_advance_due(text,integer),retention_control_worker_health(integer) TO retention_control_scheduler") {
		t.Fatal("dedicated scheduler does not have exact transition capability")
	}
}

func TestMigrationHoldCaseReferenceProjectionRequiresHoldPermission(t *testing.T) {
	sql := retentionMigration(t)
	for _, marker := range []string{"'retention:hold_create'", "'retention:hold_release'", "THEN h.case_reference ELSE NULL END"} {
		if !strings.Contains(sql, marker) {
			t.Errorf("hold case-reference projection lost permission fence %q", marker)
		}
	}
}

func TestConvergentRuntimeGrantsKeepSchedulerFunctionOnly(t *testing.T) {
	body, err := os.ReadFile("../../../deploy/postgres/runtime-grants.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	for _, marker := range []string{
		"retention_control_scheduler",
		"REVOKE ALL PRIVILEGES ON\n  retention_policy_heads",
		"REVOKE EXECUTE ON FUNCTION retention_control_advance_due(text,integer)\nFROM platform_admin_runtime",
		"GRANT EXECUTE ON FUNCTION retention_control_advance_due(text,integer),retention_control_worker_health(integer)\nTO retention_control_scheduler",
	} {
		if !strings.Contains(sql, marker) {
			t.Errorf("retention role convergence lost %q", marker)
		}
	}
}

func TestMigrationRevokesDirectDMLAndRedactsArchiveLocations(t *testing.T) {
	sql := retentionMigration(t)
	if !strings.Contains(sql, "REVOKE ALL PRIVILEGES ON retention_policy_heads") || !strings.Contains(sql, "REVOKE ALL PRIVILEGES ON retention_policy_versions") {
		t.Fatal("platform runtime direct retention DML is not revoked")
	}
	batchStart := strings.Index(sql, "CREATE FUNCTION retention_control_batches")
	batchEnd := strings.Index(sql[batchStart:], "CREATE FUNCTION retention_control_tombstones")
	if batchStart < 0 || batchEnd < 0 {
		t.Fatal("redacted batch function missing")
	}
	batchFunction := sql[batchStart : batchStart+batchEnd]
	for _, forbidden := range []string{"object_key", "object_version", "canonical_body", "canonical_payload", "payload jsonb"} {
		if strings.Contains(batchFunction, forbidden) {
			t.Errorf("browser batch projection leaks %q", forbidden)
		}
	}
}

func TestMigrationUsesForceRLSAndTenantConfinedRecordChecks(t *testing.T) {
	sql := retentionMigration(t)
	if strings.Count(sql, "FORCE ROW LEVEL SECURITY") < 4 {
		t.Fatal("all retention control tables must force RLS")
	}
	for _, source := range []string{"callback_events", "outbox_events", "event_history"} {
		if !strings.Contains(sql, "public."+source) {
			t.Errorf("record-scope existence check missing %s", source)
		}
	}
	if strings.Count(sql, "tenant_id=requested_tenant AND merchant_id=requested_merchant") < 3 {
		t.Fatal("record checks are not tenant+merchant confined")
	}
}
