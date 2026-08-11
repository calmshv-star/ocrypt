// Package refunds implements the custodial refund workflow without handling
// seed phrases or private keys. Merely observing an on-chain sender never makes
// it a safe refund destination.
package refunds

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/money"
)

var (
	ErrValidation             = errors.New("refund validation failed")
	ErrForbidden              = errors.New("refund operation forbidden")
	ErrStateConflict          = errors.New("refund state conflict")
	ErrVersionConflict        = errors.New("refund version conflict")
	ErrIdempotencyConflict    = errors.New("refund idempotency conflict")
	ErrPolicyLimit            = errors.New("refund policy limit exceeded")
	ErrDestinationUnverified  = errors.New("refund destination is not independently verified")
	ErrInsufficientRefundable = errors.New("insufficient refundable amount")
)

type TenantID string
type AssetID string
type ChainID string
type ActorID string
type RefundID string
type SettlementID string

type Address struct {
	Chain ChainID `json:"chain"`
	Value string  `json:"value"`
}

func (a Address) Validate() error {
	if strings.TrimSpace(string(a.Chain)) == "" || strings.TrimSpace(a.Value) == "" || a.Value != strings.TrimSpace(a.Value) || len(a.Value) > 512 {
		return fmt.Errorf("%w: canonical chain address is required", ErrValidation)
	}
	return nil
}

type VerificationMethod string

const (
	VerificationWalletSignature      VerificationMethod = "wallet_signature"
	VerificationCustodianInstruction VerificationMethod = "custodian_return_instruction"
	VerificationMerchantEvidence     VerificationMethod = "merchant_evidence"
	// VerificationObservedSender is evidence only, never proof of ownership.
	VerificationObservedSender VerificationMethod = "observed_chain_sender"
)

type VerifiedDestination struct {
	ID             string             `json:"id"`
	TenantID       TenantID           `json:"tenant_id"`
	SettlementID   SettlementID       `json:"settlement_id"`
	AssetID        AssetID            `json:"asset_id"`
	Address        Address            `json:"address"`
	Method         VerificationMethod `json:"method"`
	EvidenceDigest string             `json:"evidence_digest"`
	VerifiedAt     time.Time          `json:"verified_at"`
	ExpiresAt      time.Time          `json:"expires_at,omitempty"`
	RevokedAt      time.Time          `json:"revoked_at,omitempty"`
}

func (d VerifiedDestination) IsUsable(now time.Time) bool {
	if d.ID == "" || d.TenantID == "" || d.SettlementID == "" || d.AssetID == "" || d.EvidenceDigest == "" || d.VerifiedAt.IsZero() || d.VerifiedAt.After(now) || !d.RevokedAt.IsZero() || (!d.ExpiresAt.IsZero() && !now.Before(d.ExpiresAt)) {
		return false
	}
	if d.Method == VerificationObservedSender || d.Address.Validate() != nil {
		return false
	}
	return true
}

type Settlement struct {
	ID              SettlementID `json:"id"`
	TenantID        TenantID     `json:"tenant_id"`
	AssetID         AssetID      `json:"asset_id"`
	ChainID         ChainID      `json:"chain_id"`
	IntentID        string       `json:"intent_id"`
	ChainEventID    string       `json:"chain_event_id"`
	ObservedSender  Address      `json:"observed_sender"`
	ReceivedAmount  money.Amount `json:"received_amount"`
	AlreadyRefunded money.Amount `json:"already_refunded"`
	Finalized       bool         `json:"finalized"`
	RiskHold        bool         `json:"risk_hold"`
}

func (s Settlement) AvailableRefundable() (money.Amount, error) {
	if !s.Finalized || s.RiskHold {
		return money.Amount{}, fmt.Errorf("%w: settlement is not finalized or is on risk hold", ErrStateConflict)
	}
	remaining, err := s.ReceivedAmount.Sub(s.AlreadyRefunded)
	if err != nil {
		return money.Amount{}, ErrInsufficientRefundable
	}
	return remaining, nil
}

type FeeBearer string

const (
	FeeBearerMerchant FeeBearer = "merchant"
	FeeBearerCustomer FeeBearer = "customer"
)

type Policy struct {
	ID                         string               `json:"id"`
	VersionTag                 string               `json:"version_tag,omitempty"`
	Version                    int64                `json:"version"`
	TenantID                   TenantID             `json:"tenant_id"`
	AssetID                    AssetID              `json:"asset_id"`
	ChainID                    ChainID              `json:"chain_id"`
	Enabled                    bool                 `json:"enabled"`
	EmergencyPaused            bool                 `json:"emergency_paused"`
	RefundToOriginOnly         bool                 `json:"refund_to_origin_only"`
	AllowVerifiedAlternate     bool                 `json:"allow_verified_alternate"`
	RequireApprovalAlways      bool                 `json:"require_approval_always"`
	MaximumRefundAmount        money.Amount         `json:"maximum_refund_amount"`
	DailyRefundLimit           money.Amount         `json:"daily_refund_limit"`
	ApprovalThreshold          money.Amount         `json:"approval_threshold"`
	MaximumNetworkFee          money.Amount         `json:"maximum_network_fee"`
	FeeBearer                  FeeBearer            `json:"fee_bearer"`
	AllowedVerificationMethods []VerificationMethod `json:"allowed_verification_methods"`
}

