package matchingadmin

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/management"
)

var (
	ErrInvalid         = errors.New("invalid matching policy request")
	ErrUnauthenticated = errors.New("matching policy authentication failed")
	ErrForbidden       = errors.New("matching policy permission denied")
	ErrNotFound        = errors.New("matching policy change not found")
	ErrConflict        = errors.New("matching policy state conflict")
	ErrIdempotency     = errors.New("matching policy idempotency conflict")
)

const (
	ScopeRead     = "matching-policies:read"
	ScopeWrite    = "matching-policies:write"
	ScopeApprove  = "matching-policies:approve"
	ScopeActivate = "matching-policies:activate"
)

type PolicyInput struct {
	AccumulatePartials       bool     `json:"accumulate_partials"`
	UnderpaymentToleranceBPS uint32   `json:"underpayment_tolerance_bps"`
	OverpaymentMode          string   `json:"overpayment_mode"`
	AcceptLateWithinGrace    bool     `json:"accept_late_within_grace"`
	RequireSameSender        bool     `json:"require_same_sender"`
	GasFreeEnabled           bool     `json:"gasfree_enabled"`
	GasFreeFeeCollectors     []string `json:"gasfree_fee_collectors"`
}

type PolicyChange struct {
	ID                       string     `json:"id"`
	ProposedVersion          int64      `json:"proposed_version"`
	AccumulatePartials       bool       `json:"accumulate_partials"`
	UnderpaymentToleranceBPS uint32     `json:"underpayment_tolerance_bps"`
	OverpaymentMode          string     `json:"overpayment_mode"`
	AcceptLateWithinGrace    bool       `json:"accept_late_within_grace"`
	RequireSameSender        bool       `json:"require_same_sender"`
	GasFreeEnabled           bool       `json:"gasfree_enabled"`
	GasFreeFeeCollectors     []string   `json:"gasfree_fee_collectors"`
	Status                   string     `json:"status"`
	CreatedBy                string     `json:"created_by"`
	RequestedBy              string     `json:"requested_by,omitempty"`
	ApprovedBy               string     `json:"approved_by,omitempty"`
	ActivatedBy              string     `json:"activated_by,omitempty"`
	RequestReason            string     `json:"request_reason,omitempty"`
	ApprovalReason           string     `json:"approval_reason,omitempty"`
	ActivationReason         string     `json:"activation_reason,omitempty"`
	ApprovedAt               *time.Time `json:"approved_at,omitempty"`
	ActivatedAt              *time.Time `json:"activated_at,omitempty"`
	EffectiveAt              *time.Time `json:"effective_at,omitempty"`
	ActivatedPolicyID        string     `json:"activated_policy_id,omitempty"`
	CreatedAt                time.Time  `json:"created_at"`
	UpdatedAt                time.Time  `json:"updated_at"`
	Version                  int64      `json:"version"`
}

type Page struct {
	Data       []PolicyChange `json:"data"`
	NextCursor string         `json:"next_cursor,omitempty"`
}

type Mutation struct {
	Version int64  `json:"version"`
	Reason  string `json:"reason"`
}

type Activation struct {
	Version     int64     `json:"version"`
	Reason      string    `json:"reason"`
	EffectiveAt time.Time `json:"effective_at"`
}

type Idempotency struct {
	Key         string
	Fingerprint [32]byte
}

type Authenticator interface {
	Authenticate(context.Context, *http.Request, []byte) (management.Principal, error)
}

type Repository interface {
	Create(context.Context, management.Principal, PolicyInput, Idempotency) (PolicyChange, bool, error)
	Get(context.Context, management.Principal, string) (PolicyChange, error)
	List(context.Context, management.Principal, string, int) (Page, error)
	RequestApproval(context.Context, management.Principal, string, Mutation, Idempotency) (PolicyChange, bool, error)
	Approve(context.Context, management.Principal, string, Mutation, Idempotency) (PolicyChange, bool, error)
	Activate(context.Context, management.Principal, string, Activation, Idempotency) (PolicyChange, bool, error)
}
