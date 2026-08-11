package providerconfig

import (
	"os"
	"strings"
	"testing"
)

func TestProviderConfigurationMigrationIsClosedAndFenced(t *testing.T) {
	body, err := os.ReadFile("../../migrations/000020_hosted_provider_configuration.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	required := []string{
		"CREATE TABLE hosted_provider_config_manifests", "hosted_provider_config_manifest_reject_mutation",
		"FORCE ROW LEVEL SECURITY", "request_hosted_provider_config(uuid,uuid,uuid,text,bigint,jsonb,text,uuid,text,timestamptz,timestamptz)",
		"decide_hosted_provider_config(uuid,uuid,bigint,boolean,text,uuid,text,timestamptz,timestamptz)",
		"clock_timestamp()", "provider_config_public_rows", "app.platform_admin_tenants",
		"COALESCE(current_setting('app.platform_admin_global',true),'')",
		"FOREIGN KEY(active_manifest_id,provider_id,scope_id,tenant_id,merchant_id,active_manifest_version)",
		"FOREIGN KEY(config_manifest_id,provider_id,tenant_id,merchant_id)",
		"config_manifest_id", "provider_operation_binding_policy_current", "next_probe_at", "probe_failed",
		"legacy_unadmitted", "config_workflow.status='active'", "SET status=CASE WHEN status='disabled' THEN 'disabled' ELSE 'paused' END",
		"probe_response_digest", "probe_tls_spki_digest", "hosted_provider_callback_config_admitted(text,text)",
		"hosted_provider_outbound_config_admitted(uuid,uuid,text,text)",
		"UPDATE public.provider_operation_bindings SET status='disabled'",
		"provider configuration evidence changed", "manifest.payload_hash<>digest",
		"REVOKE ALL ON hosted_provider_config_manifests", "GRANT EXECUTE ON FUNCTION claim_hosted_provider_config_probes",
	}
	for _, item := range required {
		if !strings.Contains(text, item) {
			t.Errorf("migration missing %q", item)
		}
	}
	for _, forbidden := range []string{"GRANT UPDATE ON hosted_provider_configs", "GRANT SELECT ON hosted_provider_config_manifests TO platform_admin_runtime", "BYPASSRLS", "migration-response:", "migration-spki:", "'migration-bootstrap'"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("migration contains unsafe grant %q", forbidden)
		}
	}
}

func TestHTTPControlPlaneBindsEveryReadAndMutationToAssertionTenant(t *testing.T) {
	body, err := os.ReadFile("../platformadmin/provider_config_server.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if strings.Count(text, "ScopeTenantID") < 6 || strings.Count(text, "scope.TenantID != principal.ScopeTenantID") != 2 || strings.Count(text, `r.URL.Query().Get("tenant_id") != principal.ScopeTenantID`) != 2 {
		t.Fatal("provider configuration handlers do not bind reads and mutations to the asserted tenant")
	}
}

func TestHostedRecoveryReadinessRequiresCurrentConfigurationAdmission(t *testing.T) {
	body, err := os.ReadFile("../adapters/postgres/hosted_recovery.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if strings.Count(text, "hosted_provider_outbound_config_admitted(uuid,uuid,text,text)") < 2 {
		t.Fatal("hosted recovery can report ready without the current configuration admission capability")
	}
}

func TestControlReadinessChecksIdempotencyCapability(t *testing.T) {
	body, err := os.ReadFile("postgres.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, item := range []string{"hosted_provider_config_idempotency", "has_table_privilege", "SELECT,INSERT"} {
		if !strings.Contains(text, item) {
			t.Errorf("control readiness missing %q", item)
		}
	}
}

func TestRollbackRestoresCallbackAndPolicyContracts(t *testing.T) {
	body, err := os.ReadFile("../../migrations/000020_hosted_provider_configuration.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, item := range []string{"GRANT EXECUTE ON FUNCTION hosted_provider_callback_admitted(text)", "CREATE OR REPLACE FUNCTION provider_operation_binding_policy_current", "DROP COLUMN IF EXISTS config_manifest_id", "CREATE FUNCTION claim_hosted_prebind_recoveries"} {
		if !strings.Contains(text, item) {
			t.Errorf("rollback missing %q", item)
		}
	}
}

func TestRuntimeGrantsKeepConfigurationMutationBehindDefiners(t *testing.T) {
	body, err := os.ReadFile("../../../deploy/postgres/runtime-grants.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, item := range []string{
		"hosted_provider_callback_config_admitted(text,text)",
		"hosted_provider_outbound_config_admitted(uuid,uuid,text,text)",
		"claim_hosted_provider_config_probes(text,integer,timestamptz)",
		"complete_hosted_provider_config_probe(uuid,text,bigint,boolean,text,bytea,bytea,timestamptz)",
		"request_hosted_provider_config(uuid,uuid,uuid,text,bigint,jsonb,text,uuid,text,timestamptz,timestamptz)",
		"REVOKE ALL PRIVILEGES ON hosted_provider_config_manifests",
	} {
		if !strings.Contains(text, item) {
			t.Errorf("runtime grants missing %q", item)
		}
	}
	if strings.Contains(text, "GRANT UPDATE ON hosted_provider_config_manifests") || strings.Contains(text, "GRANT SELECT ON hosted_provider_config_manifests TO platform_admin_runtime") {
		t.Fatal("runtime grants expose private provider configuration storage")
	}
}
