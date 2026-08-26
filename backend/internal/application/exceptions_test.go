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

func TestUniqueAutomaticCandidateRequiresStrictlyAboveEightyAndNoTie(t *testing.T) {
	if _, ok := UniqueAutomaticCandidate([]Candidate{{RouteID: "eighty", Score: 80}}); ok {
		t.Fatal("score equal to eighty was auto-matched")
	}
	selected, ok := UniqueAutomaticCandidate([]Candidate{{RouteID: "winner", Score: 81}, {RouteID: "other", Score: 80}})
	if !ok || selected.RouteID != "winner" {
		t.Fatalf("unique score above eighty was not selected: %#v %v", selected, ok)
	}
	if _, ok := UniqueAutomaticCandidate([]Candidate{{RouteID: "a", Score: 95}, {RouteID: "b", Score: 95}}); ok {
		t.Fatal("equal high-score candidates were auto-matched")
	}
	if _, ok := UniqueAutomaticCandidate([]Candidate{{RouteID: "late", Score: 100, Class: ExceptionLate}}); ok {
		t.Fatal("late candidate outside the automatic grace window was auto-matched")
	}
}

func TestCandidateRankingPrefersTheCurrentCloseAmountOverUnrelatedOpenOrders(t *testing.T) {
	paidAt := time.Date(2026, 8, 22, 7, 45, 36, 0, time.UTC)
	processedAt := paidAt.Add(57 * time.Second)
	route := func(id, expected string, startsAt, expiresAt time.Time) domain.PaymentRoute {
		return domain.PaymentRoute{
			ID: id, IntentID: "intent-" + id, ChainID: "tron:mainnet", AssetID: "usdt-tron",
			Address: "merchant", ExpectedAmount: money.MustParse(expected), Status: domain.RouteActive,
			StartsAt: startsAt, ExpiresAt: expiresAt, GraceEndsAt: expiresAt.Add(24 * time.Hour),
		}
	}
	routes := []domain.PaymentRoute{
		route("large", "36238911", paidAt.Add(-24*time.Minute), paidAt.Add(6*time.Minute)),
		route("medium", "14512510", paidAt.Add(-23*time.Minute), paidAt.Add(7*time.Minute)),
		route("current", "8460589", paidAt.Add(-4*time.Minute), paidAt.Add(26*time.Minute)),
		route("just-expired", "8460588", paidAt.Add(-29*time.Minute), paidAt.Add(24*time.Second)),
	}
	event := domain.TransferEvent{
		Identity: domain.EventIdentity{ChainID: "tron:mainnet", AssetID: "usdt-tron", ToAddress: "merchant"},
		Amount:   money.MustParse("8460000"), OnChainTime: paidAt,
	}

	candidates := BuildCandidates(event, routes, processedAt)
	if len(candidates) != 4 || candidates[0].RouteID != "current" {
		t.Fatalf("current 699 RUB order was not ranked first: %#v", candidates)
	}
	if candidates[0].Score <= AutomaticCandidateScoreThreshold || candidates[0].Class == ExceptionAmbiguous {
		t.Fatalf("close current order did not cross the automatic threshold: %#v", candidates[0])
	}
	if candidates[2].RouteID != "medium" || candidates[2].Score > AutomaticCandidateScoreThreshold || candidates[3].RouteID != "large" || candidates[3].Score > AutomaticCandidateScoreThreshold {
		t.Fatalf("unrelated partial orders remained high-confidence candidates: %#v", candidates)
	}
	selected, ok := UniqueAutomaticCandidate(candidates)
	if !ok || selected.RouteID != "current" {
		t.Fatalf("current close payment was not selected automatically: %#v %v", selected, ok)
	}
}

