package application

import (
	"testing"

	"github.com/calmshv-star/ocrypt/backend/internal/domain"
)

func TestSameCustomerCandidateRejectsTruncatedContenderContext(t *testing.T) {
	event, candidates, contexts := sameCustomerCandidateFixture(t, "4813000000", "4812000000")
	// The hidden 101st route could belong to another authenticated customer.
	// Its persisted sentinel must veto ties even if all retained contexts agree.
	for index := range candidates {
		candidates[index].Reasons = append(candidates[index].Reasons, "candidate_context_truncated")
	}
	if winner, ok := SelectAutomaticCandidate(event, candidates, contexts); ok {
		t.Fatalf("truncated contender set selected an automatic tie owner: %#v", winner)
	}
	unique := []Candidate{{RouteID: "unique", Score: 75, Class: ExceptionOverpaid, Reasons: []string{"within_payment_window", "above_expected", "unique_route_candidate", "candidate_context_truncated"}}}
	if winner, ok := SelectAutomaticCandidate(domain.TransferEvent{}, unique, nil); !ok || winner.RouteID != "unique" {
		t.Fatalf("saturation changed the existing unique score75 path: %#v %v", winner, ok)
	}
}

func TestSameCustomerCandidateRejectsChangedPrecisionOrMemo(t *testing.T) {
	for _, name := range []string{"asset_precision", "memo"} {
		t.Run(name, func(t *testing.T) {
			event, candidates, contexts := sameCustomerCandidateFixture(t, "4813000000", "4812000000")
			context := contexts["farther"]
			if name == "asset_precision" {
				context.Route.AssetDecimals++
			} else {
				context.Route.Memo = "different-memo"
			}
			contexts["farther"] = context
			if winner, ok := SelectAutomaticCandidate(event, candidates, contexts); ok {
				t.Fatalf("different %s selected an automatic tie owner: %#v", name, winner)
			}
		})
	}
}
