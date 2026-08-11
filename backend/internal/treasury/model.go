// Package treasury contains custody orchestration policy and state machines.
//
// It deliberately contains no key material or chain-specific signing code. A
// production adapter must implement Repository mutations transactionally so
// that the aggregate, immutable audit command and outbox commands commit as a
// single unit.
package treasury

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/money"
)

var (
	ErrValidation          = errors.New("treasury validation failed")
	ErrForbidden           = errors.New("treasury operation forbidden")
	ErrStateConflict       = errors.New("treasury state conflict")
	ErrVersionConflict     = errors.New("treasury version conflict")
	ErrIdempotencyConflict = errors.New("treasury idempotency conflict")
	ErrPolicyLimit         = errors.New("treasury policy limit exceeded")
	ErrDestinationDenied   = errors.New("treasury destination is not allowlisted")
)

type TenantID string
type AssetID string
type ChainID string
type ActorID string
type RequestID string

type Address struct {
	Chain ChainID `json:"chain"`
	Value string  `json:"value"`
}

func (a Address) Validate() error {
	if strings.TrimSpace(string(a.Chain)) == "" || strings.TrimSpace(a.Value) == "" || len(a.Value) > 512 {
		return fmt.Errorf("%w: chain and canonical address are required", ErrValidation)
	}
	if a.Value != strings.TrimSpace(a.Value) {
		return fmt.Errorf("%w: address must be canonical", ErrValidation)
	}
	return nil
}

type AllowlistEntry struct {
	TenantID  TenantID  `json:"tenant_id"`
	AssetID   AssetID   `json:"asset_id"`
	Address   Address   `json:"address"`
	ValidFrom time.Time `json:"valid_from"`
	ValidTo   time.Time `json:"valid_to,omitempty"`
}

func (e AllowlistEntry) Allows(tenant TenantID, asset AssetID, address Address, at time.Time) bool {
	return e.TenantID == tenant && e.AssetID == asset && e.Address == address &&
		!at.Before(e.ValidFrom) && (e.ValidTo.IsZero() || at.Before(e.ValidTo))
}

type Policy struct {
	ID                    string           `json:"id"`
	Version               int64            `json:"version"`
	TenantID              TenantID         `json:"tenant_id"`
	AssetID               AssetID          `json:"asset_id"`
	ChainID               ChainID          `json:"chain_id"`
	Enabled               bool             `json:"enabled"`
	EmergencyPaused       bool             `json:"emergency_paused"`
	SweepThreshold        money.Amount     `json:"sweep_threshold"`
	ReserveAmount         money.Amount     `json:"reserve_amount"`
	MaximumRequestAmount  money.Amount     `json:"maximum_request_amount"`
	DailyAmountLimit      money.Amount     `json:"daily_amount_limit"`
	ApprovalThreshold     money.Amount     `json:"approval_threshold"`
	MaximumNetworkFee     money.Amount     `json:"maximum_network_fee"`
	MaximumBatchSize      int              `json:"maximum_batch_size"`
	RequireApprovalAlways bool             `json:"require_approval_always"`
	Destinations          []AllowlistEntry `json:"destinations"`
}

func (p Policy) Validate() error {
	if p.ID == "" || p.Version < 1 || p.TenantID == "" || p.AssetID == "" || p.ChainID == "" {
		return fmt.Errorf("%w: policy identity and version are required", ErrValidation)
	}
	if p.MaximumBatchSize < 1 || p.MaximumBatchSize > 1000 {
		return fmt.Errorf("%w: maximum batch size must be in [1,1000]", ErrValidation)
	}
	if p.SweepThreshold.IsZero() || p.MaximumRequestAmount.IsZero() || p.DailyAmountLimit.IsZero() {
		return fmt.Errorf("%w: positive threshold and limits are required", ErrValidation)
	}
	if p.ReserveAmount.Cmp(p.SweepThreshold) >= 0 {
		return fmt.Errorf("%w: reserve must be below sweep threshold", ErrValidation)
	}
	if p.ApprovalThreshold.Cmp(p.MaximumRequestAmount) > 0 {
		return fmt.Errorf("%w: approval threshold exceeds request limit", ErrValidation)
	}
	if p.MaximumRequestAmount.Cmp(p.DailyAmountLimit) > 0 {
		return fmt.Errorf("%w: request limit exceeds daily limit", ErrValidation)
	}
	for _, entry := range p.Destinations {
		if entry.TenantID != p.TenantID || entry.AssetID != p.AssetID || entry.Address.Chain != p.ChainID {
			return fmt.Errorf("%w: destination crosses policy tenant, asset, or chain", ErrValidation)
		}
		if err := entry.Address.Validate(); err != nil {
			return err
		}
		if !entry.ValidTo.IsZero() && !entry.ValidTo.After(entry.ValidFrom) {
			return fmt.Errorf("%w: invalid allowlist validity interval", ErrValidation)
		}
	}
	return nil
}

