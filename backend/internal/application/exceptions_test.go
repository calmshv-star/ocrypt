package application

import (
	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/money"
	"testing"
	"time"
)

func TestCandidatePolicyClassifiesWithoutGuessing(t *testing.T) {
	now := time.Now().UTC()
	route := domain.PaymentRoute{ID: "r", IntentID: "i", ChainID: "tron", AssetID: "usdt", Address: "to", ExpectedAmount: money.MustParse("100"), Status: domain.RouteActive, StartsAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour), GraceEndsAt: now.Add(24 * time.Hour)}
	event := domain.TransferEvent{Identity: domain.EventIdentity{ChainID: "tron", AssetID: "usdt", ToAddress: "to"}, Amount: money.MustParse("90"), OnChainTime: now}
	if got := BuildCandidates(event, []domain.PaymentRoute{route}, now)[0].Class; got != ExceptionPartial {
		t.Fatalf("got %s", got)
	}
	event.Amount = money.MustParse("110")
	if got := BuildCandidates(event, []domain.PaymentRoute{route}, now)[0].Class; got != ExceptionOverpaid {
		t.Fatalf("got %s", got)
	}
	event.Identity.AssetID = "trx"
	if got := BuildCandidates(event, []domain.PaymentRoute{route}, now)[0].Class; got != ExceptionWrongAsset {
		t.Fatalf("got %s", got)
	}
}

func TestCandidatePolicyPrefersRouteOwningReusedAddressAtPaymentTime(t *testing.T) {
	now := time.Date(2026, 8, 13, 4, 27, 0, 0, time.UTC)
	eventTime := time.Date(2026, 8, 13, 4, 9, 35, 0, time.UTC)
	old := domain.PaymentRoute{ID: "old-route", IntentID: "old-intent", ChainID: "eip155:1", AssetID: "eth-ethereum", Address: "0xmerchant", ExpectedAmount: money.MustParse("3207122163051090"), Status: domain.RouteExpired, StartsAt: eventTime.Add(-8 * time.Hour), ExpiresAt: eventTime.Add(-7 * time.Hour), GraceEndsAt: eventTime.Add(time.Hour)}
	current := domain.PaymentRoute{ID: "current-route", IntentID: "current-intent", ChainID: "eip155:1", AssetID: "eth-ethereum", Address: "0xmerchant", ExpectedAmount: money.MustParse("3197807894399590"), Status: domain.RouteActive, StartsAt: eventTime.Add(-5 * time.Minute), ExpiresAt: eventTime.Add(25 * time.Minute), GraceEndsAt: eventTime.Add(24 * time.Hour)}
	event := domain.TransferEvent{Identity: domain.EventIdentity{ChainID: "eip155:1", AssetID: "eth-ethereum", ToAddress: "0xmerchant"}, Amount: money.MustParse("3263350000000000"), OnChainTime: eventTime}

	candidates := BuildCandidates(event, []domain.PaymentRoute{old, current}, now)
	if len(candidates) != 2 || candidates[0].RouteID != current.ID || candidates[0].Class != ExceptionOverpaid {
		t.Fatalf("current address lease did not outrank an expired route: %#v", candidates)
	}
	if candidates[0].Score <= candidates[1].Score {
		t.Fatalf("current route score %d must exceed old route score %d", candidates[0].Score, candidates[1].Score)
	}
}

func TestUniqueAutomaticCandidateRequiresStrictlyAboveNinetyAndNoTie(t *testing.T) {
	if _, ok := UniqueAutomaticCandidate([]Candidate{{RouteID: "ninety", Score: 90}}); ok {
		t.Fatal("score equal to ninety was auto-matched")
	}
	selected, ok := UniqueAutomaticCandidate([]Candidate{{RouteID: "winner", Score: 91}, {RouteID: "other", Score: 90}})
	if !ok || selected.RouteID != "winner" {
		t.Fatalf("unique score above ninety was not selected: %#v %v", selected, ok)
	}
	if _, ok := UniqueAutomaticCandidate([]Candidate{{RouteID: "a", Score: 95}, {RouteID: "b", Score: 95}}); ok {
		t.Fatal("equal high-score candidates were auto-matched")
	}
}

func TestLateCandidateCrossesAutoThresholdOnlyInsideThirtyMinutes(t *testing.T) {
	expires := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	route := domain.PaymentRoute{ID: "route", IntentID: "intent", ChainID: "eip155:1", AssetID: "eth-ethereum", Address: "merchant", ExpectedAmount: money.MustParse("100"), StartsAt: expires.Add(-30 * time.Minute), ExpiresAt: expires, GraceEndsAt: expires.Add(24 * time.Hour)}

	inside := automatedEvent("inside-grace", "100", expires.Add(30*time.Minute))
	inside.Identity.ChainID, inside.Identity.AssetID, inside.Identity.ToAddress = route.ChainID, route.AssetID, route.Address
	insideCandidates := BuildCandidates(inside, []domain.PaymentRoute{route}, inside.OnChainTime)
	if selected, ok := UniqueAutomaticCandidate(insideCandidates); !ok || selected.Score <= AutomaticCandidateScoreThreshold {
		t.Fatalf("thirty-minute late candidate did not cross threshold: %#v", insideCandidates)
	}

	outside := inside
	outside.ID, outside.OnChainTime = "outside-grace", expires.Add(30*time.Minute+time.Second)
	outside.Identity.TransactionID = "tx-outside-grace"
	outsideCandidates := BuildCandidates(outside, []domain.PaymentRoute{route}, outside.OnChainTime)
	if _, ok := UniqueAutomaticCandidate(outsideCandidates); ok {
		t.Fatalf("candidate after thirty minutes was auto-matched: %#v", outsideCandidates)
	}
}

func TestAIResultCannotAuthorizeOrInventCandidate(t *testing.T) {
	request := AIRankRequest{CaseID: "case", Candidates: []Candidate{{RouteID: "route"}}}
	if err := ValidateAIResult(request, AIRankResult{RecommendedRouteID: "route", Confidence: .9, ReviewRequired: false}); err == nil {
		t.Fatal("AI bypassed human review")
	}
	if err := ValidateAIResult(request, AIRankResult{RecommendedRouteID: "invented", Confidence: .9, ReviewRequired: true}); err == nil {
		t.Fatal("AI invented a candidate")
	}
}
