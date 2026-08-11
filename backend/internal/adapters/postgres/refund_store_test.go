package postgres

import (
	"os"
	"strings"
	"testing"

	"github.com/calmshv-star/ocrypt/backend/internal/refunds"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestRefundUniqueViolationIsStateConflict(t *testing.T) {
	if got := classifyRefund(&pgconn.PgError{Code: "23505"}); got != refunds.ErrStateConflict {
		t.Fatalf("expected refund state conflict, got %v", got)
	}
}

func TestRefundMigrationRequiresVerifiedNonObservedDestination(t *testing.T) {
	migration := financialMigration(t)
	required := "method text NOT NULL CHECK (method IN ('wallet_signature','custodian_return_instruction','merchant_evidence'))"
	if !strings.Contains(migration, required) {
		t.Fatal("observed sender must never be accepted as destination verification")
	}
	if !strings.Contains(migration, "refund_to_origin_only boolean NOT NULL DEFAULT true") {
		t.Fatal("refund origin restriction must default closed")
	}
}

func TestRefundSettlementBridgeRequiresFinalizedMatchAndEvent(t *testing.T) {
	content, err := os.ReadFile("refund_store.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(content)
	for _, required := range []string{"pm.state='finalized' AND te.status='finalized'", "pm.received_atomic", "pm.state='reversed' OR te.status IN ('reorged','invalidated')", "coreMatchState"} {
		if !strings.Contains(source, required) {
			t.Fatalf("refund bridge missing %q", required)
		}
	}
}

func TestFinalizedTransferAloneCannotAuthorizeRefund(t *testing.T) {
	content, err := os.ReadFile("refund_store.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(content), "INSERT INTO financial_verified_refund_destinations") {
		t.Fatal("observed transfer was promoted to verified destination")
	}
}
