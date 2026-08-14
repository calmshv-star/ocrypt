package providerops

import (
	"os"
	"strings"
	"testing"
)

func TestProviderOperationsMigrationKeepsMutationBehindNarrowFunctions(t *testing.T) {
	data, err := os.ReadFile("../../migrations/000017_provider_operations.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(data)
	for _, required := range []string{
		"FORCE ROW LEVEL SECURITY", "request_provider_operation_change", "decide_provider_operation_change",
		"claim_provider_health_probes", "complete_provider_health_probe", "provider_operation_binding_policy_current",
		"provider_operation_apply_rpc_policy", "provider_health_worker_status", "policy_snapshot_version", "policy_fence_token", "max_lag_blocks",
		"chosen_group", "pg_try_advisory_xact_lock", "count(DISTINCT", "admissible_peer_groups",
		"provider_hosted_policy_versions", "provider_hosted_policy_evidence_immutable", "load_hosted_provider_health_probe",
		"requested_payload::text||E'\\n'||requested_probe_reference", "version.bootstrap_probe_reference",
		"CREATE OR REPLACE FUNCTION claim_hosted_create_recoveries", "CREATE OR REPLACE FUNCTION claim_hosted_order_recoveries",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("migration lacks %s", required)
		}
	}
	for _, forbidden := range []string{
		"GRANT UPDATE ON hosted_provider_configs", "GRANT SELECT,UPDATE ON provider_circuit_states",
		"GRANT SELECT,INSERT,DELETE ON provider_health_observations",
	} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("migration grants bypass capability %q", forbidden)
		}
	}
	if strings.Count(sql, "AFTER INSERT OR UPDATE OF status ON hosted_provider_configs") != 1 {
		t.Fatal("hosted provider sync trigger event is duplicated or missing")
	}
	if !strings.Contains(sql, "GRANT EXECUTE ON FUNCTION provider_health_worker_status(timestamptz) TO merchant_provider_health_worker") {
		t.Fatal("health readiness aggregate is not granted to the dedicated role")
	}
	exactHostedRequest := "request_hosted_provider_policy(uuid,uuid,uuid,bigint,jsonb,text,text,uuid,text,timestamptz,timestamptz)"
	if strings.Count(sql, exactHostedRequest) != 2 {
		t.Fatalf("hosted policy request signature is not exact in revoke and grant: count=%d", strings.Count(sql, exactHostedRequest))
	}
	if strings.Contains(sql, "provider_orders po") {
		t.Fatal("hosted health probe may substitute an unaudited order reference for approved bootstrap evidence")
	}
	if strings.Count(sql, "admit_hosted_provider_operation(a.tenant_id,a.merchant_id,a.provider_id") != 1 || strings.Count(sql, "admit_hosted_provider_operation(o.tenant_id,o.merchant_id,o.provider_id,'reconciliation'") != 1 {
		t.Fatal("hosted recovery claims are not fenced by the exact provider operation capability")
	}
	for _, forbiddenEvidence := range []string{
		"jsonb_build_object('bootstrap_probe_reference'", "'bootstrap_probe_reference',requested_probe_reference",
	} {
		if strings.Contains(sql, forbiddenEvidence) {
			t.Fatalf("raw bootstrap reference escapes into audit/outbox evidence: %q", forbiddenEvidence)
		}
	}
}

func TestProviderOperationsRollbackDropsEveryCapability(t *testing.T) {
	data, err := os.ReadFile("../../migrations/000017_provider_operations.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(data)
	for _, required := range []string{"provider_health_worker_status", "complete_provider_health_probe", "load_hosted_provider_health_probe", "claim_provider_health_probes", "decide_hosted_provider_policy", "request_hosted_provider_policy", "decide_provider_operation_change", "request_provider_operation_change", "provider_operation_apply_rpc_policy", "provider_operation_policy_payload_valid"} {
		if !strings.Contains(sql, "DROP FUNCTION IF EXISTS "+required) {
			t.Fatalf("rollback leaves %s", required)
		}
	}
	if !strings.Contains(sql, "CREATE OR REPLACE FUNCTION claim_hosted_create_recoveries") || !strings.Contains(sql, "CREATE OR REPLACE FUNCTION claim_hosted_order_recoveries") {
		t.Fatal("rollback does not restore the independent 000016 recovery functions before dropping provider admission")
	}
}

func TestProviderHealthUUIDOrderingMigrationIsExactAndReversible(t *testing.T) {
	up, err := os.ReadFile("../../migrations/000046_provider_health_uuid_order.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := os.ReadFile("../../migrations/000046_provider_health_uuid_order.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	for name, sql := range map[string]string{"up": string(up), "down": string(down)} {
		for _, required := range []string{
			"claim_provider_health_probes(text,integer,timestamptz)",
			"ORDER BY min(e.binding_id) LIMIT 1",
			"ORDER BY min(e.binding_id::text) LIMIT 1",
			"pg_get_functiondef",
			"EXECUTE patched",
		} {
			if !strings.Contains(sql, required) {
				t.Fatalf("%s migration lacks %q", name, required)
			}
		}
	}
}

func TestProviderHealthConfiguredEvidenceJoinMigrationIsExactAndReversible(t *testing.T) {
	up, err := os.ReadFile("../../migrations/000047_provider_health_explicit_config_join.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := os.ReadFile("../../migrations/000047_provider_health_explicit_config_join.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	for name, sql := range map[string]string{"up": string(up), "down": string(down)} {
		for _, required := range []string{
			"claim_provider_health_probes(text,integer,timestamptz)",
			"JOIN configured USING(binding_id,operation);",
			"JOIN configured ON configured.binding_id=c.binding_id AND configured.operation=c.operation;",
			"pg_get_functiondef",
			"EXECUTE patched",
		} {
			if !strings.Contains(sql, required) {
				t.Fatalf("%s migration lacks %q", name, required)
			}
		}
	}
}
