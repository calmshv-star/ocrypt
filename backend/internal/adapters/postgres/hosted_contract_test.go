package postgres

import (
	"os"
	"strings"
	"testing"
)

func hostedMigration(t *testing.T) string {
	t.Helper()
	contents, err := os.ReadFile("../../../migrations/000016_hosted_provider_routes.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}

func hostedPaymentLinkMigration(t *testing.T) string {
	t.Helper()
	contents, err := os.ReadFile("../../../migrations/000019_hosted_payment_link_jobs.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}

func TestHostedMigrationFreezesEvidenceAndScopesEveryMerchantTable(t *testing.T) {
	sql := hostedMigration(t)
	markers := []string{
		"UNIQUE (provider_id,provider_reference)",
		"CREATE UNIQUE INDEX hosted_provider_create_reference_idx",
		"CREATE TRIGGER provider_order_economic_immutable BEFORE UPDATE ON provider_orders",
		"provider order economic and create evidence is immutable",
		"CREATE TRIGGER provider_inbox_immutable BEFORE UPDATE OR DELETE ON provider_inbox",
		"CREATE TRIGGER provider_reconcile_observation_immutable BEFORE UPDATE OR DELETE ON provider_reconcile_observations",
		"ALTER TABLE hosted_provider_create_attempts FORCE ROW LEVEL SECURITY",
		"ALTER TABLE provider_orders FORCE ROW LEVEL SECURITY",
		"ALTER TABLE provider_inbox FORCE ROW LEVEL SECURITY",
		"ALTER TABLE provider_reconcile_observations FORCE ROW LEVEL SECURITY",
		"ALTER TABLE provider_prebind_inbox FORCE ROW LEVEL SECURITY",
		"CREATE TRIGGER provider_prebind_evidence_immutable BEFORE UPDATE ON provider_prebind_inbox",
		"UNIQUE (provider_id,provider_event_id)",
		"tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid",
		"merchant_id=nullif(current_setting('app.merchant_id',true),'')::uuid",
		"create_response_digest=digest(create_response_body,'sha256')",
		"CREATE FUNCTION claim_hosted_create_recoveries",
		"CREATE FUNCTION claim_hosted_prebind_recoveries",
		"CREATE FUNCTION claim_hosted_order_recoveries",
		"SET search_path=pg_catalog,public SET row_security=off",
		"REVOKE ALL ON FUNCTION claim_hosted_create_recoveries",
	}
	for _, marker := range markers {
		if !strings.Contains(sql, marker) {
			t.Fatalf("hosted migration lost invariant %q", marker)
		}
	}
	if count := strings.Count(sql, "CREATE TRIGGER provider_reconcile_observation_immutable"); count != 1 {
		t.Fatalf("provider reconcile observation trigger count=%d, want one", count)
	}
}

func TestHostedRecoveryClaimsAreFencedAndProviderOpsAdmitted(t *testing.T) {
	source, err := os.ReadFile("hosted_recovery.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, marker := range []string{
		"claim_hosted_create_recoveries",
		"claim_hosted_prebind_recoveries",
		"claim_hosted_order_recoveries",
		"ReplayHostedPrebind",
	} {
		if !strings.Contains(text, marker) {
			t.Fatalf("hosted recovery lost fence %q", marker)
		}
	}
	migration := hostedMigration(t)
	for _, marker := range []string{"FOR UPDATE OF a SKIP LOCKED", "recovery_claim_token=gen_random_uuid()", "reconcile_claim_token=gen_random_uuid()", "GRANT EXECUTE ON FUNCTION claim_hosted_create_recoveries"} {
		if !strings.Contains(migration, marker) {
			t.Fatalf("hosted definer claim lost fence %q", marker)
		}
	}
	if strings.Contains(migration, "public.admit_hosted_provider_operation") {
		t.Fatal("000016 depends on the provider-operations admission function introduced by 000017")
	}
	worker, err := os.ReadFile("../../hostedproviders/worker.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{`AdmitHostedOperation(ctx, job, "create")`, `AdmitHostedOperation(ctx, job, "cancel")`, `AdmitHostedOperation(ctx, job, "reconciliation")`} {
		if !strings.Contains(string(worker), marker) {
			t.Fatalf("hosted worker lost last-moment admission %q", marker)
		}
	}
	completionStart := strings.Index(text, "func (s *Store) CompleteHostedCreateRecovery")
	if completionStart < 0 {
		t.Fatal("hosted recovery completion boundary is missing")
	}
	completionSource := text[completionStart:]
	if strings.Contains(completionSource, "pgx.BeginTxFunc(ctx, s.db.pool") || strings.Contains(completionSource, "s.db.pool.Exec") {
		t.Fatal("hosted recovery mutation bypasses tenant+merchant transaction context")
	}
	for _, marker := range []string{"func (s *Store) MarkHostedCreateCancelled", "func (s *Store) RecordHostedReconcileObservation", "func (s *Store) RetryHostedRecovery", "return s.withinMerchant(ctx, principal"} {
		if !strings.Contains(completionSource, marker) {
			t.Fatalf("hosted recovery completion lost merchant fence %q", marker)
		}
	}
}

func TestHostedReadinessChecksMigrationsFunctionsAndRuntimeGrants(t *testing.T) {
	api, err := os.ReadFile("hosted_provider.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{"provider_prebind_inbox", "hosted_provider_callback_config_admitted(text,text)", "hosted_provider_outbound_config_admitted(uuid,uuid,text,text)", "admit_hosted_provider_operation", "has_function_privilege", "has_table_privilege"} {
		if !strings.Contains(string(api), marker) {
			t.Fatalf("API readiness lost capability check %q", marker)
		}
	}
	worker, err := os.ReadFile("hosted_recovery.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{"claim_hosted_create_recoveries", "claim_hosted_prebind_recoveries", "claim_hosted_order_recoveries", "has_function_privilege"} {
		if !strings.Contains(string(worker), marker) {
			t.Fatalf("worker readiness lost capability check %q", marker)
		}
	}
}

func TestHostedRecoveryMutationsRejectExpiredLeasesUsingDatabaseClock(t *testing.T) {
	recovery, err := os.ReadFile("hosted_recovery.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(recovery)
	for _, marker := range []string{
		"recovery_claim_until>=clock_timestamp()",
		"reconcile_claim_until>=clock_timestamp()",
		"claim_until>=clock_timestamp()",
		"leaseUpdate.RowsAffected() != 1",
	} {
		if !strings.Contains(text, marker) {
			t.Fatalf("stale hosted worker lease fence is missing %q", marker)
		}
	}
	settlement, err := os.ReadFile("hosted_settlement.go")
	if err != nil {
		t.Fatal(err)
	}
	settlementText := string(settlement)
	lock := strings.Index(settlementText, "claim_until>=clock_timestamp() FOR UPDATE")
	ledger := strings.Index(settlementText, "INSERT INTO ledger_transactions")
	attach := strings.Index(settlementText, "SET state='attached'")
	if lock < 0 || ledger < 0 || attach < 0 || lock > ledger || ledger > attach {
		t.Fatal("pre-bind lease validation, settlement, and attach are not one ordered transaction")
	}
	route, err := os.ReadFile("atomic_route.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(route), "recovery_claim_until>=clock_timestamp()") {
		t.Fatal("hosted route binding accepts an expired recovery worker lease")
	}
}

func TestHostedSettlementComparesRouteAndProviderEconomicsBeforeLedger(t *testing.T) {
	source, err := os.ReadFile("hosted_settlement.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, marker := range []string{"r.expected_amount_atomic::text", "r.asset_id", "r.asset_decimals", "candidate.EconomicDrift", "provider_callback_quarantined"} {
		if !strings.Contains(text, marker) {
			t.Fatalf("hosted settlement lost defense %q", marker)
		}
	}
	prebindCheck := strings.Index(text, "SELECT provider_reference,provider_status,asset_id,amount_atomic::text,asset_decimals,raw_body_digest,signature_digest,config_manifest_id::text,config_version FROM provider_prebind_inbox")
	boundInsert := strings.Index(text, "INSERT INTO provider_inbox")
	ledgerInsert := strings.Index(text, "INSERT INTO ledger_transactions")
	if prebindCheck < 0 || boundInsert < 0 || ledgerInsert < 0 || prebindCheck > boundInsert || boundInsert > ledgerInsert {
		t.Fatal("pre-bind tamper check/evidence insert no longer precedes hosted ledger booking")
	}
}

func TestHostedPaymentLinkJobRejectsScopeMismatchEconomicTamperAndReopen(t *testing.T) {
	sql := hostedPaymentLinkMigration(t)
	for _, marker := range []string{
		"UNIQUE(id,payment_link_id,tenant_id,merchant_id,intent_id)",
		"FOREIGN KEY (redemption_id,payment_link_id,tenant_id,merchant_id,intent_id)",
		"UNIQUE(id,tenant_id,merchant_id,provider_id,intent_id)",
		"FOREIGN KEY (create_attempt_id,tenant_id,merchant_id,provider_id,intent_id)",
		"hosted payment-link job identity and economics are immutable",
		"OLD.state='preparing' AND NEW.state IN ('bound','terminal')",
		"hosted payment-link job terminal state is immutable",
		"NEW.state='terminal' AND NEW.route_id IS NULL AND NEW.last_error_code IS NOT NULL",
		"hosted provider create request facts are immutable",
		"hosted provider create evidence requires completed transition",
		"ALTER TABLE hosted_payment_link_jobs FORCE ROW LEVEL SECURITY",
		"ALTER TABLE hosted_payment_link_incidents FORCE ROW LEVEL SECURITY",
	} {
		if !strings.Contains(sql, marker) {
			t.Fatalf("000019 lost deliberate mismatch/tamper/reopen defense %q", marker)
		}
	}
	if strings.Contains(sql, "encrypted_checkout_token") {
		t.Fatal("hosted payment-link job duplicates the checkout bearer capability")
	}
	if strings.Contains(sql, "GRANT SELECT,INSERT,UPDATE ON hosted_payment_link_jobs TO merchant_management_runtime") || strings.Contains(sql, "GRANT SELECT,INSERT,UPDATE ON hosted_provider_create_attempts TO merchant_management_runtime") {
		t.Fatal("management runtime regained broad update on hosted payment-link economics")
	}
}

func TestHostedPaymentLinkRedemptionStagesBeforeAnyProviderIO(t *testing.T) {
	source, err := os.ReadFile("../../management/postgres_redeem.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, marker := range []string{
		"hosted-payment-link-v1\\x00",
		"INSERT INTO hosted_provider_create_attempts",
		"INSERT INTO hosted_payment_link_jobs",
		`Status: "preparing_payment_route"`,
		"return s.remember",
	} {
		if !strings.Contains(text, marker) {
			t.Fatalf("hosted redemption lost durable staging marker %q", marker)
		}
	}
	for _, forbidden := range []string{".Adapter.Create(", ".Create(ctx,", "encrypted_checkout_token"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("payment-link redemption crosses provider/bearer boundary %q", forbidden)
		}
	}
}
