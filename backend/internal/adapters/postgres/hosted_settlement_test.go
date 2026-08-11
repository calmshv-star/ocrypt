package postgres

import (
	"crypto/sha256"
	"testing"

	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/money"
)

func TestProviderRefundIncidentDecisionsNeverSilentlySettle(t *testing.T) {
	tests := []struct {
		name       string
		candidate  hostedSettlementCandidate
		payment    domain.VerifiedProviderPayment
		kind       string
		outOfOrder bool
	}{
		{"paid then refunded", hostedSettlementCandidate{ProviderStatus: "paid", IntentStatus: domain.IntentSettled}, domain.VerifiedProviderPayment{ProviderStatus: "refunded"}, "refund_after_settlement", false},
		{"refunded before payment", hostedSettlementCandidate{ProviderStatus: "pending", IntentStatus: domain.IntentPending}, domain.VerifiedProviderPayment{ProviderStatus: "refunded"}, "refund_before_settlement", false},
		{"payment after refund", hostedSettlementCandidate{ProviderStatus: "refunded", IntentStatus: domain.IntentPending}, domain.VerifiedProviderPayment{ProviderStatus: "paid"}, "paid_after_refund", true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			kind, outOfOrder := providerIncidentDecision(test.candidate, test.payment)
			if kind != test.kind || outOfOrder != test.outOfOrder {
				t.Fatalf("decision = %q/%v, want %q/%v", kind, outOfOrder, test.kind, test.outOfOrder)
			}
		})
	}
}

func TestPrebindEvidenceCannotChangeWhenOrderBecomesBound(t *testing.T) {
	payment := domain.VerifiedProviderPayment{
		ProviderReference: "provider-reference-1",
		ProviderStatus:    "paid",
		AssetID:           "usdt-tron",
		Amount:            money.MustParse("12340000"),
		AssetDecimals:     6,
		RawDigest:         sha256.Sum256([]byte("canonical callback")),
		SignatureDigest:   sha256.Sum256([]byte("verified signature")),
		ConfigManifestID:  "00000000-0000-0000-0000-000000000020",
		ConfigVersion:     1,
	}
	if !providerEvidenceMatches(payment, payment.ProviderReference, payment.ProviderStatus, payment.AssetID, payment.Amount.String(), payment.AssetDecimals, payment.RawDigest[:], payment.SignatureDigest[:], payment.ConfigManifestID, payment.ConfigVersion) {
		t.Fatal("identical pre-bind evidence was not replayable after binding")
	}
	tampered := sha256.Sum256([]byte("tampered callback"))
	if providerEvidenceMatches(payment, payment.ProviderReference, payment.ProviderStatus, payment.AssetID, payment.Amount.String(), payment.AssetDecimals, tampered[:], payment.SignatureDigest[:], payment.ConfigManifestID, payment.ConfigVersion) {
		t.Fatal("same provider event id with a different digest was accepted after binding")
	}
	if providerEvidenceMatches(payment, payment.ProviderReference, "refunded", payment.AssetID, payment.Amount.String(), payment.AssetDecimals, payment.RawDigest[:], payment.SignatureDigest[:], payment.ConfigManifestID, payment.ConfigVersion) {
		t.Fatal("same provider event id with different canonical facts was accepted after binding")
	}
}

func TestPausedProviderCallbackIsQuarantinedBeforeSettlement(t *testing.T) {
	if !providerCallbackShouldQuarantine(domain.VerifiedProviderPayment{ProviderPaused: true, ProviderStatus: "paid"}) {
		t.Fatal("paused paid callback was not quarantined")
	}
	if providerCallbackShouldQuarantine(domain.VerifiedProviderPayment{ProviderStatus: "paid"}) {
		t.Fatal("active paid callback was incorrectly quarantined")
	}
}
