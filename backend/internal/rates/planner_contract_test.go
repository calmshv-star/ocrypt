package rates

import (
	"os"
	"strings"
	"testing"
)

// This contract connects the rate admission transaction to the production
// PersistedPlanner without requiring a second, ambiguous read path.
func TestAdmittedTickIsAtomicPersistedPlannerProjection(t *testing.T) {
	runtimeSource, err := os.ReadFile("postgres.go")
	if err != nil {
		t.Fatal(err)
	}
	plannerSource, err := os.ReadFile("../adapters/postgres/atomic_planning.go")
	if err != nil {
		t.Fatal(err)
	}
	runtimeSQL, plannerSQL := string(runtimeSource), string(plannerSource)
	supersede := strings.Index(runtimeSQL, "UPDATE asset_rate_ticks SET status='superseded'")
	project := strings.Index(runtimeSQL, "INSERT INTO asset_rate_ticks(id,asset_id,fiat_currency")
	admit := strings.Index(runtimeSQL, "INSERT INTO admitted_rate_ticks")
	if supersede < 0 || project < 0 || admit < 0 || !(supersede < project && project < admit) {
		t.Fatal("planner supersede, projection, and immutable admission are not one ordered transaction")
	}
	for _, fragment := range []string{"pg_advisory_xact_lock", "asset_rate_ticks_one_active_pair_idx", "WHERE $9::timestamptz+$10::bigint*interval '1 second'>clock_timestamp()", "provenance_hash", "rate_runtime_pair_bindings"} {
		if !strings.Contains(runtimeSQL+readMigration(t), fragment) {
			t.Errorf("missing projection invariant %q", fragment)
		}
	}
	for _, fragment := range []string{"FROM asset_rate_ticks rt", "rt.status='active'", "rt.observed_at+make_interval(secs=>rt.max_age_seconds)>clock_timestamp()", "ORDER BY rt.observed_at DESC,rt.id DESC LIMIT 1"} {
		if !strings.Contains(plannerSQL, fragment) {
			t.Errorf("planner no longer enforces %q", fragment)
		}
	}
}

func readMigration(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("../../migrations/000007_rate_runtime.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestForwardMigrationNeverRecreatesCorePlannerTable(t *testing.T) {
	sql := readMigration(t)
	if strings.Contains(sql, "CREATE TABLE asset_rate_ticks (") {
		t.Fatal("forward migration conflicts with migration 000001")
	}
}
