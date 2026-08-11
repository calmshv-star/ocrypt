package rates

import (
	"os"
	"strings"
	"testing"
)

func TestRuntimeFenceUsesDefinerLockWithoutHeadMutationGrant(t *testing.T) {
	raw, err := os.ReadFile("../../migrations/000030_rate_fence_and_plan_reads.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(raw)
	for _, expected := range []string{"SECURITY DEFINER", "SET row_security=off", "FOR SHARE", "GRANT EXECUTE ON FUNCTION rate_runtime_snapshot_current", "GRANT SELECT ON payment_matches,payment_match_aggregates TO merchant_plan_worker"} {
		if !strings.Contains(sql, expected) {
			t.Fatalf("migration is missing %q", expected)
		}
	}
	if strings.Contains(sql, "GRANT UPDATE ON platform_config_heads TO rate_runtime_worker") {
		t.Fatal("rate runtime must not receive mutable access to platform heads")
	}
}

func TestRuntimeAssetLockUsesDefinerWithoutAssetMutationGrant(t *testing.T) {
	raw, err := os.ReadFile("../../migrations/000031_rate_asset_lock.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(raw)
	for _, expected := range []string{"rate_runtime_asset_active", "SECURITY DEFINER", "SET row_security=off", "FOR KEY SHARE", "GRANT EXECUTE ON FUNCTION rate_runtime_asset_active"} {
		if !strings.Contains(sql, expected) {
			t.Fatalf("migration is missing %q", expected)
		}
	}
	if strings.Contains(sql, "GRANT UPDATE ON assets TO rate_runtime_worker") {
		t.Fatal("rate runtime must not receive mutable access to assets")
	}
}
