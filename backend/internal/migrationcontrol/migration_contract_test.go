package migrationcontrol

import (
	"os"
	"strings"
	"testing"
)

func migrationSQL(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../../migrations/000021_shadow_migration_control.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestMigrationSQLRuntimeOwnershipFences(t *testing.T) {
	sql := migrationSQL(t)
	for _, required := range []string{
		"migration_platform_create_fence", "migration_platform_route_fence", "migration_payment_credit_fence",
		"migration_callback_delivery_fence", "migration_imported_address_never_release",
		"BEFORE UPDATE OR DELETE ON addresses", "unknown_event_identity", "migration_review_open",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("missing runtime fence %q", required)
		}
	}
	promotion := "UPDATE public.migration_event_ownership SET owner='platform'"
	if strings.Count(sql, promotion) != 1 || !strings.Contains(sql, "WHERE migration_id=run.id AND event_identity=evidence.event_identity") {
		t.Fatal("only one independently verified event may be promoted to platform ownership")
	}
}

func TestManifestDigestBindsExactCanonicalBytes(t *testing.T) {
	sql := migrationSQL(t)
	for _, required := range []string{"canonical_body bytea", "payload_hash=digest(canonical_body,'sha256')", "requested_digest<>digest(requested_body,'sha256')", "convert_from(canonical_body,'UTF8')::jsonb=canonical_payload"} {
		if !strings.Contains(sql, required) {
			t.Errorf("missing canonical digest contract %q", required)
		}
	}
	if strings.Contains(sql, "requested_digest<>digest(requested_payload::text") {
		t.Fatal("jsonb text is still used as signed-byte digest")
	}
}

func TestImportedRouteRequiresExplicitCurrentPolicyEvidence(t *testing.T) {
	sql := migrationSQL(t)
	for _, required := range []string{"requested_required_finality<=0", "h.snapshot_id=requested_finality_snapshot", "h.fence_token=requested_finality_fence", "requested_matching_hash", "payment_route_policy_bindings", "implicit historical matching policy differs from signed import evidence"} {
		if !strings.Contains(sql, required) {
			t.Errorf("missing imported route policy check %q", required)
		}
	}
	if strings.Contains(sql, "requested_address,0,route_state") {
		t.Fatal("imported route still has zero finality")
	}
}

func TestCanaryCannotBeEnabledByWorker(t *testing.T) {
	sql := migrationSQL(t)
	grantStart := strings.Index(sql, "IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='migration_control_worker')")
	if grantStart < 0 {
		t.Fatal("worker grant block missing")
	}
	grantBlock := sql[grantStart:]
	if strings.Contains(grantBlock, "record_migration_canary_version") {
		t.Fatal("worker can directly mutate canary ownership")
	}
	for _, required := range []string{"kind='canary'", "migration.transition_executed", "r.asset_id=ANY(requested_assets)", "migration canary asset ownership denies platform route"} {
		if !strings.Contains(sql, required) {
			t.Errorf("missing approved canary boundary %q", required)
		}
	}
}

func TestWorkerReplayAndLeaseContracts(t *testing.T) {
	sql := migrationSQL(t)
	for _, required := range []string{"migration_worker_lease_valid", "mutated import replay", "mutated shadow replay", "mutated verification replay", "mutated watch address replay", "mutated order replay", "lease_until>clock_timestamp()"} {
		if !strings.Contains(sql, required) {
			t.Errorf("missing worker/replay contract %q", required)
		}
	}
	for _, signature := range []string{
		"migration_apply_watch_address(requested_migration uuid,requested_worker text,requested_token uuid,requested_fence bigint",
		"migration_apply_order(requested_migration uuid,requested_worker text,requested_token uuid,requested_fence bigint",
	} {
		if !strings.Contains(sql, signature) {
			t.Errorf("unfenced worker signature %q", signature)
		}
	}
}

func TestLostResponseReplayPrecedesMutableGates(t *testing.T) {
	sql := migrationSQL(t)
	cases := []struct{ function, replay, mutable string }{
		{"attach_migration_manifest", "operation='attach_manifest'", "run.row_version<>expected_row_version"},
		{"request_migration_transition", "operation='request_transition'", "UPDATE public.migration_transition_requests SET status='expired'"},
		{"decide_migration_transition", "operation='decide_transition'", "request.status<>'pending_approval'"},
		{"execute_migration_transition", "operation='execute_transition'", "request.status<>'approved'"},
	}
	for _, item := range cases {
		start := strings.Index(sql, "CREATE FUNCTION "+item.function)
		if start < 0 {
			t.Fatalf("missing %s", item.function)
		}
		block := sql[start:]
		replayAt, mutableAt := strings.Index(block, item.replay), strings.Index(block, item.mutable)
		if replayAt < 0 || mutableAt < 0 || replayAt > mutableAt {
			t.Errorf("%s checks mutable state before exact replay", item.function)
		}
	}
	for _, required := range []string{"response_body jsonb NOT NULL", "jsonb_populate_record", "prior.resource_id<>requested_manifest", "prior.resource_id<>request.id", "prior.resource_id<>run.id"} {
		if !strings.Contains(sql, required) {
			t.Errorf("idempotency resource/snapshot evidence missing %q", required)
		}
	}
}

