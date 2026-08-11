package application

import (
	"context"
	"testing"

	"github.com/calmshv-star/ocrypt/backend/internal/domain"
)

type aiStoreFixture struct {
	Store
	candidates []domain.MatchCandidate
	recorded   int
}

func (s *aiStoreFixture) GetCandidates(context.Context, Principal, string) ([]domain.MatchCandidate, error) {
	return s.candidates, nil
}

func (s *aiStoreFixture) RecordAIRank(context.Context, Principal, string, int64, AIRankResult, string, string) error {
	s.recorded++
	return nil
}

type rankerFixture struct{ result AIRankResult }

func (r rankerFixture) Rank(context.Context, AIRankRequest) (AIRankResult, error) {
	return r.result, nil
}
func (rankerFixture) ModelName() string    { return "fixture-model" }
func (rankerFixture) EndpointHost() string { return "ai.example.test" }

func TestRankUnmatchedPersistsAdvisoryWithoutFinancialMutation(t *testing.T) {
	unmatchedID := "018f22b0-4db4-7c58-8f18-4d2f9d7b6a11"
	routeID := "018f22b0-4db4-7c58-8f18-4d2f9d7b6a12"
	store := &aiStoreFixture{candidates: []domain.MatchCandidate{{RouteID: routeID, Score: 85, CandidateSetVersion: 1, Evidence: map[string]any{"class": "late", "amount_delta": "0", "reason_codes": []any{"same_recipient"}}}}}
	service := New(store)
	principal := Principal{ActorID: "018f22b0-4db4-7c58-8f18-4d2f9d7b6a13", Scopes: map[string]bool{"operations:write": true}}
	result, err := service.RankUnmatched(t.Context(), principal, unmatchedID, rankerFixture{AIRankResult{RecommendedRouteID: routeID, Confidence: .82, ReasonCodes: []string{"same_recipient"}, ReviewRequired: true}})
	if err != nil {
		t.Fatal(err)
	}
	if result.RecommendedRouteID != routeID || store.recorded != 1 {
		t.Fatalf("result=%+v recorded=%d", result, store.recorded)
	}

	_, err = service.RankUnmatched(t.Context(), principal, unmatchedID, rankerFixture{AIRankResult{RecommendedRouteID: routeID, Confidence: .82, ReviewRequired: false}})
	if err == nil || store.recorded != 1 {
		t.Fatalf("unsafe result was not rejected: err=%v recorded=%d", err, store.recorded)
	}
}
