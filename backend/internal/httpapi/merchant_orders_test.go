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

func TestMerchantFacadeKeepsPayerSelectedETHRouteWithFallbacks(t *testing.T) {
	now := time.Now().UTC()
	intent := domain.PaymentIntent{
		ID: "payment", MerchantOrderID: "order", AmountMinor: money.MustParse("49900"), Currency: "RUB", CurrencyScale: 2,
		Status: domain.IntentPending, ExpiresAt: now.Add(time.Hour), UpdatedAt: now,
		Routes: []domain.PaymentRoute{
			{ID: "arbitrum-fallback", ChainID: "eip155:42161", AssetID: "eth-arbitrum", DisplayAmount: "0.002174"},
			{ID: "ethereum-selected", QuoteID: "quote", AddressAssignmentID: "assignment", ChainID: "eip155:1", AssetID: "eth-ethereum", Address: "0x2222222222222222222222222222222222222222", DisplayAmount: "0.002174"},
			{ID: "base-fallback", ChainID: "eip155:8453", AssetID: "eth-base", DisplayAmount: "0.002174"},
		},
	}
	response := (&Server{}).merchantResponse(intent, "")
	if response.Payment == nil || response.Payment.RouteID != "ethereum-selected" || response.Payment.Network != "eip155:1" {
		t.Fatalf("payer-selected route missing from merchant response: %+v", response.Payment)
	}
}
