package application

import (
	"bytes"
	"encoding/json"
	"io"
	"sort"
	"strings"

	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/money"
)

// AutomaticCandidateContext must come from locked merchant-authenticated records,
// not from scanner evidence or the transfer's sender address.
type AutomaticCandidateContext struct {
	Route  domain.PaymentRoute
	Intent domain.PaymentIntent
}

// SelectAutomaticCandidate preserves unique selection, but can resolve a tied
// small overpayment only when every highest-scored invoice is equivalent and one
// has a strictly smaller exact distance. Equal distances never use recency.
func SelectAutomaticCandidate(event domain.TransferEvent, candidates []Candidate, contexts map[string]AutomaticCandidateContext) (Candidate, bool) {
	ranked := append([]Candidate(nil), candidates...)
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].Score > ranked[j].Score })
	if winner, ok := UniqueAutomaticCandidate(ranked); ok {
		return winner, true
	}
	if len(ranked) < 2 || ranked[0].Score != 100 || ranked[1].Score != 100 {
		return Candidate{}, false
	}
	var reference AutomaticCandidateContext
	var referenceMetadata []byte
	var winner Candidate
	var closest money.Amount
	uniqueClosest := false
	seen := make(map[string]bool)
	for _, candidate := range ranked {
		if candidate.Score != ranked[0].Score {
			break
		}
		context, exists := contexts[candidate.RouteID]
		route, intent := context.Route, context.Intent
		if !exists || seen[candidate.RouteID] || candidate.Class != ExceptionOverpaid ||
			candidateHasReason(candidate, "candidate_context_truncated") ||
			!candidateHasReason(candidate, "within_payment_window") ||
			!candidateHasReason(candidate, "overpayment_within_five_percent") ||
			route.ID == "" || route.ID != candidate.RouteID || route.IntentID == "" ||
			route.IntentID != candidate.IntentID || intent.ID != route.IntentID ||
			intent.TenantID == "" || intent.MerchantID == "" || strings.TrimSpace(intent.CustomerReference) == "" ||
			intent.AmountMinor.IsZero() || domain.ValidateCurrency(intent.Currency, intent.CurrencyScale) != nil ||
			(route.Status != domain.RouteActive && route.Status != domain.RouteExpired) ||
			route.ChainID != event.Identity.ChainID || route.AssetID != event.Identity.AssetID ||
			route.Address != event.Identity.ToAddress || route.AssetDecimals != event.AssetDecimals ||
			event.OnChainTime.Before(route.StartsAt) || event.OnChainTime.After(route.ExpiresAt) ||
			route.ExpectedAmount.IsZero() || !overpaymentWithinTolerance(event.Amount, route.ExpectedAmount, candidateCloseAmountToleranceBPS) {
			return Candidate{}, false
		}
		seen[candidate.RouteID] = true
		metadata, valid := canonicalCandidateMetadata(intent.Metadata)
		if !valid {
			return Candidate{}, false
		}
		if len(seen) == 1 {
			reference, referenceMetadata = context, metadata
		} else if intent.TenantID != reference.Intent.TenantID || intent.MerchantID != reference.Intent.MerchantID ||
			intent.CustomerReference != reference.Intent.CustomerReference || intent.AmountMinor.Cmp(reference.Intent.AmountMinor) != 0 ||
			intent.Currency != reference.Intent.Currency || intent.CurrencyScale != reference.Intent.CurrencyScale ||
			!bytes.Equal(metadata, referenceMetadata) || route.Memo != reference.Route.Memo {
			return Candidate{}, false
		}
		distance, err := event.Amount.Sub(route.ExpectedAmount)
		if err != nil {
			return Candidate{}, false
		}
		if winner.RouteID == "" || distance.Cmp(closest) < 0 {
			winner, closest, uniqueClosest = candidate, distance, true
		} else if distance.Cmp(closest) == 0 {
			uniqueClosest = false
		}
	}
	return winner, uniqueClosest
}

// UseNumber prevents distinct large JSON integers from collapsing through
// float64. Marshal canonicalizes object-key order and insignificant whitespace;
// different numeric spellings conservatively remain different metadata.
func canonicalCandidateMetadata(raw json.RawMessage) ([]byte, bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil || value == nil {
		return nil, false
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return nil, false
	}
	canonical, err := json.Marshal(value)
	return canonical, err == nil
}
