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
func TestAIResultCannotAuthorizeOrInventCandidate(t *testing.T) {
	request := AIRankRequest{CaseID: "case", Candidates: []Candidate{{RouteID: "route"}}}
	if err := ValidateAIResult(request, AIRankResult{RecommendedRouteID: "route", Confidence: .9, ReviewRequired: false}); err == nil {
		t.Fatal("AI bypassed human review")
	}
	if err := ValidateAIResult(request, AIRankResult{RecommendedRouteID: "invented", Confidence: .9, ReviewRequired: true}); err == nil {
		t.Fatal("AI invented a candidate")
	}
}
