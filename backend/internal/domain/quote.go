package domain

import (
	"github.com/calmshv-star/ocrypt/backend/internal/money"
	"time"
)

type RateQuote struct {
	ID                 string       `json:"id"`
	TenantID           string       `json:"-"`
	MerchantID         string       `json:"merchant_id"`
	PaymentIntentID    string       `json:"payment_intent_id"`
	FiatAmountMinor    money.Amount `json:"fiat_amount_minor"`
	FiatCurrency       string       `json:"fiat_currency"`
	FiatScale          uint8        `json:"fiat_scale"`
	AssetID            string       `json:"asset_id"`
	CryptoAmountAtomic money.Amount `json:"crypto_amount_atomic"`
	RateNumerator      money.Amount `json:"rate_numerator"`
	RateDenominator    money.Amount `json:"rate_denominator"`
	SpreadBPS          uint32       `json:"spread_bps"`
	Source             string       `json:"source"`
	PolicyVersion      int64        `json:"policy_version"`
	IssuedAt           time.Time    `json:"issued_at"`
	ExpiresAt          time.Time    `json:"expires_at"`
}

type AddressAssignment struct {
	ID               string    `json:"id"`
	AddressID        string    `json:"address_id"`
	ChainID          string    `json:"chain_id"`
	CanonicalAddress string    `json:"canonical_address"`
	DisplayAddress   string    `json:"display_address"`
	Memo             string    `json:"memo,omitempty"`
	AssignedUntil    time.Time `json:"assigned_until"`
	LeaseToken       string    `json:"-"`
}
