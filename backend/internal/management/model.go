package management

import (
	"encoding/json"
	"errors"
	"time"
)

var (
	ErrInvalid             = errors.New("invalid management request")
	ErrUnauthenticated     = errors.New("management authentication failed")
	ErrForbidden           = errors.New("management permission denied")
	ErrNotFound            = errors.New("management resource not found")
	ErrConflict            = errors.New("management state conflict")
	ErrIdempotencyConflict = errors.New("management idempotency conflict")
	ErrDependency          = errors.New("management dependency unavailable")
)

type Principal struct {
	TenantID       string
	MerchantID     string
	ActorID        string
	SessionID      string
	AuthMethod     string
	Scopes         map[string]bool
	StepUpAt       time.Time
	ApprovalActor  string
	ApprovalReason string
}

func (p Principal) Has(scope string) bool { return p.Scopes[scope] || p.Scopes["management:*"] }

type Idempotency struct {
	Key         string
	Fingerprint [32]byte
}

type PaymentLink struct {
	ID              string          `json:"id"`
	Name            string          `json:"name"`
	PublicURL       string          `json:"public_url,omitempty"`
	AmountMinor     string          `json:"amount_minor"`
	Currency        string          `json:"currency"`
	CurrencyScale   int16           `json:"currency_scale"`
	Description     string          `json:"description"`
	AllowedRoutes   json.RawMessage `json:"allowed_routes"`
	Metadata        json.RawMessage `json:"metadata"`
	AllowedOrigin   string          `json:"allowed_origin,omitempty"`
	SuccessURL      string          `json:"success_url"`
	CancelURL       string          `json:"cancel_url"`
	MaxUses         int64           `json:"max_uses"`
	UseCount        int64           `json:"use_count"`
	SettledCount    int64           `json:"settled_count"`
	SettledMinor    string          `json:"settled_minor"`
	Status          string          `json:"status"`
	ExpiresAt       *time.Time      `json:"expires_at,omitempty"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
	Version         int64           `json:"version"`
	PublicTokenHash [32]byte        `json:"-"`
}

type PaymentLinkInput struct {
	Name          string          `json:"name"`
	AmountMinor   string          `json:"amount_minor"`
	Currency      string          `json:"currency"`
	CurrencyScale int16           `json:"currency_scale"`
	Description   string          `json:"description"`
	AllowedRoutes json.RawMessage `json:"allowed_routes"`
	Metadata      json.RawMessage `json:"metadata"`
	AllowedOrigin string          `json:"allowed_origin,omitempty"`
	SuccessURL    string          `json:"success_url"`
	CancelURL     string          `json:"cancel_url"`
	MaxUses       int64           `json:"max_uses"`
	ExpiresAt     *time.Time      `json:"expires_at,omitempty"`
}

type PaymentLinkRoute struct {
	Provider   string `json:"provider"`
	ChainID    string `json:"chain_id,omitempty"`
	ProviderID string `json:"provider_id"`
	AssetID    string `json:"asset_id"`
}

type PublicPaymentLink struct {
	Name          string          `json:"name"`
	AmountMinor   string          `json:"amount_minor"`
	Currency      string          `json:"currency"`
	CurrencyScale int16           `json:"currency_scale"`
	Description   string          `json:"description"`
	AllowedRoutes json.RawMessage `json:"allowed_routes"`
	ExpiresAt     *time.Time      `json:"expires_at,omitempty"`
}

type RedeemPaymentLinkInput struct {
	CustomerReference string          `json:"customer_reference,omitempty"`
	Metadata          json.RawMessage `json:"metadata,omitempty"`
}

type PaymentLinkRedemption struct {
	IntentID   string          `json:"intent_id"`
	Checkout   CheckoutIssue   `json:"checkout"`
	Session    CheckoutSession `json:"session"`
	SuccessURL string          `json:"success_url"`
	CancelURL  string          `json:"cancel_url"`
}

type CheckoutIssueInput struct {
	IntentID       string   `json:"intent_id"`
	Audience       string   `json:"audience,omitempty"`
	AllowedOrigin  string   `json:"allowed_origin,omitempty"`
	TTLSeconds     int64    `json:"ttl_seconds"`
	AllowedActions []string `json:"allowed_actions,omitempty"`
}

type CheckoutIssue struct {
	Token     string    `json:"token"`
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
}

type CheckoutRoute struct {
	ID              string `json:"id"`
	Provider        string `json:"provider"`
	ProviderID      string `json:"provider_id,omitempty"`
	Network         string `json:"network,omitempty"`
	Asset           string `json:"asset"`
	Amount          string `json:"amount"`
	ReceivedAmount  string `json:"received_amount,omitempty"`
	RemainingAmount string `json:"remaining_amount,omitempty"`
	PaymentCount    int64  `json:"payment_count,omitempty"`
	TopUpAllowed    *bool  `json:"top_up_allowed,omitempty"`
	Address         string `json:"address,omitempty"`
	PaymentURL      string `json:"payment_url,omitempty"`
	TransactionHash string `json:"transaction_hash,omitempty"`
	ExplorerURL     string `json:"explorer_url,omitempty"`
}

type CheckoutSession struct {
	IntentID        string          `json:"intent_id"`
	OrderID         string          `json:"order_id"`
	Status          string          `json:"status"`
	ExpiresAt       time.Time       `json:"expires_at"`
	SelectedRouteID string          `json:"selected_route_id"`
	Routes          []CheckoutRoute `json:"routes"`
	Version         int64           `json:"-"`
}

type ReceiptTarget struct {
	TenantID       string
	MerchantID     string
	IntentID       string
	RouteID        string
	ChainID        string
	AssetID        string
	Address        string
	ExpectedAmount string
	AssetDecimals  uint8
}

type ReceiptAnalysisInput struct {
	MediaType string
	Image     []byte
	Target    ReceiptTarget
}

type ReceiptAnalysis struct {
	TransactionID string   `json:"transaction_id,omitempty"`
	NetworkHint   string   `json:"network_hint,omitempty"`
	AssetHint     string   `json:"asset_hint,omitempty"`
	Amount        string   `json:"amount,omitempty"`
	Destination   string   `json:"destination,omitempty"`
	OccurredAt    string   `json:"occurred_at,omitempty"`
	Confidence    int      `json:"confidence"`
	ReasonCodes   []string `json:"reason_codes"`
}

// ReceiptTransferCandidate is a canonical chain fact discovered independently
// of the user-supplied image. A receipt may suggest where to look, but only a
// unique already-ingested transfer can supply the transaction identity.
type ReceiptTransferCandidate struct {
	TransferEventID string
	TransactionID   string
}

type ReceiptSubmission struct {
	ID                     string          `json:"id"`
	PaymentID              string          `json:"payment_id"`
	Status                 string          `json:"status"`
	ProofID                string          `json:"proof_id,omitempty"`
	ChainID                string          `json:"chain_id,omitempty"`
	TransactionID          string          `json:"transaction_id,omitempty"`
	CorrelationMethod      string          `json:"correlation_method,omitempty"`
	MatchedTransferEventID string          `json:"matched_transfer_event_id,omitempty"`
	Analysis               ReceiptAnalysis `json:"analysis"`
	Message                string          `json:"message"`
	CreatedAt              time.Time       `json:"created_at"`
}

type WebhookEndpoint struct {
	ID             string     `json:"id"`
	URL            string     `json:"url"`
	EventTypes     []string   `json:"event_types"`
	TimeoutMS      int        `json:"timeout_ms"`
	MaxConcurrency int        `json:"max_concurrency"`
	Status         string     `json:"status"`
	SigningKeyID   string     `json:"signing_key_id"`
	OverlapEndsAt  *time.Time `json:"overlap_ends_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	Version        int64      `json:"version"`
}

