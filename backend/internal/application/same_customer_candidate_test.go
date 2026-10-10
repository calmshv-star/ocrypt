package application

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/money"
)

// Context is authoritative merchant-authenticated intent data, never an address
// inferred by the scanner or a customer identity copied from candidate evidence.
func sameCustomerCandidateFixture(t *testing.T, amounts ...string) (domain.TransferEvent, []Candidate, map[string]AutomaticCandidateContext) {
	t.Helper()
	paidAt := time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)
	event := domain.TransferEvent{ID: "synthetic-transfer", Amount: money.MustParse("4900000000"), OnChainTime: paidAt, Status: domain.TransferFinalized, Confirmations: 12, Identity: domain.EventIdentity{ChainID: "synthetic:chain", AssetID: "synthetic:asset", ToAddress: "synthetic:recipient"}}
	var routes []domain.PaymentRoute
	contexts := make(map[string]AutomaticCandidateContext)
	for n, amount := range amounts {
		id := []string{"closer", "farther", "third"}[n]
		route := domain.PaymentRoute{ID: id, IntentID: "intent-" + id, ChainID: event.Identity.ChainID, AssetID: event.Identity.AssetID, Address: event.Identity.ToAddress, ExpectedAmount: money.MustParse(amount), StartsAt: paidAt.Add(-time.Minute), ExpiresAt: paidAt.Add(time.Hour), GraceEndsAt: paidAt.Add(24 * time.Hour), Status: domain.RouteActive, RequiredFinality: 12}
		intent := domain.PaymentIntent{ID: route.IntentID, TenantID: "synthetic-tenant", MerchantID: "synthetic-merchant", CustomerReference: "authenticated-customer", MerchantOrderID: "order-" + id, AmountMinor: money.MustParse("499"), Currency: "USD", CurrencyScale: 2, Metadata: json.RawMessage(`{}`), Status: domain.IntentPending}
		contexts[id] = AutomaticCandidateContext{Route: route, Intent: intent}
		routes = append(routes, route)
	}
	return event, BuildCandidates(event, routes, paidAt), contexts
}

func TestSameCustomerCandidateSelectsStrictlyClosestSmallOverpayment(t *testing.T) {
	event, candidates, contexts := sameCustomerCandidateFixture(t, "4813000000", "4812000000")
	if len(candidates) != 2 || candidates[0].Score != 100 || candidates[1].Score != 100 || candidates[0].AmountDelta != "87000000" || candidates[1].AmountDelta != "88000000" {
		t.Fatalf("fixture did not reproduce exact score100/100 small-overpayment tie: %#v", candidates)
	}
	selected, ok := SelectAutomaticCandidate(event, candidates, contexts)
	if !ok || selected.RouteID != "closer" {
		t.Fatalf("M1: equivalent repeated same-customer invoices must select strictly closer route, got %#v, %v", selected, ok)
	}
	if _, ok := UniqueAutomaticCandidate(candidates); ok {
		t.Fatal("generic selector resolved tied invoices without authenticated context")
	}
}

func TestSameCustomerCandidateCanonicalMetadataAndEveryTiedRoute(t *testing.T) {
	event, candidates, contexts := sameCustomerCandidateFixture(t, "4813000000", "4812000000", "4811000000")
	for id, c := range contexts {
		c.Intent.Metadata = json.RawMessage(`{"product":"month","flags":{"renew":false},"units":9007199254740993}`)
		if id == "farther" {
			c.Intent.Metadata = json.RawMessage(`{ "units":9007199254740993, "flags":{"renew":false}, "product":"month" }`)
		}
		contexts[id] = c
	}
	if selected, ok := SelectAutomaticCandidate(event, candidates, contexts); !ok || selected.RouteID != "closer" {
		t.Fatalf("M1: canonical metadata or third equivalent route rejected: %#v %v", selected, ok)
	}
	third := contexts["third"]
	third.Intent.CustomerReference = "another-customer"
	contexts["third"] = third
	if selected, ok := SelectAutomaticCandidate(event, candidates, contexts); ok {
		t.Fatalf("M2: third equally scored stranger was ignored: %#v", selected)
	}
}

