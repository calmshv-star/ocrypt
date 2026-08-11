package rates

import (
	"os"
	"strings"
	"testing"
)

func TestRateMigrationSecurityAndCorrectnessInvariants(t *testing.T) {
	data, err := os.ReadFile("../../migrations/000007_rate_runtime.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(data)
	required := []string{
		"rate_runtime_identities", "ENABLE ROW LEVEL SECURITY", "FORCE ROW LEVEL SECURITY", "current_setting(''app.rate_worker_id'',true)",
		"rate_source_observations", "admitted_rate_ticks", "admitted_rate_tick_observations", "rate_collection_dead_letters",
		"rate_observations_immutable", "rate_ticks_immutable", "sources_digest bytea", "raw_response_hash bytea",
		"rate_policy_snapshot_id,scope_id,policy_config_kind,policy_key", "rate_source_snapshot_id,scope_id,source_config_kind,source_key",
		"lease_owner uuid", "claim_token bigint", "UNIQUE(scope_id,policy_key,claim_token)", "REVOKE UPDATE,DELETE,TRUNCATE",
		"IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='rate_runtime_worker')", "price_numerator numeric(78,0)",
		"asset_rate_ticks_one_active_pair_idx", "GRANT SELECT,INSERT,UPDATE ON rate_runtime_jobs", "GRANT UPDATE(status) ON asset_rate_ticks",
	}
	for _, fragment := range required {
		if !strings.Contains(sql, fragment) {
			t.Errorf("missing invariant %q", fragment)
		}
	}
	for _, forbidden := range []string{"double precision", "real ", "api_key text", "secret text", "on delete cascade"} {
		if strings.Contains(strings.ToLower(sql), forbidden) {
			t.Errorf("unsafe fragment %q", forbidden)
		}
	}
}

func TestRateDownMigrationDropsDependentsBeforeIdentity(t *testing.T) {
	data, err := os.ReadFile("../../migrations/000007_rate_runtime.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(data)
	if strings.Contains(sql, "DROP TABLE IF EXISTS asset_rate_ticks") {
		t.Fatal("down migration would drop core planner table")
	}
	if strings.Index(sql, "DROP TABLE IF EXISTS rate_collection_dead_letters") > strings.Index(sql, "DROP TABLE IF EXISTS rate_runtime_jobs") {
		t.Fatal("dead letter FK dropped after job")
	}
	if strings.Index(sql, "DROP TABLE IF EXISTS rate_runtime_jobs") > strings.Index(sql, "DROP TABLE IF EXISTS rate_runtime_identities") {
		t.Fatal("job FK dropped after identity")
	}
}
