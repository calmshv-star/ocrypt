package domain

import "time"

type ProofStatus string

const (
	ProofQueued    ProofStatus = "queued"
	ProofVerifying ProofStatus = "verifying"
	ProofLinked    ProofStatus = "linked"
	ProofNotFound  ProofStatus = "not_found"
	ProofInvalid   ProofStatus = "invalid"
)

type PaymentProof struct {
	ID               string      `json:"id"`
	TenantID         string      `json:"-"`
	MerchantID       string      `json:"merchant_id"`
	PaymentIntentID  string      `json:"payment_intent_id"`
	ChainID          string      `json:"chain_id"`
	TransactionID    string      `json:"transaction_id"`
	Status           ProofStatus `json:"status"`
	TransferEventIDs []string    `json:"transfer_event_ids"`
	CreatedAt        time.Time   `json:"created_at"`
	UpdatedAt        time.Time   `json:"updated_at"`
	Version          int64       `json:"version"`
}
