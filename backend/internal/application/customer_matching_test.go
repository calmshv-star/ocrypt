package application

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/money"
)

func customerOverpaymentFixture() ([]domain.PaymentRoute, domain.TransferEvent, map[string]domain.PaymentIntent) {
	now := time.Date(2026, 9, 13, 9, 27, 30, 0, time.UTC)
	routes := []domain.PaymentRoute{automatedRoute(now), automatedRoute(now)}
	intents := map[string]domain.PaymentIntent{}
	for idx, id := range []string{"older", "closer"} {
		routes[idx].ID = id
		routes[idx].IntentID = "intent-" + id
		routes[idx].ExpectedAmount = money.MustParse([]string{"5930000", "5940000"}[idx])
		intents[routes[idx].IntentID] = domain.PaymentIntent{TenantID: "tenant", MerchantID: "merchant", CustomerReference: "customer", AmountMinor: money.MustParse("49900"), Currency: "RUB", CurrencyScale: 2, Metadata: json.RawMessage(`{}`)}
	}
	return routes, automatedEvent("payment", "5950000", now), intents
}

func TestSameCustomerOverpaymentSelectsClosestAndPreservesExcess(t *testing.T) {
	routes, event, intents := customerOverpaymentFixture()
	for _, network := range []string{"tron:mainnet", "solana:mainnet", "eip155:1", "ton:mainnet"} {
		t.Run(network, func(t *testing.T) {
			for i := range routes {
				routes[i].ChainID = network
			}
			event.Identity.ChainID = network
			candidates := BuildCandidates(event, routes, event.OnChainTime.Add(time.Minute))
			if _, ok := UniqueAutomaticCandidate(candidates); ok {
				t.Fatal("fixture no longer reproduces score tie")
			}
			selected, ok := UniqueCustomerOverpaymentCandidate(candidates, intents)
			if !ok || selected.RouteID != "closer" || selected.Score != 100 {
				t.Fatalf("wrong selection: %#v %v", selected, ok)
			}
			policy := automatedPolicy()
			policy.OverpaymentMode = OverpaymentCreditExpected
			decision, err := EvaluateAutomatedMatch(routes[1], []domain.TransferEvent{event, event}, event.OnChainTime.Add(time.Minute), policy)
			if err != nil || decision.Outcome != AutomatedSettle || decision.Received.String() != "5950000" || decision.Credited.String() != "5940000" || len(decision.Allocations) != 1 {
				t.Fatalf("wrong settlement/replay accounting: %#v %v", decision, err)
			}
		})
	}
}

func TestSameCustomerOverpaymentKeepsAmbiguousAndUnrelatedPaymentsInReview(t *testing.T) {
	for _, name := range []string{"different_customer", "missing_customer", "missing_intent", "different_merchant", "different_tenant", "different_fiat_amount", "different_currency", "different_scale", "different_product", "different_metadata", "large_metadata_ids", "equal_distance", "late", "underpaid", "large_overpaid", "third_customer"} {
		t.Run(name, func(t *testing.T) {
			routes, event, intents := customerOverpaymentFixture()
			other := intents["intent-older"]
			switch name {
			case "different_customer":
				other.CustomerReference = "another"
			case "missing_customer":
				other.CustomerReference = ""
			case "different_merchant":
				other.MerchantID = "another"
			case "different_tenant":
				other.TenantID = "another"
			case "different_fiat_amount":
				other.AmountMinor = money.MustParse("299400")
			case "different_currency":
				other.Currency = "USD"
			case "different_scale":
				other.CurrencyScale = 0
			case "different_product":
				other.Description = "VPN"
			case "different_metadata":
				other.Metadata = json.RawMessage(`{"product":"vpn"}`)
			case "large_metadata_ids":
				other.Metadata = json.RawMessage(`{"product":9007199254740993}`)
				a := intents["intent-closer"]
				a.Metadata = json.RawMessage(`{"product":9007199254740992}`)
				intents["intent-closer"] = a
			case "equal_distance":
				routes[0].ExpectedAmount = routes[1].ExpectedAmount
			case "late":
				event.OnChainTime = routes[0].ExpiresAt.Add(time.Minute)
			case "underpaid":
				event.Amount = money.MustParse("5920000")
			case "large_overpaid":
				event.Amount = money.MustParse("7000000")
			case "third_customer":
				third := routes[0]
				third.ID = "third"
				third.IntentID = "third-intent"
				third.ExpectedAmount = money.MustParse("5920000")
				routes = append(routes, third)
				thirdIntent := other
				thirdIntent.CustomerReference = "another"
				intents[third.IntentID] = thirdIntent
			}
			intents["intent-older"] = other
			if name == "missing_intent" {
				delete(intents, "intent-older")
			}
			candidates := BuildCandidates(event, routes, event.OnChainTime.Add(time.Minute))
			if selected, ok := UniqueCustomerOverpaymentCandidate(candidates, intents); ok {
				t.Fatalf("unsafe automatic selection: %#v", selected)
			}
		})
	}
}