func TestCanonicalEventOwnershipPreventsDoubleCredit(t *testing.T) {
	sql := migrationSQL(t)
	for _, required := range []string{
		"opening_ledger_transaction_id uuid", "canonical_identity:=(fact->>'chain_id')", "verified event already credited",
		"ownership.opening_ledger_transaction_id IS NOT NULL", "admitted_route_id<>NEW.route_id", "migration_callback_ownership_after_create", "migration_provider_observation",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("event ownership invariant missing %q", required)
		}
	}
}

func TestCanaryTransferNeedsOneExactCohortRoute(t *testing.T) {
	sql := migrationSQL(t)
	for _, required := range []string{
		"candidate_count=1", "r.asset_id=ANY(canary.asset_ids)", "r.expected_amount_atomic=NEW.amount_atomic",
		"r.receiving_address=NEW.to_address", "NEW.on_chain_time>=r.starts_at", "NEW.on_chain_time<r.grace_ends_at",
		"IF candidate_count>1 THEN EXIT", "uncredited canary fact returned to shadow on rollback",
		"WHEN EXISTS(SELECT 1 FROM public.migration_imported_orders io", "ELSE 'platform'",
		"o.intent_id=route.intent_id AND o.owner='platform'", "selected_owner:='platform'",
		"o.admitted_route_id IS NULL OR EXISTS(SELECT 1 FROM public.migration_imported_orders io",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("canary transfer admission missing %q", required)
		}
	}
}

func TestOnChainRollbackPreservesOneExactPlatformRoute(t *testing.T) {
	sql := migrationSQL(t)
	start := strings.Index(sql, "CREATE FUNCTION migration_observe_transfer_identity")
	end := strings.Index(sql[start:], "CREATE TRIGGER migration_transfer_observation")
	if start < 0 || end < 0 {
		t.Fatal("transfer ownership function missing")
	}
	block := sql[start : start+end]
	for _, required := range []string{
		"run.create_traffic_owner<>'platform'", "JOIN public.migration_callback_ownership o",
		"o.owner='platform'", "r.provider='on_chain'", "r.expected_amount_atomic=NEW.amount_atomic",
		"NEW.on_chain_time>=r.starts_at", "NEW.on_chain_time<r.grace_ends_at",
		"IF candidate_count=1 THEN owner:='platform'; admitted_route:=candidate.id",
	} {
		if !strings.Contains(block, required) {
			t.Errorf("on-chain rollback ownership missing %q", required)
		}
	}
}

func TestVerifiedOpeningDerivesExactAccounts(t *testing.T) {
	sql := migrationSQL(t)
	for _, required := range []string{"intent.status<>'needs_review'", "account_code='treasury_asset'", "account_code='merchant_settlement_liability'", "merchant_id=intent.merchant_id", "VALUES(requested_ledger,run.tenant_id,'migration_opening'"} {
		if !strings.Contains(sql, required) {
			t.Errorf("missing opening ledger invariant %q", required)
		}
	}
	if strings.Contains(sql, "requested_debit") || strings.Contains(sql, "requested_credit") {
		t.Fatal("worker still selects arbitrary ledger accounts")
	}
}

func TestDecommissionUsesLiveDatabaseBlockers(t *testing.T) {
	sql := migrationSQL(t)
	for _, required := range []string{"imported_orders_open", "imported_reservations_active", "callback_ownership_not_transferred", "callback_delivery_backlog", "source_callback_backlog", "unmatched_backlog", "actuator_action_pending", "migration_worker_active", "migration_decommission_evidence"} {
		if !strings.Contains(sql, required) {
			t.Errorf("missing decommission blocker %q", required)
		}
	}
}

func TestRollbackDropsVerificationTriggerFunction(t *testing.T) {
	raw, err := os.ReadFile("../../migrations/000021_shadow_migration_control.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "DROP FUNCTION IF EXISTS migration_verification_frozen()") {
		t.Fatal("migration rollback leaves the verification immutability trigger function behind")
	}
}

func TestManifestAndCanaryContractsFailClosed(t *testing.T) {
	sql := migrationSQL(t)
	for _, required := range []string{
		"cardinality(requested_merchants)<1", "JOIN public.migration_callback_ownership o", "source_reference=evidence.event_identity",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("missing closed manifest/canary contract %q", required)
		}
	}
}
