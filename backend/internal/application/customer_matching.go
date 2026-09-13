package application

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"

	"github.com/calmshv-star/ocrypt/backend/internal/domain"
)

const SameCustomerOverpaymentReason = "same_customer_closest_overpayment"

// UniqueCustomerOverpaymentCandidate resolves only a strictly closer small
// overpayment among contemporaneous invoices belonging to the same merchant
// customer and with identical fiat economics and merchant-supplied context.
// A shared receiving wallet is never treated as customer identity. Missing
// identity, equal amount distances, different customers/products/prices, late
// payments and large excesses retain the ordinary manual-review boundary.
func UniqueCustomerOverpaymentCandidate(candidates []Candidate, intents map[string]domain.PaymentIntent) (Candidate, bool) {
	if len(candidates) < 2 {
		return Candidate{}, false
	}
	first := candidates[0]
	if first.Score <= AutomaticCandidateScoreThreshold || first.Score != candidates[1].Score || compareCandidateAmountDistance(first, candidates[1]) >= 0 {
		return Candidate{}, false
	}
	owner, ok := intents[first.IntentID]
	if !ok || strings.TrimSpace(owner.CustomerReference) == "" || owner.TenantID == "" || owner.MerchantID == "" || owner.Currency == "" || owner.AmountMinor.IsZero() {
		return Candidate{}, false
	}
	for _, candidate := range candidates {
		if candidate.Score < first.Score {
			break
		}
		other, exists := intents[candidate.IntentID]
		if candidate.Score != first.Score || candidate.Class != ExceptionOverpaid || !candidateHasReason(candidate, "within_payment_window") || !candidateHasReason(candidate, "overpayment_within_five_percent") ||
			!exists || owner.TenantID != other.TenantID || owner.MerchantID != other.MerchantID || owner.CustomerReference != other.CustomerReference ||
			owner.Currency != other.Currency || owner.CurrencyScale != other.CurrencyScale || owner.AmountMinor.Cmp(other.AmountMinor) != 0 ||
			owner.Description != other.Description || !sameMatchingMetadata(owner.Metadata, other.Metadata) {
			return Candidate{}, false
		}
	}
	first.Reasons = append(append([]string(nil), first.Reasons...), SameCustomerOverpaymentReason)
	return first, true
}

func sameMatchingMetadata(left, right json.RawMessage) bool {
	var a, b any
	if len(left) == 0 {
		left = json.RawMessage(`{}`)
	}
	if len(right) == 0 {
		right = json.RawMessage(`{}`)
	}
	leftDecoder, rightDecoder := json.NewDecoder(bytes.NewReader(left)), json.NewDecoder(bytes.NewReader(right))
	leftDecoder.UseNumber()
	rightDecoder.UseNumber()
	return json.Valid(left) && json.Valid(right) && leftDecoder.Decode(&a) == nil && rightDecoder.Decode(&b) == nil && reflect.DeepEqual(a, b)
}
