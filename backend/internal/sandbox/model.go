// Package sandbox implements the deterministic merchant test environment.
// It deliberately owns its persistence model instead of reusing live payment
// tables: sandbox identifiers can never be resolved by production payment
// handlers and reset cannot cascade into recoverable production data.
package sandbox

import (
	"encoding/json"
	"time"
)

const (
	DefaultClock = "2026-08-01T00:00:00Z"
	MaxPageSize  = 100
)

type ScenarioKind string

const (
	ScenarioExact             ScenarioKind = "exact_payment"
	ScenarioPartial           ScenarioKind = "partial_payment"
	ScenarioUnder             ScenarioKind = "underpayment"
	ScenarioOver              ScenarioKind = "overpayment"
	ScenarioLate              ScenarioKind = "late_payment"
	ScenarioWrongAsset        ScenarioKind = "wrong_asset"
	ScenarioDuplicateCallback ScenarioKind = "duplicate_callback"
	ScenarioOutOfOrder        ScenarioKind = "out_of_order_callback"
	ScenarioTimeout           ScenarioKind = "timeout"
	ScenarioDeadLetter        ScenarioKind = "dead_letter"
	ScenarioReorg             ScenarioKind = "reorg"
	ScenarioReorgRecovery     ScenarioKind = "reorg_recovery"
)

type ActionKind string

const (
	ActionObserve            ActionKind = "observe"
	ActionConfirm            ActionKind = "confirm"
	ActionFinalize           ActionKind = "finalize"
	ActionCallbackDeliver    ActionKind = "callback_deliver"
	ActionCallbackOutOfOrder ActionKind = "callback_deliver_out_of_order"
	ActionCallbackFail       ActionKind = "callback_fail"
	ActionCallbackTimeout    ActionKind = "callback_timeout"
	ActionDeadLetter         ActionKind = "dead_letter"
	ActionReorg              ActionKind = "reorg"
	ActionRecover            ActionKind = "recover"
)

type Workspace struct {
	Mode                   string         `json:"mode"`
	MerchantID             string         `json:"merchant_id"`
	Clock                  time.Time      `json:"clock"`
	Version                int64          `json:"version"`
	Credential             TestCredential `json:"credential"`
	Addresses              []TestAddress  `json:"addresses"`
	ResetConfirmationToken string         `json:"reset_confirmation_token"`
}

type TestCredential struct {
	KeyID        string   `json:"key_id"`
	Environment  string   `json:"environment"`
	Scopes       []string `json:"scopes"`
	Secret       string   `json:"secret"`
	SecretStatus string   `json:"secret_status"`
}

type TestAddress struct {
	ID      string `json:"id"`
	ChainID string `json:"chain_id"`
	AssetID string `json:"asset_id"`
	Address string `json:"address"`
	Memo    string `json:"memo,omitempty"`
}

type Scenario struct {
	ID                string        `json:"id"`
	TenantID          string        `json:"-"`
	MerchantID        string        `json:"-"`
	Kind              ScenarioKind  `json:"scenario"`
	MerchantOrderID   string        `json:"merchant_order_id"`
	PaymentIntent     PaymentIntent `json:"payment_intent"`
	Route             PaymentRoute  `json:"route"`
	ObservedAmount    string        `json:"observed_amount_atomic"`
	ObservedAssetID   string        `json:"observed_asset_id,omitempty"`
	Confirmations     uint64        `json:"confirmations"`
	Finalized         bool          `json:"finalized"`
	LastEventSequence int64         `json:"last_event_sequence"`
	Version           int64         `json:"version"`
	CreatedAt         time.Time     `json:"created_at"`
	UpdatedAt         time.Time     `json:"updated_at"`
	Events            []Event       `json:"events,omitempty"`
}

