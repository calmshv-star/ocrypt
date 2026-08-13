package httpapi

import (
	"testing"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/money"
)

func TestMerchantFacadeTreatsFinalizedOverpaymentAsPaid(t *testing.T) {
	now := time.Now().UTC()
	intent := domain.PaymentIntent{
		ID: "payment", MerchantOrderID: "order", AmountMinor: money.MustParse("49900"), Currency: "RUB", CurrencyScale: 2,
		Status: domain.IntentOverpaid, StatusReason: "deterministic_overpayment_policy", ExpiresAt: now.Add(time.Hour), UpdatedAt: now, Version: 4,
		Routes: []domain.PaymentRoute{{ID: "route", ChainID: "eip155:1", AssetID: "eth-ethereum", Address: "0xmerchant", DisplayAmount: "0.0031", ExpectedAmount: money.MustParse("3100000000000000"), ReceivedAmount: "0.0032", ExcessAmount: "0.0001", RequiredFinality: 12}},
	}

	response := (&Server{}).merchantResponse(intent, "")
	if response.Status != string(domain.IntentSettled) || response.Payment == nil || response.Payment.ExcessAmount != "0.0001" || response.StatusReason != intent.StatusReason {
		t.Fatalf("overpayment fulfillment response lost paid or reconciliation evidence: %+v", response)
	}
}