func TestCandidateRankingDoesNotGiveUnrelatedSmallInvoiceFullOverpaymentScore(t *testing.T) {
	paidAt := time.Date(2026, 8, 26, 19, 42, 54, 0, time.UTC)
	route := func(id, expected string, startsAt time.Time) domain.PaymentRoute {
		return domain.PaymentRoute{
			ID: id, IntentID: "intent-" + id, ChainID: "tron:mainnet", AssetID: "usdt-tron",
			Address: "TSW3ZVUt5jjuyiVgppBduZCtQeCKzR5Dv4", ExpectedAmount: money.MustParse(expected),
			Status: domain.RouteActive, StartsAt: startsAt, ExpiresAt: startsAt.Add(30 * time.Minute),
			GraceEndsAt: startsAt.Add(24*time.Hour + 30*time.Minute),
		}
	}
	intended := route("intended-2994-rub", "35500000", paidAt.Add(-71*time.Second))
	unrelated := route("unrelated-499-rub", "5940000", paidAt.Add(-18*time.Minute))
	event := domain.TransferEvent{
		Identity: domain.EventIdentity{ChainID: "tron:mainnet", AssetID: "usdt-tron", ToAddress: intended.Address},
		Amount:   money.MustParse("35501836"), OnChainTime: paidAt,
	}

	candidates := BuildCandidates(event, []domain.PaymentRoute{unrelated, intended}, paidAt.Add(time.Minute))
	if len(candidates) != 2 || candidates[0].RouteID != intended.ID {
		t.Fatalf("near-exact 2994 RUB order was not ranked first: %#v", candidates)
	}
	if candidates[0].Score != 100 || candidates[0].Class != ExceptionOverpaid {
		t.Fatalf("near-exact overpayment should score 100 without ambiguity: %#v", candidates[0])
	}
	if candidates[1].Score != 85 || candidates[1].Class != ExceptionOverpaid {
		t.Fatalf("unrelated 499 RUB order received a full-confidence score: %#v", candidates[1])
	}
	selected, ok := UniqueAutomaticCandidate(candidates)
	if !ok || selected.RouteID != intended.ID {
		t.Fatalf("near-exact order was not selected automatically: %#v %v", selected, ok)
	}
}

func TestCandidateRankingUsesAmountDistanceBeforeRouteIDForEqualScores(t *testing.T) {
	now := time.Date(2026, 8, 26, 20, 0, 0, 0, time.UTC)
	route := func(id, expected string) domain.PaymentRoute {
		return domain.PaymentRoute{
			ID: id, IntentID: "intent-" + id, ChainID: "tron:mainnet", AssetID: "usdt-tron", Address: "merchant",
			ExpectedAmount: money.MustParse(expected), Status: domain.RouteActive, StartsAt: now.Add(-time.Minute),
			ExpiresAt: now.Add(29 * time.Minute), GraceEndsAt: now.Add(24 * time.Hour),
		}
	}
	event := domain.TransferEvent{
		Identity: domain.EventIdentity{ChainID: "tron:mainnet", AssetID: "usdt-tron", ToAddress: "merchant"},
		Amount:   money.MustParse("1000100"), OnChainTime: now,
	}

	candidates := BuildCandidates(event, []domain.PaymentRoute{route("a-farther", "1000000"), route("z-closer", "1000099")}, now)
	if candidates[0].RouteID != "z-closer" || candidates[0].Class == ExceptionAmbiguous {
		t.Fatalf("closer amount did not break equal-score tie safely: %#v", candidates)
	}
}

func TestLargeOverpaymentStillAutoMatchesWhenRouteIsUnique(t *testing.T) {
	now := time.Date(2026, 8, 26, 20, 0, 0, 0, time.UTC)
	route := domain.PaymentRoute{
		ID: "only-route", IntentID: "only-intent", ChainID: "tron:mainnet", AssetID: "usdt-tron", Address: "merchant",
		ExpectedAmount: money.MustParse("100"), Status: domain.RouteActive, StartsAt: now.Add(-time.Minute),
		ExpiresAt: now.Add(29 * time.Minute), GraceEndsAt: now.Add(24 * time.Hour),
	}
	event := domain.TransferEvent{
		Identity: domain.EventIdentity{ChainID: route.ChainID, AssetID: route.AssetID, ToAddress: route.Address},
		Amount:   money.MustParse("1000000"), OnChainTime: now,
	}

	candidates := BuildCandidates(event, []domain.PaymentRoute{route}, now)
	selected, ok := UniqueAutomaticCandidate(candidates)
	if !ok || selected.RouteID != route.ID || selected.Score != 85 {
		t.Fatalf("unique large overpayment should remain automatic: %#v %v", selected, ok)
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
