package platformruntime

import (
	"os"
	"strings"
	"testing"
)

func TestRuntimeAdmissionMigrationIsFailClosedAndReversible(t *testing.T) {
	up, err := os.ReadFile("../../migrations/000010_platform_runtime_admission.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := os.ReadFile("../../migrations/000010_platform_runtime_admission.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"platform_route_runtime_admission", "platform_wallet_runtime_admission", "platform_latest_pause", "scanner_runtime_config_evidence", "platform_environment_snapshot_id", "runtime_required_finality", "SECURITY DEFINER", "row_security=off", "REVOKE ALL"} {
		if !strings.Contains(string(up), required) {
			t.Fatalf("migration missing %q", required)
		}
	}
	for _, required := range []string{"DROP FUNCTION IF EXISTS platform_route_runtime_admission", "DROP TABLE IF EXISTS scanner_runtime_config_evidence", "DROP COLUMN IF EXISTS platform_environment_snapshot_id"} {
		if !strings.Contains(string(down), required) {
			t.Fatalf("rollback missing %q", required)
		}
	}
}
