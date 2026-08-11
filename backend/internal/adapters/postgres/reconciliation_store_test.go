package postgres

import (
	"strings"
	"testing"

	"github.com/calmshv-star/ocrypt/backend/internal/reconciliation"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestReconciliationUniqueViolationIsIdempotencyConflict(t *testing.T) {
	if got := classifyReconciliation(&pgconn.PgError{Code: "23505"}); got != reconciliation.ErrIdempotencyConflict {
		t.Fatalf("expected reconciliation idempotency conflict, got %v", got)
	}
}

func TestReconciliationReportsDoNotMutateLedger(t *testing.T) {
	migration := financialMigration(t)
	start := strings.Index(migration, "CREATE TABLE financial_reconciliation_runs")
	if start < 0 {
		t.Fatal("reconciliation schema not found")
	}
	section := migration[start:]
	if strings.Contains(section, "REFERENCES financial_ledger") {
		t.Fatal("reconciliation reports must remain evidence-only and not create ledger coupling")
	}
}