type WebhookEndpointInput struct {
	URL            string   `json:"url"`
	EventTypes     []string `json:"event_types"`
	TimeoutMS      int      `json:"timeout_ms"`
	MaxConcurrency int      `json:"max_concurrency"`
}

type SecretResult struct {
	KeyID      string     `json:"key_id"`
	Secret     string     `json:"secret"`
	ValidFrom  time.Time  `json:"valid_from"`
	ValidUntil *time.Time `json:"valid_until,omitempty"`
}

type WebhookEndpointSecret struct {
	Endpoint WebhookEndpoint `json:"endpoint"`
	KeyID    string          `json:"key_id"`
	Secret   string          `json:"secret"`
}

type WebhookVerificationTarget struct {
	Endpoint  WebhookEndpoint
	Challenge string
}

type WebhookDelivery struct {
	ID                string     `json:"id"`
	EventID           string     `json:"event_id"`
	EventType         string     `json:"event_type"`
	Status            string     `json:"status"`
	AttemptCount      int        `json:"attempt_count"`
	LastHTTPStatus    *int       `json:"last_http_status,omitempty"`
	LastErrorCategory string     `json:"last_error_category,omitempty"`
	ResponseSnippet   string     `json:"response_snippet,omitempty"`
	NextAttemptAt     time.Time  `json:"next_attempt_at"`
	AcknowledgedAt    *time.Time `json:"acknowledged_at,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
	Version           int64      `json:"version"`
}

type APIClient struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Status    string          `json:"status"`
	Scopes    []string        `json:"scopes"`
	Versions  []APIKeyVersion `json:"versions"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
	Version   int64           `json:"version"`
}