func (p Policy) AllowsDestination(destination Address, at time.Time) bool {
	for _, entry := range p.Destinations {
		if entry.Allows(p.TenantID, p.AssetID, destination, at) {
			return true
		}
	}
	return false
}

type Source struct {
	Address   Address      `json:"address"`
	Available money.Amount `json:"available"`
	NonceRef  string       `json:"nonce_ref"`
}

type BatchItem struct {
	Source   Address      `json:"source"`
	Amount   money.Amount `json:"amount"`
	NonceRef string       `json:"nonce_ref"`
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
	ActorID    ActorID   `json:"actor_id"`
	ApprovedAt time.Time `json:"approved_at"`
	Reason     string    `json:"reason"`
}

type SweepRequest struct {
	ID                     RequestID    `json:"id"`
	TenantID               TenantID     `json:"tenant_id"`
	AssetID                AssetID      `json:"asset_id"`
	ChainID                ChainID      `json:"chain_id"`
	PolicyID               string       `json:"policy_id"`
	PolicyVersion          int64        `json:"policy_version"`
	IdempotencyKey         string       `json:"idempotency_key"`
	RequestHash            string       `json:"request_hash"`
	CreatorID              ActorID      `json:"creator_id"`
	Destination            Address      `json:"destination"`
	Items                  []BatchItem  `json:"items"`
	Amount                 money.Amount `json:"amount"`
	FeeCap                 money.Amount `json:"fee_cap"`
	QuotedFee              money.Amount `json:"quoted_fee"`
	Status                 Status       `json:"status"`
	Approvals              []Approval   `json:"approvals"`
	UnsignedDigest         string       `json:"unsigned_digest,omitempty"`
	UnsignedTransactionRef string       `json:"unsigned_transaction_ref,omitempty"`
	SignedDigest           string       `json:"signed_digest,omitempty"`
	SignedTransactionRef   string       `json:"signed_transaction_ref,omitempty"`
	TransactionHash        string       `json:"transaction_hash,omitempty"`
	FailureCode            string       `json:"failure_code,omitempty"`
	Version                int64        `json:"version"`
	CreatedAt              time.Time    `json:"created_at"`
	UpdatedAt              time.Time    `json:"updated_at"`
}

func (r SweepRequest) Terminal() bool {
	switch r.Status {
	case StatusFinalized, StatusRejected, StatusCancelled, StatusFailed:
		return true
	default:
		return false
	}
}

type AuthContext struct {
	ActorID          ActorID
	Permissions      map[string]bool
	StepUpValidUntil time.Time
}

// WorkloadContext is for authenticated scanner/finality workers. It is
// deliberately distinct from an interactive MFA context.
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
	if a.ActorID == "" || !a.Permissions[permission] {
		return ErrForbidden
	}
	if !a.StepUpValidUntil.After(now) {
		return fmt.Errorf("%w: active step-up authentication is required", ErrForbidden)
	}
	return nil
}

// AuditCommand and OutboxCommand are append-only commands. Adapters must not
// expose update/delete methods for these records.
type AuditCommand struct {
	ID, Action, AggregateID, ActorID, Reason string
	TenantID                                 TenantID
	OccurredAt                               time.Time
}

type OutboxCommand struct {
	ID, EventType, AggregateID string
	TenantID                   TenantID
	OccurredAt                 time.Time
	Payload                    []byte
}

// LedgerCommand is an immutable double-entry posting request. The adapter
// validates both opaque account IDs against TenantID and AssetID.
type LedgerCommand struct {
	ID, EntryType, AggregateID, DebitAccountID, CreditAccountID string
	TenantID                                                    TenantID
	AssetID                                                     AssetID
	Amount                                                      money.Amount
	OccurredAt                                                  time.Time
}