type PaymentIntent struct {
	ID            string     `json:"id"`
	AmountMinor   string     `json:"amount_minor"`
	Currency      string     `json:"currency"`
	CurrencyScale uint8      `json:"currency_scale"`
	Status        string     `json:"status"`
	StatusReason  string     `json:"status_reason,omitempty"`
	ExpiresAt     time.Time  `json:"expires_at"`
	SettledAt     *time.Time `json:"settled_at,omitempty"`
	Version       int64      `json:"version"`
}

type PaymentRoute struct {
	ID                    string    `json:"id"`
	IntentID              string    `json:"intent_id"`
	ChainID               string    `json:"chain_id"`
	AssetID               string    `json:"asset_id"`
	ExpectedAmountAtomic  string    `json:"expected_amount_atomic"`
	AssetDecimals         uint8     `json:"asset_decimals"`
	Address               string    `json:"address"`
	Memo                  string    `json:"memo,omitempty"`
	RequiredConfirmations uint64    `json:"required_confirmations"`
	Status                string    `json:"status"`
	ExpiresAt             time.Time `json:"expires_at"`
	Version               int64     `json:"version"`
}

type Event struct {
	ID         string          `json:"id"`
	Sequence   int64           `json:"sequence"`
	Type       string          `json:"type"`
	OccurredAt time.Time       `json:"occurred_at"`
	Payload    json.RawMessage `json:"payload"`
}

type Callback struct {
	ID            string            `json:"id"`
	ScenarioID    string            `json:"scenario_id"`
	EventID       string            `json:"event_id"`
	EventSequence int64             `json:"event_sequence"`
	Status        string            `json:"status"`
	CanonicalBody json.RawMessage   `json:"canonical_body"`
	BodySHA256    string            `json:"body_sha256"`
	AttemptCount  int               `json:"attempt_count"`
	Attempts      []CallbackAttempt `json:"attempts"`
	CreatedAt     time.Time         `json:"created_at"`
	UpdatedAt     time.Time         `json:"updated_at"`
	Version       int64             `json:"version"`
}

type CallbackAttempt struct {
	Number        int       `json:"number"`
	Outcome       string    `json:"outcome"`
	HTTPStatus    int       `json:"http_status,omitempty"`
	ErrorCategory string    `json:"error_category,omitempty"`
	ResponseBytes int       `json:"response_bytes"`
	AttemptedAt   time.Time `json:"attempted_at"`
}

type CreateScenario struct {
	Kind                  ScenarioKind `json:"scenario"`
	MerchantOrderID       string       `json:"merchant_order_id"`
	AmountMinor           string       `json:"amount_minor"`
	Currency              string       `json:"currency"`
	CurrencyScale         uint8        `json:"currency_scale"`
	ChainID               string       `json:"chain_id,omitempty"`
	AssetID               string       `json:"asset_id,omitempty"`
	ExpectedAmountAtomic  string       `json:"expected_amount_atomic,omitempty"`
	AssetDecimals         uint8        `json:"asset_decimals,omitempty"`
	RequiredConfirmations uint64       `json:"required_confirmations,omitempty"`
}

type Action struct {
	Type            ActionKind `json:"type"`
	AmountAtomic    string     `json:"amount_atomic,omitempty"`
	AssetID         string     `json:"asset_id,omitempty"`
	Confirmations   uint64     `json:"confirmations,omitempty"`
	CallbackID      string     `json:"callback_id,omitempty"`
	HTTPStatus      int        `json:"http_status,omitempty"`
	ResponseBody    string     `json:"response_body,omitempty"`
	ErrorText       string     `json:"error,omitempty"`
	ReorgDepth      uint64     `json:"reorg_depth,omitempty"`
	ExpectedVersion int64      `json:"expected_version"`
}

type Page[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor"`
}

type ResetResult struct {
	DeletedScenarios int64     `json:"deleted_scenarios"`
	DeletedCallbacks int64     `json:"deleted_callbacks"`
	WorkspaceVersion int64     `json:"workspace_version"`
	Clock            time.Time `json:"clock"`
}
