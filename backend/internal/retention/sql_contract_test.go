package retention

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func retentionMigration(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("../../migrations/000015_retention_archive.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestRetentionMigrationFailClosedContracts(t *testing.T) {
	source := retentionMigration(t)
	required := []string{
		"ALTER TABLE retention_archive_index FORCE ROW LEVEL SECURITY",
		"retention_objects_immutable", "retention_index_immutable", "retention_policy_immutable",
		"SECURITY DEFINER", "SET search_path=pg_catalog,public SET row_security=off",
		"object_lock_mode='COMPLIANCE'", "object_version<>", "retention_until>p_now",
		"p_object_key<>'retention/v1/'||batch.tenant_id::text||'/'||batch.data_class||'/'||batch.id::text||'.json'",
		"pg_advisory_xact_lock", "first_prune_checked_at", "prune_not_before",
		"status<>'acknowledged'", "published_at IS NOT NULL", "JOIN public.event_history",
		"digest(convert_to(e.payload::text,'UTF8'),'sha256')=i.original_digest",
		"data_class NOT IN ('callback_event_body','event_history_payload') OR NOT prune_enabled",
		"set_config('app.retention_hold_write',coalesce(prior_write,''),true)",
		"REVOKE ALL ON FUNCTION create_retention_policy_version", "requested_version<>expected_version",
		"REVOKE ALL ON FUNCTION retention_claim_archive_batch", "REVOKE ALL ON FUNCTION retention_advance_prune",
	}
	for _, contract := range required {
		if !strings.Contains(source, contract) {
			t.Errorf("retention migration lost contract %q", contract)
		}
	}
	if strings.Contains(source, "GRANT DELETE") || strings.Contains(source, "GRANT UPDATE ON callback_events") || strings.Contains(source, "GRANT UPDATE ON outbox_events") {
		t.Fatal("retention worker received direct destructive source-table authority")
	}
}

func TestRetentionMigrationNeverMutatesFinancialOrAuditEvidence(t *testing.T) {
	source := retentionMigration(t)
	protected := []string{
		"ledger_transactions", "ledger_entries", "payment_matches", "transfer_events",
		"financial_audit", "admin_audit", "management_audit", "platform_admin_audit",
		"merchant_settings_audit", "reconciliation_reports", "retention_archive_index",
	}
	for _, table := range protected {
		pattern := regexp.MustCompile(`(?i)(DELETE\s+FROM|UPDATE)\s+(public\.)?` + regexp.QuoteMeta(table) + `\b`)
		if pattern.MatchString(source) {
			t.Errorf("retention migration contains a forbidden mutation of %s", table)
		}
	}
}

func TestCallbackArchiveCannotSilentlyBreakReplay(t *testing.T) {
	source := retentionMigration(t)
	if strings.Contains(source, "UPDATE public.callback_events") || strings.Contains(source, "canonical_body=''::bytea") {
		t.Fatal("callback body pruning was added without WORM hydration in the callback worker")
	}
	if !strings.Contains(source, "status=CASE WHEN data_class='published_outbox_payload' THEN 'verified' ELSE 'archive_only' END") {
		t.Fatal("callback archive is not finalized as archive-only")
	}
}

func TestRetentionDownMigrationDoesNotAttemptSourceDataRestoration(t *testing.T) {
	raw, err := os.ReadFile("../../migrations/000015_retention_archive.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	down := string(raw)
	if !strings.Contains(down, "cannot roll back retention archive while outbox tombstones exist") {
		t.Fatal("rollback does not fail closed when hot payloads have been tombstoned")
	}
	if regexp.MustCompile(`(?i)(UPDATE|DELETE\s+FROM)\s+(public\.)?(outbox_events|callback_events|event_history)`).MatchString(down) {
		t.Fatal("rollback attempted to synthesize or remove archived source data")
	}
}
