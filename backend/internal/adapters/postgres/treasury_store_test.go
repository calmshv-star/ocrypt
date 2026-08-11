package postgres

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/treasury"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestFinancialMigrationRejectsCrossTenantAndCrossAssetReferences(t *testing.T) {
	migration := financialMigration(t)
	required := []string{
		"FOREIGN KEY (chain_id, asset_id) REFERENCES assets(chain_id, id)",
		"FOREIGN KEY (intent_id, tenant_id) REFERENCES payment_intents(id, tenant_id)",
		"financial_refund_match_scope_unique",
		"FOREIGN KEY (payment_match_id, tenant_id, chain_event_id, intent_id)",
		"FOREIGN KEY (transaction_id, tenant_id, asset_id)",
		"REFERENCES financial_ledger_transactions(id, tenant_id, asset_id)",
		"ALTER TABLE %I FORCE ROW LEVEL SECURITY",
		"CREATE FUNCTION append_financial_audit",
		"previous_hash bytea NOT NULL",
		"entry_hash bytea NOT NULL",
		"pg_advisory_xact_lock",
		"CREATE TABLE financial_proxy_nonces",
		"CREATE TABLE financial_outbox",
		"dead_lettered_at timestamptz",
	}
	for _, contract := range required {
		if !strings.Contains(migration, contract) {
			t.Errorf("migration is missing deliberate-violation defense %q", contract)
		}
	}
}

func TestProxyNonceContractHasNoMerchantCredentialDependency(t *testing.T) {
	migration := financialMigration(t)
	start := strings.Index(migration, "CREATE TABLE financial_proxy_nonces")
	end := strings.Index(migration[start:], ");")
	if start < 0 || end < 0 {
		t.Fatal("proxy nonce table missing")
	}
	section := migration[start : start+end]
	if strings.Contains(section, "api_clients") || strings.Contains(section, "merchant") || strings.Contains(section, "tenant_id") {
		t.Fatal("financial proxy nonce must be consumable before merchant or tenant lookup")
	}
}

func TestAuditStoreCanOnlyAppendThroughHashChainFunction(t *testing.T) {
	content, err := os.ReadFile("treasury_store.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	if !strings.Contains(text, "SELECT append_financial_audit") || strings.Contains(text, "INSERT INTO financial_audit_log") {
		t.Fatal("store bypasses tamper-evident audit append function")
	}
}

func TestFinancialMigrationHasOneReconciliationIdempotencyColumn(t *testing.T) {
	migration := financialMigration(t)
	start := strings.Index(migration, "CREATE TABLE financial_reconciliation_runs")
	end := strings.Index(migration[start:], ");")
	if start < 0 || end < 0 {
		t.Fatal("reconciliation run table not found")
	}
	table := migration[start : start+end]
	if count := strings.Count(table, "idempotency_key text"); count != 1 {
		t.Fatalf("reconciliation run must declare one idempotency key, got %d", count)
	}
}

func TestFinancialLedgerLegHasOneAmountColumn(t *testing.T) {
	migration := financialMigration(t)
	start := strings.Index(migration, "CREATE TABLE financial_ledger_legs")
	end := strings.Index(migration[start:], ");")
	if start < 0 || end < 0 {
		t.Fatal("ledger legs table missing")
	}
	section := migration[start : start+end]
	if count := strings.Count(section, "amount_atomic uint256"); count != 1 {
		t.Fatalf("ledger leg amount column count=%d", count)
	}
}

func TestFinancialMigrationIsIndependentOfAdminControlPlane(t *testing.T) {
	migration := financialMigration(t)
	for _, forbidden := range []string{"operator_users", "operator_roles", "approval_requests", "admin_"} {
		if strings.Contains(migration, forbidden) {
			t.Fatalf("000003 must not depend on 000002 admin object %q", forbidden)
		}
	}
}

func TestFinancialLeaseValidationFailsClosedWithoutPool(t *testing.T) {
	_, acquired, err := AcquireFinancialLease(context.Background(), nil, "tenant", "sweep", "id", "worker", time.Minute)
	if err == nil || acquired {
		t.Fatal("invalid lease configuration must fail closed")
	}
}

func TestTreasuryUniqueViolationIsStateConflict(t *testing.T) {
	err := classifyTreasury(&pgconn.PgError{Code: "23505"})
	if err != treasury.ErrStateConflict {
		t.Fatalf("expected state conflict, got %v", err)
	}
}

func TestIntervalLiteralUsesIntegerMilliseconds(t *testing.T) {
	if got := intervalLiteral(1500 * time.Millisecond); got != "1500 milliseconds" {
		t.Fatalf("unexpected interval %q", got)
	}
}

func TestFinancialCabinetMigrationClosesPermissionsAndDecisionReplay(t *testing.T) {
	migration := financialCabinetMigration(t, "up")
	for _, marker := range []string{
		"CREATE FUNCTION list_current_admin_financial_permissions(requested_tenant uuid)",
		"u.id=nullif(current_setting('app.admin_user_id',true),'')::uuid",
		"b.merchant_id IS NULL",
		"b.tenant_id IS NULL OR b.tenant_id=requested_tenant",
		"CREATE TABLE financial_operator_idempotency",
		"PRIMARY KEY(tenant_id,actor_id,operation,idempotency_key)",
		"request_fingerprint bytea NOT NULL CHECK(octet_length(request_fingerprint)=32)",
		"ALTER TABLE financial_operator_idempotency FORCE ROW LEVEL SECURITY",
		"GRANT SELECT,INSERT ON financial_operator_idempotency TO merchant_financial_runtime",
		"'sweep.approve','sweep.cancel','refund.approve','refund.cancel','reconciliation.execute'",
	} {
		if !strings.Contains(migration, marker) {
			t.Errorf("000014 is missing financial cabinet guard %q", marker)
		}
	}
	for _, forbidden := range []string{
		"('support_operator','financial:",
		"('payment_operator','financial:",
		"('treasury_operator','financial:sweep_approve')",
		"('treasury_operator','financial:refund_approve')",
		"('senior_approver','financial:sweep_create')",
		"('senior_approver','financial:refund_create')",
	} {
		if strings.Contains(migration, forbidden) {
			t.Errorf("000014 grants a forbidden financial capability %q", forbidden)
		}
	}
	down := financialCabinetMigration(t, "down")
	for _, marker := range []string{
		"REVOKE EXECUTE ON FUNCTION list_current_admin_financial_permissions(uuid) FROM merchant_admin_runtime",
		"REVOKE ALL ON financial_operator_idempotency FROM merchant_financial_runtime",
		"DROP TABLE financial_operator_idempotency",
		"DROP FUNCTION list_current_admin_financial_permissions(uuid)",
	} {
		if !strings.Contains(down, marker) {
			t.Errorf("000014 rollback is missing %q", marker)
		}
	}
}

func financialMigration(t *testing.T) string {
	t.Helper()
	content, err := os.ReadFile("../../../migrations/000003_financial_ops.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func financialCabinetMigration(t *testing.T, direction string) string {
	t.Helper()
	content, err := os.ReadFile("../../../migrations/000014_financial_admin_cabinet." + direction + ".sql")
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}