func (p Policy) Validate() error {
	if p.ID == "" || p.Version < 1 || p.TenantID == "" || p.AssetID == "" || p.ChainID == "" || p.MaximumRefundAmount.IsZero() || p.DailyRefundLimit.IsZero() {
		return fmt.Errorf("%w: complete versioned refund policy is required", ErrValidation)
	}
	if p.MaximumRefundAmount.Cmp(p.DailyRefundLimit) > 0 || p.ApprovalThreshold.Cmp(p.MaximumRefundAmount) > 0 {
		return fmt.Errorf("%w: inconsistent refund limits", ErrValidation)
	}
	if p.FeeBearer != FeeBearerMerchant && p.FeeBearer != FeeBearerCustomer {
		return fmt.Errorf("%w: invalid network fee bearer", ErrValidation)
	}
	if len(p.AllowedVerificationMethods) == 0 {
		return fmt.Errorf("%w: at least one destination verification method is required", ErrValidation)
	}
	for _, method := range p.AllowedVerificationMethods {
		if method == VerificationObservedSender || method == "" {
			return fmt.Errorf("%w: observed sender cannot verify refund ownership", ErrValidation)
		}
	}
	return nil
}

func (p Policy) AllowsMethod(method VerificationMethod) bool {
	for _, allowed := range p.AllowedVerificationMethods {
		if method == allowed {
			return true
		}
	}
	return false
}

type Status string

const (
	StatusApprovalRequired  Status = "approval_required"
	StatusApproved          Status = "approved"
	StatusBuilding          Status = "building"
	StatusAwaitingSignature Status = "awaiting_signature"
	StatusSigned            Status = "signed"
	StatusBroadcast         Status = "broadcast"
	StatusConfirmed         Status = "confirmed"
	StatusFinalized         Status = "finalized"
	StatusRejected          Status = "rejected"
	StatusCancelled         Status = "cancelled"
	StatusFailed            Status = "failed"
	StatusReorged           Status = "reorged"
)

type Approval struct {
	ActorID ActorID   `json:"actor_id"`
	At      time.Time `json:"at"`
	Reason  string    `json:"reason"`
}

type Refund struct {
	ID                        RefundID     `json:"id"`
	TenantID                  TenantID     `json:"tenant_id"`
	SettlementID              SettlementID `json:"settlement_id"`
	AssetID                   AssetID      `json:"asset_id"`
	ChainID                   ChainID      `json:"chain_id"`
	PolicyID                  string       `json:"policy_id"`
	PolicyVersion             int64        `json:"policy_version"`
	CreatorID                 ActorID      `json:"creator_id"`
	IdempotencyKey            string       `json:"idempotency_key"`
	RequestHash               string       `json:"request_hash"`
	DestinationVerificationID string       `json:"destination_verification_id"`
	Destination               Address      `json:"destination"`
	GrossAmount               money.Amount `json:"gross_amount"`
	RefundAmount              money.Amount `json:"refund_amount"`
	NetworkFee                money.Amount `json:"network_fee"`
	FeeBearer                 FeeBearer    `json:"fee_bearer"`
	Status                    Status       `json:"status"`
	Approvals                 []Approval   `json:"approvals"`
	UnsignedDigest            string       `json:"unsigned_digest,omitempty"`
	UnsignedReference         string       `json:"unsigned_reference,omitempty"`
	SignedDigest              string       `json:"signed_digest,omitempty"`
	SignedReference           string       `json:"signed_reference,omitempty"`
	TransactionHash           string       `json:"transaction_hash,omitempty"`
	Version                   int64        `json:"version"`
	CreatedAt                 time.Time    `json:"created_at"`
	UpdatedAt                 time.Time    `json:"updated_at"`
}

type AuthContext struct {
	ActorID          ActorID
	Permissions      map[string]bool
	StepUpValidUntil time.Time
}

type WorkloadContext struct {
	ActorID     ActorID
	Permissions map[string]bool
}

func (w WorkloadContext) Authorize(permission string) error {
	if w.ActorID == "" || !w.Permissions[permission] {
		return ErrForbidden
	}
	return nil
}

func (a AuthContext) Authorize(permission string, now time.Time) error {
	if a.ActorID == "" || !a.Permissions[permission] || !a.StepUpValidUntil.After(now) {
		return ErrForbidden
	}
	return nil
}

type AuditCommand struct {
	ID             string
	TenantID       TenantID
	AggregateID    string
	ActorID        ActorID
	Action, Reason string
	OccurredAt     time.Time
}
type OutboxCommand struct {
	ID                     string
	TenantID               TenantID
	AggregateID, EventType string
	Payload                []byte
	OccurredAt             time.Time
}

// LedgerCommand is an immutable double-entry posting request. The repository
// commits it together with the refund state, audit record, and outbox event.
type LedgerCommand struct {
	ID, EntryType, AggregateID, DebitAccountID, CreditAccountID string
	TenantID                                                    TenantID
	AssetID                                                     AssetID
	Amount                                                      money.Amount
	OccurredAt                                                  time.Time
}
