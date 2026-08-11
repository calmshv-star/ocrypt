package merchantplatform

import "encoding/json"

type AtomicAmount string
type RouteSelector struct {
	Provider   string `json:"provider"`
	ChainID    string `json:"chain_id,omitempty"`
	ProviderID string `json:"provider_id,omitempty"`
	AssetID    string `json:"asset_id"`
}

func OnChainRouteSelector(chainID, assetID string) RouteSelector {
	return RouteSelector{Provider: "on_chain", ChainID: chainID, AssetID: assetID}
}
func HostedGatewayRouteSelector(providerID, assetID string) RouteSelector {
	return RouteSelector{Provider: "hosted_gateway", ProviderID: providerID, AssetID: assetID}
}

type CreatePaymentIntentRequest struct {
	MerchantOrderID   string          `json:"merchant_order_id"`
	AmountMinor       AtomicAmount    `json:"amount_minor"`
	Currency          string          `json:"currency"`
	CurrencyScale     int             `json:"currency_scale"`
	Description       string          `json:"description,omitempty"`
	CustomerReference string          `json:"customer_reference,omitempty"`
	ExpiresIn         int             `json:"expires_in,omitempty"`
	ExpiresAt         string          `json:"expires_at,omitempty"`
	AllowedRoutes     []RouteSelector `json:"allowed_routes,omitempty"`
	Metadata          map[string]any  `json:"metadata,omitempty"`
}
type CreatePaymentRouteRequest struct {
	Provider      string                     `json:"provider"`
	OnChain       *OnChainRouteRequest       `json:"on_chain,omitempty"`
	HostedGateway *HostedGatewayRouteRequest `json:"hosted_gateway,omitempty"`
	ExpiresIn     int                        `json:"expires_in,omitempty"`
}
type OnChainRouteRequest struct {
	ChainID string `json:"chain_id"`
	AssetID string `json:"asset_id"`
}
type HostedGatewayRouteRequest struct {
	ProviderID string `json:"provider_id"`
	AssetID    string `json:"asset_id"`
}

func NewOnChainPaymentRoute(chainID, assetID string, expiresIn int) CreatePaymentRouteRequest {
	return CreatePaymentRouteRequest{Provider: "on_chain", OnChain: &OnChainRouteRequest{ChainID: chainID, AssetID: assetID}, ExpiresIn: expiresIn}
}
func NewHostedGatewayPaymentRoute(providerID, assetID string, expiresIn int) CreatePaymentRouteRequest {
	return CreatePaymentRouteRequest{Provider: "hosted_gateway", HostedGateway: &HostedGatewayRouteRequest{ProviderID: providerID, AssetID: assetID}, ExpiresIn: expiresIn}
}

