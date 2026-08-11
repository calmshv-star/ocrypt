package domain

import (
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/money"
)

// HostedProviderConfig is non-secret admission metadata. CredentialRef and
// CallbackSecretRef are opaque names resolved by a secret-bound runtime; the
// database never stores provider credentials.
type HostedProviderConfig struct {
	ID                    string
	TenantID              string
	MerchantID            string
	AdapterKind           string
	APIOrigin             string
	CreatePath            string
	CancelPath            string
	StatusPath            string
	RefundPath            string
	ReconcilePath         string
	PaymentURLOrigins     []string
	CredentialRef         string
	APIKeyID              string
	CallbackSecretRef     string
	CallbackKeyID         string
	CallbackSignatureKind string
	AssetID               string
	AssetDecimals         uint8
	Currency              string
	Status                string
	ConfigManifestID      string
	ConfigVersion         int64
}

type HostedCreateRequest struct {
	ProviderID      string
	IntentID        string
	IdempotencyKey  string
	RequestHash     string
	AssetID         string
	FiatAmountMinor money.Amount
	Currency        string
	CurrencyScale   uint8
	ExpiresAt       time.Time
}

type HostedCreateResult struct {
	ProviderOrderID    string
	ProviderReference  string
	PaymentURL         string
	AssetID            string
	Amount             money.Amount
	AssetDecimals      uint8
	QuoteID            string
	RateNumerator      money.Amount
	RateDenominator    money.Amount
	QuoteIssuedAt      time.Time
	RawResponse        []byte
	ResponseDigest     [32]byte
	ResponseReceivedAt time.Time
	ExpiresAt          time.Time
}

type HostedCreateFence struct {
	Claimed   bool
	Completed bool
	Result    HostedCreateResult
}

type VerifiedProviderPayment struct {
	TenantID          string
	MerchantID        string
	ProviderID        string
	ProviderOrderID   string
	ProviderReference string
	ProviderEventID   string
	ProviderStatus    string
	AssetID           string
	Amount            money.Amount
	AssetDecimals     uint8
	OccurredAt        time.Time
	ReceivedAt        time.Time
	RawBody           []byte
	RawDigest         [32]byte
	SignatureScheme   string
	SignatureKeyID    string
	ConfigManifestID  string
	ConfigVersion     int64
	SignatureDigest   [32]byte
	ProviderPaused    bool
}

type HostedSettlementResult struct {
	Duplicate         bool   `json:"duplicate"`
	OutOfOrder        bool   `json:"out_of_order"`
	Quarantined       bool   `json:"quarantined"`
	Settled           bool   `json:"settled"`
	ProviderInboxID   string `json:"provider_inbox_id,omitempty"`
	PrebindEvidenceID string `json:"prebind_evidence_id,omitempty"`
	IntentID          string `json:"intent_id,omitempty"`
	RouteID           string `json:"route_id,omitempty"`
	SettlementID      string `json:"settlement_id,omitempty"`
	WebhookEventID    string `json:"webhook_event_id,omitempty"`
}
