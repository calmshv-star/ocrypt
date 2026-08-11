package postgres

import (
	"os"
	"strings"
	"testing"
)

func TestPlatformRouteAdmissionFailsClosedAndWritesEvidenceAtomically(t *testing.T) {
	migration, err := os.ReadFile("../../../migrations/000010_platform_runtime_admission.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	planning, err := os.ReadFile("atomic_planning.go")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(migration)
	for _, required := range []string{
		"h.kind='feature_flag' AND h.logical_key='new_routes'",
		"s.payload->>'key'='new_routes'",
		"(s.payload->>'rollout_bps')::integer=10000",
		"NOT public.platform_latest_pause(requested_tenant,'feature_flag','new_routes')",
		"s.payload->>'effect' IN ('read_only','disable_new_routes')",
		"NOT public.platform_latest_pause(NULL,'finality_policy',requested_chain)",
		"s.payload->>'chain_ref'=requested_chain",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("route admission missing fail-closed clause %q", required)
		}
	}
	if strings.Contains(sql, "COALESCE((SELECT (s.payload->>'enabled')::boolean") {
		t.Fatal("missing new_routes snapshot still defaults to enabled")
	}
	code := string(planning)
	for _, required := range []string{
		"platform_route_runtime_admission",
		"platform_wallet_runtime_admission",
		"platform_environment_snapshot_id,platform_environment_fence,platform_chain_snapshot_id,platform_chain_fence,platform_asset_snapshot_id,platform_asset_fence,platform_finality_snapshot_id,platform_finality_fence,runtime_required_finality",
		"platform_wallet_pool_snapshot_id,platform_wallet_pool_fence",
	} {
		if !strings.Contains(code, required) {
			t.Fatalf("atomic planner does not persist complete runtime evidence: %q", required)
		}
	}
}