type CancelPaymentIntentRequest struct {
	Reason          string `json:"reason"`
	ExpectedVersion int64  `json:"expected_version,omitempty"`
}
type ExpirePaymentIntentRequest struct {
	Reason          string `json:"reason"`
	ExpectedVersion int64  `json:"expected_version"`
}
type UpdatePaymentIntentMetadataRequest struct {
	ExpectedVersion int64          `json:"expected_version"`
	Metadata        map[string]any `json:"metadata"`
}
type CreateReconciliationReportRequest struct {
	Format      string `json:"format,omitempty"`
	PeriodStart string `json:"period_start"`
	PeriodEnd   string `json:"period_end"`
}
type SubmitPaymentProofRequest struct {
	PaymentIntentID string `json:"payment_intent_id"`
	ChainID         string `json:"chain_id"`
	TransactionID   string `json:"transaction_id"`
}
type PaymentRoute struct {
	ID                   string       `json:"id"`
	IntentID             string       `json:"intent_id"`
	ChainID              string       `json:"chain_id,omitempty"`
	AssetID              string       `json:"asset_id"`
	Provider             string       `json:"provider"`
	ProviderID           string       `json:"provider_id,omitempty"`
	ProviderOrderID      string       `json:"provider_order_id,omitempty"`
	ProviderReference    string       `json:"provider_reference,omitempty"`
	PaymentURL           string       `json:"payment_url,omitempty"`
	ExpectedAmountAtomic AtomicAmount `json:"expected_amount_atomic"`
	AssetDecimals        int          `json:"asset_decimals"`
	DisplayAmount        string       `json:"display_amount"`
	Address              string       `json:"address,omitempty"`
	Memo                 string       `json:"memo,omitempty"`
	RequiredFinality     int64        `json:"required_finality"`
	Status               string       `json:"status"`
	Version              int64        `json:"version"`
	StartsAt             string       `json:"starts_at"`
	ExpiresAt            string       `json:"expires_at"`
	GraceEndsAt          string       `json:"grace_ends_at"`
}
type PaymentIntent struct {
	ID                string          `json:"id"`
	MerchantID        string          `json:"merchant_id"`
	MerchantOrderID   string          `json:"merchant_order_id"`
	CustomerReference string          `json:"customer_reference,omitempty"`
	AmountMinor       AtomicAmount    `json:"amount_minor"`
	Currency          string          `json:"currency"`
	CurrencyScale     int             `json:"currency_scale"`
	Description       string          `json:"description,omitempty"`
	Status            string          `json:"status"`
	StatusReason      string          `json:"status_reason,omitempty"`
	Metadata          map[string]any  `json:"metadata,omitempty"`
	AllowedRoutes     []RouteSelector `json:"allowed_routes"`
	Version           int64           `json:"version"`
	CreatedAt         string          `json:"created_at"`
	UpdatedAt         string          `json:"updated_at"`
	ExpiresAt         string          `json:"expires_at"`
	SettledAt         string          `json:"settled_at,omitempty"`
	CancelledAt       string          `json:"cancelled_at,omitempty"`
	Routes            []PaymentRoute  `json:"routes"`
	CheckoutToken     string          `json:"checkout_token,omitempty"`
}
type PaymentProof struct {
	ID               string   `json:"id"`
	MerchantID       string   `json:"merchant_id"`
	PaymentIntentID  string   `json:"payment_intent_id"`
	ChainID          string   `json:"chain_id"`
	TransactionID    string   `json:"transaction_id"`
	Status           string   `json:"status"`
	TransferEventIDs []string `json:"transfer_event_ids"`
	CreatedAt        string   `json:"created_at"`
	UpdatedAt        string   `json:"updated_at"`
	Version          int64    `json:"version"`
}
type Asset struct {
	ID                   string       `json:"id"`
	ChainID              string       `json:"chain_id"`
	Symbol               string       `json:"symbol"`
	Name                 string       `json:"name"`
	Kind                 string       `json:"kind"`
	Contract             string       `json:"contract,omitempty"`
	Decimals             int          `json:"decimals"`
	Status               string       `json:"status"`
	MinimumDepositAtomic AtomicAmount `json:"minimum_deposit_atomic"`
}
type Envelope[T any] struct {
	Data       T      `json:"data"`
	RequestID  string `json:"request_id"`
	APIVersion string `json:"api_version"`
}
type CursorPage[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor"`
}
type EventPage[T any] struct {
	Items        []T    `json:"items"`
	NextCursor   string `json:"next_cursor"`
	NextSequence string `json:"next_sequence"`
}
type PublicEvent struct {
	EventID          string          `json:"event_id"`
	EventType        string          `json:"event_type"`
	SchemaVersion    string          `json:"schema_version"`
	AggregateID      string          `json:"aggregate_id"`
	AggregateType    string          `json:"aggregate_type"`
	AggregateVersion int64           `json:"aggregate_version"`
	Sequence         int64           `json:"sequence"`
	Payload          json.RawMessage `json:"payload"`
	OccurredAt       string          `json:"occurred_at"`
}
type StoredWebhookEvent struct {
	EventID             string       `json:"event_id"`
	EventType           string       `json:"event_type"`
	SchemaVersion       string       `json:"schema_version"`
	Sequence            int64        `json:"sequence"`
	PaymentIntentID     string       `json:"payment_intent_id,omitempty"`
	CanonicalBody       WebhookEvent `json:"canonical_body"`
	CanonicalBodyBase64 string       `json:"canonical_body_base64"`
	BodySHA256          string       `json:"body_sha256"`
	OccurredAt          string       `json:"occurred_at"`
}
type WebhookEvent struct {
	EventID       string          `json:"event_id"`
	EventType     string          `json:"event_type"`
	SchemaVersion string          `json:"schema_version"`
	Sequence      int64           `json:"sequence"`
	OccurredAt    string          `json:"occurred_at"`
	MerchantID    string          `json:"merchant_id"`
	Livemode      bool            `json:"livemode"`
	PaymentIntent json.RawMessage `json:"payment_intent"`
	Settlement    json.RawMessage `json:"settlement,omitempty"`
	Observation   json.RawMessage `json:"observation,omitempty"`
	Resolution    json.RawMessage `json:"resolution,omitempty"`
}
type CheckoutRoute struct {
	ID              string `json:"id"`
	Provider        string `json:"provider"`
	ProviderID      string `json:"provider_id,omitempty"`
	Network         string `json:"network,omitempty"`
	Asset           string `json:"asset"`
	Amount          string `json:"amount"`
	Address         string `json:"address,omitempty"`
	PaymentURL      string `json:"payment_url,omitempty"`
	TransactionHash string `json:"transaction_hash,omitempty"`
}
type CheckoutSession struct {
	IntentID        string          `json:"intent_id"`
	OrderID         string          `json:"order_id"`
	Status          string          `json:"status"`
	ExpiresAt       string          `json:"expires_at"`
	SelectedRouteID string          `json:"selected_route_id"`
	Routes          []CheckoutRoute `json:"routes"`
}
type MerchantTransfer struct {
	TransferEventID string       `json:"transfer_event_id"`
	PaymentIntentID string       `json:"payment_intent_id"`
	PaymentRouteID  string       `json:"payment_route_id"`
	ChainID         string       `json:"chain_id"`
	AssetID         string       `json:"asset_id"`
	TransactionID   string       `json:"transaction_id"`
	EventIndex      string       `json:"event_index"`
	FromAddress     string       `json:"from_address"`
	ToAddress       string       `json:"to_address"`
	AmountAtomic    AtomicAmount `json:"amount_atomic"`
	BlockHeight     AtomicAmount `json:"block_height"`
	BlockHash       string       `json:"block_hash"`
	Confirmations   int64        `json:"confirmations"`
	Status          string       `json:"status"`
	MatchState      string       `json:"match_state"`
	OnChainTime     string       `json:"on_chain_time"`
}
type QuoteView struct {
	ID                 string       `json:"id"`
	PaymentIntentID    string       `json:"payment_intent_id"`
	FiatAmountMinor    AtomicAmount `json:"fiat_amount_minor"`
	FiatCurrency       string       `json:"fiat_currency"`
	FiatScale          int          `json:"fiat_scale"`
	AssetID            string       `json:"asset_id"`
	CryptoAmountAtomic AtomicAmount `json:"crypto_amount_atomic"`
	ReferencePrice     string       `json:"reference_price"`
	SpreadBPS          int          `json:"spread_bps"`
	PolicyVersion      int64        `json:"policy_version"`
	IssuedAt           string       `json:"issued_at"`
	ExpiresAt          string       `json:"expires_at"`
}
type QuoteDetail struct {
	QuoteView
	SourceTickIDs       []string         `json:"source_tick_ids"`
	Sources             []map[string]any `json:"sources"`
	RawProvenanceSHA256 string           `json:"raw_provenance_sha256"`
}
type BalanceView struct {
	AccountCode  string       `json:"account_code"`
	AssetID      string       `json:"asset_id"`
	DebitAtomic  AtomicAmount `json:"debit_atomic"`
	CreditAtomic AtomicAmount `json:"credit_atomic"`
}
type ReconciliationSummary struct {
	IntentCounts        map[string]int64 `json:"intent_counts"`
	UnmatchedOpen       int64            `json:"unmatched_open"`
	PendingOutbox       int64            `json:"pending_outbox"`
	DeadLetterCallbacks int64            `json:"dead_letter_callbacks"`
	GeneratedAt         string           `json:"generated_at"`
}
type ReconciliationReport struct {
	ID                     string `json:"id"`
	Status                 string `json:"status"`
	Format                 string `json:"format"`
	PeriodStart            string `json:"period_start"`
	PeriodEnd              string `json:"period_end"`
	SnapshotLedgerSequence string `json:"snapshot_ledger_sequence"`
	SnapshotCutoff         string `json:"snapshot_cutoff"`
	AttemptCount           int    `json:"attempt_count"`
	LastErrorCode          string `json:"last_error_code,omitempty"`
	ObjectSizeBytes        string `json:"object_size_bytes,omitempty"`
	ObjectSHA256           string `json:"object_sha256,omitempty"`
	Signature              string `json:"signature,omitempty"`
	SigningKeyID           string `json:"signing_key_id,omitempty"`
	DownloadPath           string `json:"download_path,omitempty"`
	CreatedAt              string `json:"created_at"`
	UpdatedAt              string `json:"updated_at"`
	CompletedAt            string `json:"completed_at,omitempty"`
	Version                int64  `json:"version"`
}
type PaymentLink struct{ Data map[string]any }
type CheckoutIssue struct {
	Token     string `json:"token"`
	URL       string `json:"url"`
	ExpiresAt string `json:"expires_at"`
}
