package management

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestCheckoutPaymentProgressUsesExactAtomicArithmetic(t *testing.T) {
	received, remaining, canTopUp := checkoutPaymentProgress("6120000", "4620000", 6)
	if received != "4.62" || remaining != "1.5" || !canTopUp {
		t.Fatalf("unexpected progress: received=%q remaining=%q canTopUp=%v", received, remaining, canTopUp)
	}

	received, remaining, canTopUp = checkoutPaymentProgress("6120000", "7000000", 6)
	if received != "7" || remaining != "0" || canTopUp {
		t.Fatalf("overpayment must have no remainder: received=%q remaining=%q canTopUp=%v", received, remaining, canTopUp)
	}

	if received, remaining, canTopUp = checkoutPaymentProgress("6120000", "0", 6); received != "" || remaining != "" || canTopUp {
		t.Fatalf("zero progress must stay absent: received=%q remaining=%q canTopUp=%v", received, remaining, canTopUp)
	}
}

func TestPublicCheckoutStatusKeepsPartialPaymentDistinct(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	if got := publicCheckoutStatus("partially_paid", now.Add(time.Minute), now); got != "partially_paid" {
		t.Fatalf("partial payment was collapsed into %q", got)
	}
	if got := publicCheckoutStatus("partially_paid", now, now); got != "expired" {
		t.Fatalf("expired partial payment must not invite another transfer: %q", got)
	}
	for _, internal := range []string{"needs_review", "reorg_review"} {
		if got := publicCheckoutStatus(internal, now.Add(time.Minute), now); got != "needs_review" {
			t.Fatalf("%s must remain visibly fail-closed, got %q", internal, got)
		}
		if got := publicCheckoutStatus(internal, now, now); got != "needs_review" {
			t.Fatalf("expired %s must not invite a replacement payment, got %q", internal, got)
		}
	}
}

func TestCheckoutProjectionReadsDurableAggregateAndFrozenPolicy(t *testing.T) {
	source, err := os.ReadFile("postgres_links_checkout.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, marker := range []string{
		"LEFT JOIN payment_match_aggregates pma",
		"pma.state<>'reversed'",
		"matched.allocation_role='payment'",
		"binding.policy_snapshot->>'accumulate_partials'",
		"binding.policy_snapshot->>'accept_late_within_grace'",
		"collectionDeadline.After(now)",
	} {
		if !strings.Contains(text, marker) {
			t.Fatalf("checkout progress is missing durable safety marker %q", marker)
		}
	}
}

func TestCheckoutProgressRuntimeRoleIsReadOnlyAndReadinessChecked(t *testing.T) {
	grants, err := os.ReadFile("../../../deploy/postgres/runtime-grants.sql")
	if err != nil {
		t.Fatal(err)
	}
	grantText := string(grants)
	const checkoutGrant = "payment_matches,transfer_events,payment_match_aggregates,payment_route_policy_bindings\nTO merchant_management_runtime;"
	if !strings.Contains(grantText, checkoutGrant) {
		t.Fatal("management checkout requires one explicit read-only progress grant")
	}

	postgres, err := os.ReadFile("postgres.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{
		"has_table_privilege(current_user,'public.payment_match_aggregates','SELECT')",
		"has_table_privilege(current_user,'public.payment_route_policy_bindings','SELECT')",
	} {
		if !strings.Contains(string(postgres), marker) {
			t.Fatalf("management readiness is missing %q", marker)
		}
	}
}
