package domain

import (
	"encoding/json"
	"time"
)

type PublicEvent struct {
	EventID          string          `json:"event_id"`
	EventType        string          `json:"event_type"`
	SchemaVersion    string          `json:"schema_version"`
	AggregateID      string          `json:"aggregate_id"`
	AggregateType    string          `json:"aggregate_type"`
	AggregateVersion int64           `json:"aggregate_version"`
	Sequence         int64           `json:"sequence"`
	Payload          json.RawMessage `json:"payload"`
	OccurredAt       time.Time       `json:"occurred_at"`
}

type MerchantTransfer struct {
	TransferEventID string         `json:"transfer_event_id"`
	PaymentIntentID string         `json:"payment_intent_id"`
	PaymentRouteID  string         `json:"payment_route_id"`
	ChainID         string         `json:"chain_id"`
	AssetID         string         `json:"asset_id"`
	TransactionID   string         `json:"transaction_id"`
	EventIndex      string         `json:"event_index"`
	FromAddress     string         `json:"from_address"`
	ToAddress       string         `json:"to_address"`
	AmountAtomic    string         `json:"amount_atomic"`
	BlockHeight     string         `json:"block_height"`
	BlockHash       string         `json:"block_hash"`
	Confirmations   uint64         `json:"confirmations"`
	Status          TransferStatus `json:"status"`
	MatchState      string         `json:"match_state"`
	OnChainTime     time.Time      `json:"on_chain_time"`
}

type QuoteView struct {
	ID                 string    `json:"id"`
	PaymentIntentID    string    `json:"payment_intent_id"`
	FiatAmountMinor    string    `json:"fiat_amount_minor"`
	FiatCurrency       string    `json:"fiat_currency"`
	FiatScale          uint8     `json:"fiat_scale"`
	AssetID            string    `json:"asset_id"`
	CryptoAmountAtomic string    `json:"crypto_amount_atomic"`
	ReferencePrice     string    `json:"reference_price"`
	SpreadBPS          int       `json:"spread_bps"`
	PolicyVersion      int64     `json:"policy_version"`
	IssuedAt           time.Time `json:"issued_at"`
	ExpiresAt          time.Time `json:"expires_at"`
}

// QuoteDetail is an immutable merchant-safe view of the exact quote inputs.
// Raw provider responses and object-store locations are deliberately excluded;
// their hashes are sufficient for a merchant to bind an exported audit record.
type QuoteDetail struct {
	QuoteView
	SourceTickIDs       []string          `json:"source_tick_ids"`
	Sources             []QuoteSourceView `json:"sources"`
	RawProvenanceSHA256 string            `json:"raw_provenance_sha256"`
}

type QuoteSourceView struct {
	ID               string    `json:"id"`
	Source           string    `json:"source"`
	PriceNumerator   string    `json:"price_numerator"`
	PriceDenominator string    `json:"price_denominator"`
	SpreadBPS        int       `json:"spread_bps"`
	PolicyVersion    int64     `json:"policy_version"`
	ObservedAt       time.Time `json:"observed_at"`
	MaxAgeSeconds    int       `json:"max_age_seconds"`
	ProvenanceSHA256 string    `json:"provenance_sha256"`
}

// WebhookEventView exposes the byte-exact callback envelope together with its
// digest. CanonicalBodyBase64 is the lossless representation; CanonicalBody is
// included for ordinary JSON consumers.
type WebhookEventView struct {
	EventID             string          `json:"event_id"`
	EventType           string          `json:"event_type"`
	SchemaVersion       string          `json:"schema_version"`
	Sequence            int64           `json:"sequence"`
	PaymentIntentID     string          `json:"payment_intent_id,omitempty"`
	CanonicalBody       json.RawMessage `json:"canonical_body"`
	CanonicalBodyBase64 string          `json:"canonical_body_base64"`
	BodySHA256          string          `json:"body_sha256"`
	OccurredAt          time.Time       `json:"occurred_at"`
}

type ReconciliationReportStatus string

const (
	ReconciliationReportQueued     ReconciliationReportStatus = "queued"
	ReconciliationReportProcessing ReconciliationReportStatus = "processing"
	ReconciliationReportRetry      ReconciliationReportStatus = "retry"
	ReconciliationReportReady      ReconciliationReportStatus = "ready"
	ReconciliationReportDeadLetter ReconciliationReportStatus = "dead_letter"
)

// ReconciliationReport is the durable state of an immutable ledger export.
// All counts, amounts, sequence values and byte sizes are JSON strings to avoid
// precision loss in JavaScript clients.
type ReconciliationReport struct {
	ID                     string                     `json:"id"`
	TenantID               string                     `json:"-"`
	MerchantID             string                     `json:"-"`
	Status                 ReconciliationReportStatus `json:"status"`
	Format                 string                     `json:"format"`
	PeriodStart            time.Time                  `json:"period_start"`
	PeriodEnd              time.Time                  `json:"period_end"`
	SnapshotLedgerSequence string                     `json:"snapshot_ledger_sequence"`
	SnapshotCutoff         time.Time                  `json:"snapshot_cutoff"`
	AttemptCount           int                        `json:"attempt_count"`
	LastErrorCode          string                     `json:"last_error_code,omitempty"`
	ObjectSizeBytes        string                     `json:"object_size_bytes,omitempty"`
	ObjectSHA256           string                     `json:"object_sha256,omitempty"`
	Signature              string                     `json:"signature,omitempty"`
	SigningKeyID           string                     `json:"signing_key_id,omitempty"`
	DownloadPath           string                     `json:"download_path,omitempty"`
	CreatedAt              time.Time                  `json:"created_at"`
	UpdatedAt              time.Time                  `json:"updated_at"`
	CompletedAt            *time.Time                 `json:"completed_at,omitempty"`
	Version                int64                      `json:"version"`
}

type BalanceView struct {
	AccountCode  string `json:"account_code"`
	AssetID      string `json:"asset_id"`
	DebitAtomic  string `json:"debit_atomic"`
	CreditAtomic string `json:"credit_atomic"`
}

type ReconciliationSummary struct {
	IntentCounts        map[string]int64 `json:"intent_counts"`
	UnmatchedOpen       int64            `json:"unmatched_open"`
	PendingOutbox       int64            `json:"pending_outbox"`
	DeadLetterCallbacks int64            `json:"dead_letter_callbacks"`
	GeneratedAt         time.Time        `json:"generated_at"`
}
