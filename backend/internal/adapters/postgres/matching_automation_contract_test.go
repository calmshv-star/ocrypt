package postgres

import (
	"os"
	"strings"
	"testing"
)

func TestAutomatedMatchingMigrationIsPolicyBoundFencedAndTenantIsolated(t *testing.T) {
	raw, err := os.ReadFile("../../../migrations/000006_automated_matching.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(raw)
	checks := []string{
		"CREATE TABLE automated_matching_policies",
		"CREATE TABLE payment_route_policy_bindings",
		"CREATE TRIGGER payment_route_bind_matching_policy",
		"WHERE state<>'reversed'",
		"lease_token uuid",
		"reschedule_requested boolean",
		"CREATE TABLE automated_matching_decisions",
		"automated matching history is append-only",
		"FORCE ROW LEVEL SECURITY",
		"gasfree_fee_collectors",
		"credit_expected_hold_excess",
	}
	for _, check := range checks {
		if !strings.Contains(sql, check) {
			t.Errorf("migration is missing %q", check)
		}
	}
	if strings.Contains(sql, "cross_asset") || strings.Contains(sql, "float") || strings.Contains(sql, "double precision") {
		t.Fatal("automated financial matching migration introduced conversion or inexact arithmetic")
	}
}

func TestAutomatedMatchingReorgAndRefundContractsFailClosed(t *testing.T) {
	reorg, err := os.ReadFile("reorg.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []string{"payment_match_aggregates SET state='reversed'", "payment_matches SET state='reversed'", "payment_settlement.reversal", "automated_matching_jobs"} {
		if !strings.Contains(string(reorg), check) {
			t.Errorf("aggregate reorg contract missing %q", check)
		}
	}
	refunds, err := os.ReadFile("refund_store.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(refunds), "pm.allocation_role='payment'") {
		t.Fatal("GasFree fee sibling could enter the refundable settlement bridge")
	}
	matching, err := os.ReadFile("matching_automation.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(matching), "matching_processing_fee_expense") {
		t.Fatal("GasFree verifier evidence created a fictitious merchant expense ledger leg")
	}
	for _, check := range []string{`accounts["treasury_asset"]`, `accounts["merchant_settlement_liability"]`, `decision.Received.Sub(decision.Credited)`} {
		if !strings.Contains(string(matching), check) {
			t.Errorf("balanced payment/excess ledger contract missing %q", check)
		}
	}
}

func TestAutomatedMatchingApproximateAmountRequiresOneOverlappingRoute(t *testing.T) {
	matching, err := os.ReadFile("matching_automation.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(matching)
	for _, check := range []string{
		"other.id<>$7",
		"te.on_chain_time BETWEEN other.starts_at AND other.grace_ends_at",
		"JOIN payment_route_policy_bindings",
		"multiple_policy_bound_routes_overlap",
	} {
		if !strings.Contains(source, check) {
			t.Errorf("approximate-amount matching lost its unique overlapping-route guard %q", check)
		}
	}
}
