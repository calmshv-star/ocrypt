package application

import (
	"testing"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/money"
)

func TestExactMatcherIncludesEventAssetAddressAmountAndWindow(t *testing.T) {
	now := time.Now().UTC()
	event := domain.TransferEvent{Identity: domain.EventIdentity{ChainID: "tron:mainnet", AssetID: "usdt-tron", ToAddress: "TReceiver"}, Amount: money.MustParse("10000001"), OnChainTime: now}
	route := domain.PaymentRoute{ID: "route-1", ChainID: "tron:mainnet", AssetID: "usdt-tron", Address: "TReceiver", ExpectedAmount: money.MustParse("10000001"), Status: domain.RouteActive, StartsAt: now.Add(-time.Minute), GraceEndsAt: now.Add(time.Minute)}
	selected, decision := SelectExactRoute(event, []domain.PaymentRoute{route})
	if decision != MatchExact || selected == nil || selected.ID != "route-1" {
		t.Fatalf("unexpected decision: %s %+v", decision, selected)
	}
	event.Amount = money.MustParse("10000000")
	if _, decision := SelectExactRoute(event, []domain.PaymentRoute{route}); decision != MatchNone {
		t.Fatalf("underpayment matched exactly: %s", decision)
	}
}
func TestExactMatcherFailsClosedOnAmbiguity(t *testing.T) {
	now := time.Now().UTC()
	event := domain.TransferEvent{Identity: domain.EventIdentity{ChainID: "eip155:1", AssetID: "usdc-ethereum", ToAddress: "0xabc"}, Amount: money.MustParse("100"), OnChainTime: now}
	route := domain.PaymentRoute{ChainID: event.Identity.ChainID, AssetID: event.Identity.AssetID, Address: event.Identity.ToAddress, ExpectedAmount: event.Amount, Status: domain.RouteActive, StartsAt: now.Add(-time.Minute), GraceEndsAt: now.Add(time.Minute)}
	if selected, decision := SelectExactRoute(event, []domain.PaymentRoute{route, route}); decision != MatchAmbiguous || selected != nil {
		t.Fatalf("ambiguous transfer did not fail closed: %s", decision)
	}
}
