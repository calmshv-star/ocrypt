package application

import (
	"encoding/json"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/money"
)

type Principal struct {
	TenantID   string
	MerchantID string
	ActorID    string
	KeyID      string
	Scopes     map[string]bool
}

func (p Principal) Allows(scope string) bool { return p.Scopes[scope] }

type CreateIntent struct {
	Principal         Principal
	IdempotencyKey    string
	MerchantOrderID   string
	CustomerReference string
	AmountMinor       money.Amount
	Currency          string
	CurrencyScale     uint8
	Description       string
	Metadata          json.RawMessage
	AllowedRoutes     []domain.RouteSelector
	ExpiresAt         time.Time
	CorrelationID     string
	RequestHash       string
}

type CreateRoute struct {
	Principal           Principal
	IntentID            string
	IdempotencyKey      string
	Provider            string
	ProviderID          string
	ProviderOrderID     string
	ProviderReference   string
	PaymentURL          string
	QuoteID             string
	AddressAssignmentID string
	ChainID             string
	AssetID             string
	ExpectedAmount      money.Amount
	AssetDecimals       uint8
	DisplayAmount       string
	Address             string
	Memo                string
	RequiredFinality    uint64
	ExpiresAt           time.Time
	GraceEndsAt         time.Time
	CorrelationID       string
	RequestHash         string
}

type CancelIntent struct {
	Principal       Principal
	IntentID        string
	IdempotencyKey  string
	Reason          string
	ExpectedVersion int64
	CorrelationID   string
	RequestHash     string
}

type ExpireIntent struct {
	Principal       Principal
	IntentID        string
	IdempotencyKey  string
	Reason          string
	ExpectedVersion int64
	CorrelationID   string
	RequestHash     string
}

type UpdateIntentMetadata struct {
	Principal       Principal
	IntentID        string
	IdempotencyKey  string
	Metadata        json.RawMessage
	ExpectedVersion int64
	CorrelationID   string
	RequestHash     string
}

type CreateReconciliationReport struct {
	Principal      Principal
	IdempotencyKey string
	Format         string
	PeriodStart    time.Time
	PeriodEnd      time.Time
	CorrelationID  string
	RequestHash    string
}

type SubmitPaymentProof struct {
	Principal       Principal
	IdempotencyKey  string
	PaymentIntentID string
	ChainID         string
	TransactionID   string
	CorrelationID   string
	RequestHash     string
}

type RequestManualResolution struct {
	Principal         Principal
	UnmatchedID       string
	TargetRouteID     string
	IdempotencyKey    string
	AcceptShortfall   bool
	AcceptLatePayment bool
	AcceptCrossAsset  bool
	Reason            string
	RequestHash       string
}

type ApproveManualResolution struct {
	Principal       Principal
	ResolutionID    string
	ExpectedVersion int64
}