type APIKeyVersion struct {
	ID         string     `json:"id"`
	KeyID      string     `json:"key_id"`
	Number     int64      `json:"number"`
	Status     string     `json:"status"`
	ValidFrom  time.Time  `json:"valid_from"`
	ValidUntil *time.Time `json:"valid_until,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

type APIClientInput struct {
	Name       string     `json:"name"`
	Scopes     []string   `json:"scopes"`
	ValidUntil *time.Time `json:"valid_until,omitempty"`
}

type APIClientSecret struct {
	Client APIClient `json:"client"`
	KeyID  string    `json:"key_id"`
	Secret string    `json:"secret"`
}

type AuditEvent struct {
	ID           string          `json:"id"`
	Sequence     int64           `json:"sequence"`
	ActorID      string          `json:"actor_id"`
	SessionID    string          `json:"session_id,omitempty"`
	Action       string          `json:"action"`
	ResourceType string          `json:"resource_type"`
	ResourceID   string          `json:"resource_id"`
	Reason       string          `json:"reason,omitempty"`
	Details      json.RawMessage `json:"details"`
	PreviousHash string          `json:"previous_hash"`
	EntryHash    string          `json:"entry_hash"`
	OccurredAt   time.Time       `json:"occurred_at"`
}

// ManagementActionRequest is the durable four-eyes envelope for a dangerous
// mutation. RequestBody is generated by the service from typed input and is
// never supplied again by the approver.
type ManagementActionRequest struct {
	ID                     string          `json:"id"`
	Operation              string          `json:"operation"`
	ResourceType           string          `json:"resource_type"`
	ResourceID             string          `json:"resource_id"`
	ResourceVersion        int64           `json:"resource_version"`
	RequestReason          string          `json:"request_reason"`
	RequestedBy            string          `json:"requested_by"`
	ApprovedBy             string          `json:"approved_by,omitempty"`
	ApprovalReason         string          `json:"approval_reason,omitempty"`
	Status                 string          `json:"status"`
	FailureCode            string          `json:"failure_code,omitempty"`
	MutationIdempotencyKey string          `json:"-"`
	RequestBody            json.RawMessage `json:"-"`
	RequestHash            [32]byte        `json:"-"`
	ApprovalHash           [32]byte        `json:"-"`
	RequestedSession       string          `json:"-"`
	RequestedStepUpAt      time.Time       `json:"-"`
	LeaseToken             string          `json:"-"`
	LeaseUntil             *time.Time      `json:"-"`
	CreatedAt              time.Time       `json:"created_at"`
	ExpiresAt              time.Time       `json:"expires_at"`
	ApprovedAt             *time.Time      `json:"approved_at,omitempty"`
	CompletedAt            *time.Time      `json:"completed_at,omitempty"`
	UpdatedAt              time.Time       `json:"updated_at"`
	Version                int64           `json:"version"`
}

type ManagementActionDecision struct {
	Reason string `json:"reason"`
}

type Page[T any] struct {
	Data       []T    `json:"data"`
	NextCursor string `json:"next_cursor,omitempty"`
}
