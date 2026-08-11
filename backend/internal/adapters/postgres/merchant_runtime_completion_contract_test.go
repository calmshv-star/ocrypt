package postgres

import (
	"os"
	"strings"
	"testing"
)

func TestMerchantRuntimeCompletionMigrationIsAtomicAndTenantFenced(t *testing.T) {
	raw, err := os.ReadFile("../../../migrations/000008_merchant_runtime_completion.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	migration := string(raw)
	for _, marker := range []string{
		"BEGIN;",
		"COMMIT;",
		"LOCK TABLE ledger_transactions IN ACCESS EXCLUSIVE MODE",
		"ORDER BY booked_at,id",
		"CREATE TABLE payment_intent_versions",
		"payment_intents_capture_insert",
		"payment_intents_capture_update",
		"payment_intents_require_version_advance",
		"CREATE TABLE reconciliation_reports",
		"ALTER TABLE reconciliation_reports FORCE ROW LEVEL SECURITY",
		"merchant_reconciliation_worker",
		"LOCK TABLE callback_events IN SHARE MODE",
		"CREATE TABLE merchant_event_sequences",
		"ALTER TABLE merchant_event_sequences FORCE ROW LEVEL SECURITY",
		"merchant_event_sequences_tenant_policy",
	} {
		if !strings.Contains(migration, marker) {
			t.Errorf("000008 lost production invariant %q", marker)
		}
	}
	if !strings.HasPrefix(strings.TrimSpace(migration), "BEGIN;") || !strings.HasSuffix(strings.TrimSpace(migration), "COMMIT;") {
		t.Fatal("000008 must be one fail-atomic PostgreSQL transaction")
	}
}

func TestEveryCallbackWriterUsesTransactionalMerchantAllocator(t *testing.T) {
	for _, file := range []string{
		"merchant_runtime_completion.go",
		"matching_automation.go",
		"resolution_settlement.go",
		"reorg.go",
		"settlement.go",
	} {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		source := string(raw)
		if strings.Contains(source, "max(merchant_sequence)") {
			t.Fatalf("%s bypasses the durable merchant event allocator", file)
		}
		if strings.Contains(source, "INSERT INTO callback_events") && !strings.Contains(source, "nextMerchantEventSequence") {
			t.Fatalf("%s writes callbacks without allocating a tenant/merchant sequence", file)
		}
	}
	allocator, err := os.ReadFile("callback_sequence.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{"ON CONFLICT(tenant_id,merchant_id) DO UPDATE", "last_sequence=merchant_event_sequences.last_sequence+1", "RETURNING last_sequence"} {
		if !strings.Contains(string(allocator), marker) {
			t.Fatalf("transactional allocator invariant missing %q", marker)
		}
	}
}

func TestReconciliationSnapshotAndLatePaymentGraceAreExplicit(t *testing.T) {
	raw, err := os.ReadFile("merchant_runtime_completion.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	for _, marker := range []string{
		"LOCK TABLE ledger_transactions IN SHARE MODE",
		"period_end must not exceed snapshot_cutoff",
		"fenceSequence, fenceSequence, snapshotCutoff",
		"route.grace_ends_at<=$1",
		"upper(reservation.active_window)<=$1",
		"route_grace_elapsed",
	} {
		if !strings.Contains(source, marker) {
			t.Fatalf("snapshot/grace invariant missing %q", marker)
		}
	}
	expireStart := strings.Index(source, "func (s *Store) ExpireIntent")
	expireEnd := strings.Index(source[expireStart:], "func (s *Store) ReleaseElapsedRouteGrace")
	if expireStart < 0 || expireEnd < 0 {
		t.Fatal("expire implementation boundary is missing")
	}
	if strings.Contains(source[expireStart:expireStart+expireEnd], "amount_reservations SET state='released'") {
		t.Fatal("intent expiry released the late-payment collision reservation before grace")
	}
}
