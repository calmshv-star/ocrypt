package postgres

import (
	"os"
	"strings"
	"testing"
)

func TestPaymentToleranceMigrationAppendsVersionedPolicy(t *testing.T) {
	raw, err := os.ReadFile("../../../migrations/000039_activate_payment_tolerance.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	migration := string(raw)
	for _, required := range []string{
		"CREATE TEMP TABLE matching_policy_tolerance_rollout",
		"p.underpayment_tolerance_bps<>500",
		"p.overpayment_mode<>'credit_expected_hold_excess'",
		"INSERT INTO automated_matching_policy_changes",
		"INSERT INTO automated_matching_policies",
		"500,'credit_expected_hold_excess'",
		"p.accumulate_partials",
		"p.accept_late_within_grace",
		"p.require_same_sender",
		"p.gasfree_enabled",
		"p.gasfree_fee_collectors",
	} {
		if !strings.Contains(migration, required) {
			t.Fatalf("payment tolerance migration is missing %q", required)
		}
	}
	if strings.Contains(migration, "UPDATE automated_matching_policies") || strings.Contains(migration, "DELETE FROM automated_matching_policies") {
		t.Fatal("payment tolerance migration mutates append-only policy history")
	}
}