func TestSameCustomerCandidateRejectsUnsafeContext(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*AutomaticCandidateContext)
	}{
		{"different_customer", func(c *AutomaticCandidateContext) { c.Intent.CustomerReference = "different" }},
		{"empty_customer", func(c *AutomaticCandidateContext) { c.Intent.CustomerReference = "" }},
		{"different_tenant", func(c *AutomaticCandidateContext) { c.Intent.TenantID = "another-tenant" }},
		{"different_merchant", func(c *AutomaticCandidateContext) { c.Intent.MerchantID = "another-merchant" }},
		{"different_fiat_amount", func(c *AutomaticCandidateContext) { c.Intent.AmountMinor = money.MustParse("500") }},
		{"different_fiat_currency", func(c *AutomaticCandidateContext) { c.Intent.Currency = "EUR" }},
		{"different_fiat_scale", func(c *AutomaticCandidateContext) { c.Intent.CurrencyScale = 3 }},
		{"different_metadata", func(c *AutomaticCandidateContext) { c.Intent.Metadata = json.RawMessage(`{"product":"different"}`) }},
		{"invalid_metadata", func(c *AutomaticCandidateContext) { c.Intent.Metadata = json.RawMessage(`{`) }},
		{"equal_distance", func(c *AutomaticCandidateContext) { c.Route.ExpectedAmount = money.MustParse("4813000000") }},
		{"outside_primary_window", func(c *AutomaticCandidateContext) { c.Route.ExpiresAt = c.Route.StartsAt }},
		{"before_route_start", func(c *AutomaticCandidateContext) { c.Route.StartsAt = c.Route.ExpiresAt }},
		{"wrong_asset", func(c *AutomaticCandidateContext) { c.Route.AssetID = "other-asset" }},
		{"wrong_chain", func(c *AutomaticCandidateContext) { c.Route.ChainID = "other-chain" }},
		{"wrong_recipient", func(c *AutomaticCandidateContext) { c.Route.Address = "other-recipient" }},
		{"wrong_route_context", func(c *AutomaticCandidateContext) { c.Route.ID = "another-route" }},
		{"wrong_intent_context", func(c *AutomaticCandidateContext) { c.Intent.ID = "another-intent" }},
		{"zero_expected", func(c *AutomaticCandidateContext) { c.Route.ExpectedAmount = money.Zero() }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			event, candidates, contexts := sameCustomerCandidateFixture(t, "4813000000", "4812000000")
			c := contexts["farther"]
			tc.mutate(&c)
			contexts["farther"] = c
			if selected, ok := SelectAutomaticCandidate(event, candidates, contexts); ok {
				t.Fatalf("M2: unsafe authoritative context selected: %#v", selected)
			}
		})
	}
}

func TestSameCustomerCandidateRejectsUnsafeEvidence(t *testing.T) {
	for _, classification := range []ExceptionClass{ExceptionLate, ExceptionExact, ExceptionPartial, ExceptionUnderpaid, ExceptionWrongAsset, ExceptionAmbiguous, ExceptionUnmatched} {
		t.Run(string(classification), func(t *testing.T) {
			event, candidates, contexts := sameCustomerCandidateFixture(t, "4813000000", "4812000000")
			candidates[1].Class = classification
			if selected, ok := SelectAutomaticCandidate(event, candidates, contexts); ok {
				t.Fatalf("M2: unsupported tied classification selected: %#v", selected)
			}
		})
	}
	for _, name := range []string{"no_context", "missing_context", "low_score", "no_primary_window", "large_excess", "not_overpaid", "metadata_float_rounding"} {
		t.Run(name, func(t *testing.T) {
			event, candidates, contexts := sameCustomerCandidateFixture(t, "4813000000", "4812000000")
			switch name {
			case "no_context":
				contexts = nil
			case "missing_context":
				delete(contexts, "farther")
			case "low_score":
				for n := range candidates {
					candidates[n].Score = 74
				}
			case "no_primary_window":
				candidates[1].Reasons = []string{"within_automatic_30_minute_grace", "overpayment_within_five_percent"}
			case "large_excess":
				event.Amount = money.MustParse("6000000000")
			case "not_overpaid":
				event.Amount = money.MustParse("4800000000")
			case "metadata_float_rounding":
				c := contexts["closer"]
				c.Intent.Metadata = json.RawMessage(`{"order":9007199254740992}`)
				contexts["closer"] = c
				c = contexts["farther"]
				c.Intent.Metadata = json.RawMessage(`{"order":9007199254740993}`)
				contexts["farther"] = c
			}
			if selected, ok := SelectAutomaticCandidate(event, candidates, contexts); ok {
				t.Fatalf("M2: unsafe tied invoice selected: %#v", selected)
			}
		})
	}
}

func TestSameCustomerCandidatePreservesUniqueSeventyFive(t *testing.T) {
	candidates := []Candidate{{RouteID: "unique", Score: 75, Class: ExceptionOverpaid, Reasons: []string{"within_payment_window", "above_expected", "unique_route_candidate"}}, {RouteID: "old", Score: 40, Class: ExceptionLate}}
	selected, ok := SelectAutomaticCandidate(domain.TransferEvent{}, candidates, nil)
	if !ok || selected.RouteID != "unique" {
		t.Fatalf("M2: unique inclusive score75 path changed: %#v %v", selected, ok)
	}
}

func TestSameCustomerCandidateUsesExactMoneyAndInputIndependentOrdering(t *testing.T) {
	event, _, contexts := sameCustomerCandidateFixture(t, "4813000000", "4812000000")
	event.Amount = money.MustParse("100000000000000000000000000001")
	for id, c := range contexts {
		amount := "100000000000000000000000000000"
		if id == "farther" {
			amount = "99999999999999999999999999999"
		}
		c.Route.ExpectedAmount = money.MustParse(amount)
		contexts[id] = c
	}
	for _, order := range [][]string{{"closer", "farther"}, {"farther", "closer"}} {
		candidates := BuildCandidates(event, []domain.PaymentRoute{contexts[order[0]].Route, contexts[order[1]].Route}, event.OnChainTime)
		if selected, ok := SelectAutomaticCandidate(event, candidates, contexts); !ok || selected.RouteID != "closer" {
			t.Fatalf("M1: one-atomic difference above uint64 selected incorrectly: %#v %v", selected, ok)
		}
	}
}
