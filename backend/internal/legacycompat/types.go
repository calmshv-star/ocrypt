// Package legacycompat contains the quarantined JSON-MD5/Form-MD5 compatibility
// boundary. No other package may depend on its MD5 signing contract.
package legacycompat

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"time"
)

type Protocol string

const (
	ProtocolJSONMD5 Protocol = "json_md5"
	ProtocolFormMD5 Protocol = "form_md5"
)

var (
	ErrInvalid      = errors.New("invalid legacy request")
	ErrUnauthorized = errors.New("legacy authentication failed")
	ErrNotFound     = errors.New("legacy transaction not found")
	ErrUnavailable  = errors.New("legacy compatibility unavailable")
	ErrConflict     = errors.New("legacy idempotency conflict")
)

type Credential struct {
	ConfigID            string
	CredentialVersionID string
	CredentialVersion   int64
	Protocol            Protocol
	PID                 string
	TenantID            string
	MerchantID          string
	LegacySecretRef     string
	CallbackKeyID       string
	CoreKeyID           string
	CoreSecretRef       string
	Currency            string
	CurrencyScale       uint8
	ChainID             string
	AssetID             string
	LegacyToken         string
	LegacyNetwork       string
	LegacyPaymentType   string
	IPAllowlist         []*net.IPNet
	Approved            bool
	Enabled             bool
	SunsetAt            time.Time
}

type CreateRequest struct {
	Protocol      Protocol
	PID           string
	OrderID       string
	Amount        string
	Currency      string
	Token         string
	Network       string
	PaymentType   string
	Name          string
	NotifyURL     string
	ReturnURL     string
	Signature     string
	Canonical     string
	CanonicalHash [32]byte
}

type Mapping struct {
	ConfigID            string
	CredentialVersionID string
	Protocol            Protocol
	TradeID             string
	OrderID             string
	IntentID            string
	RouteID             string
	RequestHash         [32]byte
	NotifyURL           string
	ReturnURL           string
	Name                string
	PaymentType         string
	Amount              string
	Currency            string
	Token               string
	Network             string
	CreatedAt           time.Time
}

type CoreIntent struct {
	ID              string          `json:"id"`
	CheckoutToken   string          `json:"checkout_token,omitempty"`
	MerchantOrderID string          `json:"merchant_order_id"`
	AmountMinor     string          `json:"amount_minor"`
	Currency        string          `json:"currency"`
	CurrencyScale   uint8           `json:"currency_scale"`
	Status          string          `json:"status"`
	ExpiresAt       time.Time       `json:"expires_at"`
	Routes          []CoreRoute     `json:"routes"`
	Metadata        json.RawMessage `json:"metadata,omitempty"`
}

type CoreRoute struct {
	ID             string    `json:"id"`
	IntentID       string    `json:"intent_id"`
	ChainID        string    `json:"chain_id"`
	AssetID        string    `json:"asset_id"`
	DisplayAmount  string    `json:"display_amount"`
	Address        string    `json:"address"`
	Status         string    `json:"status"`
	ExpiresAt      time.Time `json:"expires_at"`
	PaymentURL     string    `json:"payment_url,omitempty"`
	Provider       string    `json:"provider"`
	ExpectedAtomic string    `json:"expected_amount_atomic"`
}

type CoreEvent struct {
	EventID   string          `json:"event_id"`
	EventType string          `json:"event_type"`
	Sequence  int64           `json:"sequence"`
	Payload   json.RawMessage `json:"payload"`
}

type CallbackJob struct {
	DeliveryID          string
	LeaseToken          string
	Fence               int64
	Protocol            Protocol
	EventID             string
	TargetURL           string
	HTTPMethod          string
	ContentType         string
	FrozenBody          []byte
	CredentialVersionID string
	CallbackKeyID       string
	AttemptCount        int
}

type Repository interface {
	LookupCredential(context.Context, Protocol, string, time.Time) (Credential, error)
	LookupCredentialVersion(context.Context, string) (Credential, error)
	RecordMapping(context.Context, Mapping) (Mapping, bool, error)
	LookupMapping(context.Context, string) (Mapping, error)
	LookupMappingByIntent(context.Context, string, string) (Mapping, error)
	ListEventSources(context.Context, time.Time) ([]EventSource, error)
	ClassifyEvent(context.Context, string, int64, string, string, time.Time) error
	EnqueueCallbackAndAdvance(context.Context, EventSource, CoreEvent, Mapping, FrozenCallback, time.Time) error
	ClaimCallbacks(context.Context, string, int, time.Duration, time.Time) ([]CallbackJob, error)
	AcknowledgeCallback(context.Context, string, string, int64, int, [32]byte, time.Time) (bool, error)
	FailCallback(context.Context, string, string, int64, string, int, time.Time) (bool, error)
	Ready(context.Context, time.Time) error
}

type EventSource struct {
	ConfigID      string
	Protocol      Protocol
	PID           string
	CoreKeyID     string
	CoreSecretRef string
	AfterSequence int64
}

type FrozenCallback struct {
	HTTPMethod          string
	ContentType         string
	TargetURL           string
	Body                []byte
	CredentialVersionID string
	CallbackKeyID       string
}

type Core interface {
	CreateIntent(context.Context, Credential, string, string, CreateRequest) (CoreIntent, error)
	CreateRoute(context.Context, Credential, string, string, string) (CoreRoute, error)
	GetIntent(context.Context, Credential, string) (CoreIntent, error)
	ListEvents(context.Context, EventSource, int) ([]CoreEvent, error)
}

type SecretSource interface {
	Read(string) ([]byte, error)
}
