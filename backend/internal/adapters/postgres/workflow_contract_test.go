package postgres

import (
	"os"
	"strings"
	"testing"
)

func platformMigration(t *testing.T) string {
	t.Helper()
	contents, err := os.ReadFile("../../../migrations/000001_platform.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}

func TestAdvisoryAIAndManualApprovalSchemaFailClosed(t *testing.T) {
	sql := platformMigration(t)
	for _, marker := range []string{
		"CREATE TABLE ai_rank_suggestions",
		"review_required boolean NOT NULL CHECK (review_required)",
		"FOREIGN KEY (recommended_route_id, tenant_id) REFERENCES payment_routes(id, tenant_id)",
		"ALTER TABLE ai_rank_suggestions FORCE ROW LEVEL SECURITY",
		"status <> 'approval_required' OR ((accept_shortfall OR accept_cross_asset) AND approved_by IS NULL)",
		"approved_by <> requested_by",
	} {
		if !strings.Contains(sql, marker) {
			t.Fatalf("workflow migration lost guard %q", marker)
		}
	}
}

func TestProofQueueAndExternalOutboxHaveFencedDurabilityColumns(t *testing.T) {
	sql := platformMigration(t)
	for _, marker := range []string{
		"CREATE INDEX payment_proofs_queue_idx ON payment_proofs (chain_id, next_attempt_at, id)",
		"lease_token uuid",
		"CREATE TABLE event_history",
		"CREATE INDEX outbox_publish_idx ON outbox_events (available_at, id) WHERE published_at IS NULL",
		"FOREIGN KEY (merchant_id, tenant_id) REFERENCES merchants(id, tenant_id)",
	} {
		if !strings.Contains(sql, marker) {
			t.Fatalf("durable worker migration lost guard %q", marker)
		}
	}
}

func TestPersistedRoutePlanningHasCompensatingReleaseFence(t *testing.T) {
	source, err := os.ReadFile("planning.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, marker := range []string{
		`lockIdempotency(ctx, tx, p.MerchantID, "create_route_plan", idempotencyKey)`,
		`lockIdempotency(ctx, tx, p.MerchantID, "create_route", idempotencyKey)`,
		"status='released'",
		"planning_idempotency_key=NULL,planning_request_hash=NULL",
		"status == \"bound\"",
	} {
		if !strings.Contains(text, marker) {
			t.Fatalf("route-plan compensation lost guard %q", marker)
		}
	}
}

func TestManualResolutionBindsLatestEligibleCandidateThroughSettlement(t *testing.T) {
	migration := platformMigration(t)
	for _, marker := range []string{
		"candidate_set_version bigint NOT NULL CHECK (candidate_set_version > 0)",
		"FOREIGN KEY (unmatched_id, candidate_set_version, target_route_id) REFERENCES match_candidates(unmatched_id, candidate_set_version, route_id)",
	} {
		if !strings.Contains(migration, marker) {
			t.Fatalf("manual resolution schema lost candidate-version guard %q", marker)
		}
	}
	operatorSource, err := os.ReadFile("operator_store.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{
		"cardinality(c.disqualifiers)=0",
		"c.candidate_set_version=(SELECT max(latest.candidate_set_version)",
		"c.candidate_set_version=mr.candidate_set_version",
	} {
		if !strings.Contains(string(operatorSource), marker) {
			t.Fatalf("manual resolution request/approval lost candidate guard %q", marker)
		}
	}
	settlementSource, err := os.ReadFile("resolution_settlement.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{
		"JOIN match_candidates c ON",
		"c.candidate_set_version=mr.candidate_set_version",
		"cardinality(c.disqualifiers)=0",
		`"candidate_set_version": stored.CandidateSetVersion`,
	} {
		if !strings.Contains(string(settlementSource), marker) {
			t.Fatalf("manual settlement lost candidate guard/evidence %q", marker)
		}
	}
}
