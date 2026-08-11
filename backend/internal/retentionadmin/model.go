package retentionadmin

import (
	"context"
	"crypto/sha256"
	"errors"
	"time"
)

var (
	ErrUnauthenticated     = errors.New("authentication required")
	ErrForbidden           = errors.New("permission denied")
	ErrStepUpRequired      = errors.New("recent MFA step-up required")
	ErrInvalid             = errors.New("invalid retention control request")
	ErrNotFound            = errors.New("retention control resource not found")
	ErrConflict            = errors.New("retention control conflict")
	ErrIdempotencyConflict = errors.New("idempotency key reused with different input")
	ErrDependency          = errors.New("retention control dependency unavailable")
	ErrLeaseLost           = errors.New("retention control lease lost")
)

type DataClass string

const (
	CallbackEventBody   DataClass = "callback_event_body"
	EventHistoryPayload DataClass = "event_history_payload"
	PublishedOutbox     DataClass = "published_outbox_payload"
)

func (c DataClass) Valid() bool {
	return c == CallbackEventBody || c == EventHistoryPayload || c == PublishedOutbox
}

func (c DataClass) Prunable() bool { return c == PublishedOutbox }

type Scope struct {
	TenantID string `json:"tenant_id"`
}

type Grant struct {
	Permission string `json:"permission"`
	TenantID   string `json:"tenant_id,omitempty"`
}

type Principal struct {
	ActorID   string    `json:"actor_id"`
	SessionID string    `json:"session_id"`
	StepUpAt  time.Time `json:"step_up_at,omitempty"`
	Grants    []Grant   `json:"grants"`
}

func (p Principal) authorize(permission string, scope Scope) error {
	for _, grant := range p.Grants {
		if grant.Permission == permission && (grant.TenantID == "" || grant.TenantID == scope.TenantID) {
			return nil
		}
	}
	return ErrForbidden
}

type Idempotency struct {
	Key         string
	Fingerprint [sha256.Size]byte
}

type PolicyProposal struct {
	ArchiveAfterDays int  `json:"archive_after_days"`
	PruneGraceDays   int  `json:"prune_grace_days"`
	ObjectLockDays   int  `json:"object_lock_days"`
	PruneEnabled     bool `json:"prune_enabled"`
}

type EffectivePolicy struct {
	ID               string     `json:"id"`
	TenantID         string     `json:"tenant_id"`
	DataClass        DataClass  `json:"data_class"`
	Version          int64      `json:"version"`
	ArchiveAfterDays int        `json:"archive_after_days"`
	PruneGraceDays   int        `json:"prune_grace_days"`
	ObjectLockDays   int        `json:"object_lock_days"`
	PruneEnabled     bool       `json:"prune_enabled"`
	EffectiveAt      time.Time  `json:"effective_at"`
	PolicyDigest     string     `json:"policy_digest"`
	HeadFence        int64      `json:"head_fence"`
	LastActivatedAt  *time.Time `json:"last_activated_at,omitempty"`
}

type PolicyStatus string

const (
	PolicyPending   PolicyStatus = "pending_approval"
	PolicyScheduled PolicyStatus = "scheduled"
	PolicyActive    PolicyStatus = "active"
	PolicyRejected  PolicyStatus = "rejected"
	PolicyConflict  PolicyStatus = "conflict"
	PolicyExpired   PolicyStatus = "expired"
)

type PolicyChange struct {
	ID                       string         `json:"id"`
	TenantID                 string         `json:"tenant_id"`
	DataClass                DataClass      `json:"data_class"`
	ExpectedEffectiveVersion int64          `json:"expected_effective_version"`
	ExpectedHeadFence        int64          `json:"expected_head_fence"`
	Proposal                 PolicyProposal `json:"proposal"`
	Status                   PolicyStatus   `json:"status"`
	Reason                   string         `json:"reason"`
	RequestedBy              string         `json:"requested_by"`
	ApprovedBy               string         `json:"approved_by,omitempty"`
	RejectedBy               string         `json:"rejected_by,omitempty"`
	DecisionReason           string         `json:"decision_reason,omitempty"`
	ScheduledFor             time.Time      `json:"scheduled_for"`
	ExpiresAt                time.Time      `json:"expires_at"`
	ApprovedAt               *time.Time     `json:"approved_at,omitempty"`
	DecidedAt                *time.Time     `json:"decided_at,omitempty"`
	ActivatedAt              *time.Time     `json:"activated_at,omitempty"`
	CreatedAt                time.Time      `json:"created_at"`
	UpdatedAt                time.Time      `json:"updated_at"`
	RowVersion               int64          `json:"row_version"`
}

type HoldScope string

const (
	HoldTenant   HoldScope = "tenant"
	HoldMerchant HoldScope = "merchant"
	HoldRecord   HoldScope = "record"
)

type LegalHold struct {
	ID             string     `json:"id"`
	TenantID       string     `json:"tenant_id"`
	DataClass      DataClass  `json:"data_class"`
	ScopeType      HoldScope  `json:"scope_type"`
	MerchantID     string     `json:"merchant_id,omitempty"`
	SourceTable    string     `json:"source_table,omitempty"`
	SourceRecordID string     `json:"source_record_id,omitempty"`
	CaseReference  string     `json:"case_reference"`
	Reason         string     `json:"reason"`
	CreatedBy      string     `json:"created_by"`
	CreatedAt      time.Time  `json:"created_at"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
	ReleasedAt     *time.Time `json:"released_at,omitempty"`
	ReleasedBy     string     `json:"released_by,omitempty"`
	ExpiredAt      *time.Time `json:"expired_at,omitempty"`
	Version        int64      `json:"version"`
}

func (h LegalHold) State() string {
	if h.ReleasedAt != nil {
		return "released"
	}
	if h.ExpiredAt != nil {
		return "expired"
	}
	return "active"
}

type ReleaseStatus string

const (
	ReleasePending   ReleaseStatus = "pending_approval"
	ReleaseCompleted ReleaseStatus = "completed"
	ReleaseRejected  ReleaseStatus = "rejected"
	ReleaseConflict  ReleaseStatus = "conflict"
	ReleaseExpired   ReleaseStatus = "expired"
)

type HoldReleaseRequest struct {
	ID              string        `json:"id"`
	TenantID        string        `json:"tenant_id"`
	HoldID          string        `json:"hold_id"`
	ExpectedVersion int64         `json:"expected_hold_version"`
	Status          ReleaseStatus `json:"status"`
	Reason          string        `json:"reason"`
	RequestedBy     string        `json:"requested_by"`
	ApprovedBy      string        `json:"approved_by,omitempty"`
	RejectedBy      string        `json:"rejected_by,omitempty"`
	DecisionReason  string        `json:"decision_reason,omitempty"`
	CreatedAt       time.Time     `json:"created_at"`
	ExpiresAt       time.Time     `json:"expires_at"`
	DecidedAt       *time.Time    `json:"decided_at,omitempty"`
	RowVersion      int64         `json:"row_version"`
}

type ArchiveBatchEvidence struct {
	ID                   string     `json:"id"`
	DataClass            DataClass  `json:"data_class"`
	PolicyVersion        int64      `json:"policy_version"`
	Status               string     `json:"status"`
	ItemCount            int64      `json:"item_count"`
	ObjectSHA256         string     `json:"object_sha256,omitempty"`
	ManifestSHA256       string     `json:"manifest_sha256,omitempty"`
	SigningKeyID         string     `json:"signing_key_id,omitempty"`
	ObjectRetentionUntil *time.Time `json:"object_retention_until,omitempty"`
	VerifiedAt           *time.Time `json:"verified_at,omitempty"`
	PrunedAt             *time.Time `json:"pruned_at,omitempty"`
	CreatedAt            time.Time  `json:"created_at"`
}

type TombstoneEvidence struct {
	DataClass      DataClass `json:"data_class"`
	SourceTable    string    `json:"source_table"`
	SourceRecordID string    `json:"source_record_id"`
	MerchantID     string    `json:"merchant_id"`
	OriginalSHA256 string    `json:"original_sha256"`
	BatchID        string    `json:"batch_id"`
	ArchivedAt     time.Time `json:"archived_at"`
}

type SchedulerHealth struct {
	LastCycleAt      *time.Time `json:"last_cycle_at,omitempty"`
	DuePolicyChanges int64      `json:"due_policy_changes"`
	DueHoldReleases  int64      `json:"due_hold_releases"`
	DueHoldExpiries  int64      `json:"due_hold_expiries"`
	Ready            bool       `json:"ready"`
}

type Page[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
}

type RequestPolicyInput struct {
	TenantID                 string
	DataClass                DataClass
	ExpectedEffectiveVersion int64
	ExpectedHeadFence        int64
	Proposal                 PolicyProposal
	ScheduledFor             time.Time
	Reason                   string
}

type DecisionInput struct {
	ExpectedRowVersion int64
	Reason             string
}

type CreateHoldInput struct {
	TenantID       string
	DataClass      DataClass
	ScopeType      HoldScope
	MerchantID     string
	SourceTable    string
	SourceRecordID string
	CaseReference  string
	Reason         string
	ExpiresAt      *time.Time
}

type RequestReleaseInput struct {
	TenantID            string
	HoldID              string
	ExpectedHoldVersion int64
	Reason              string
}

type Repository interface {
	PingControl(context.Context) error
	ListPolicies(context.Context, Principal, Scope) ([]EffectivePolicy, error)
	ListPolicyChanges(context.Context, Principal, Scope, string, int) (Page[PolicyChange], error)
	ListHolds(context.Context, Principal, Scope, string, int) (Page[LegalHold], error)
	ListReleaseRequests(context.Context, Principal, Scope, string, int) (Page[HoldReleaseRequest], error)
	ListBatches(context.Context, Principal, Scope, string, int) (Page[ArchiveBatchEvidence], error)
	ListTombstones(context.Context, Principal, Scope, string, int) (Page[TombstoneEvidence], error)
	RequestPolicy(context.Context, Principal, RequestPolicyInput, Idempotency) (PolicyChange, error)
	DecidePolicy(context.Context, Principal, Scope, string, bool, DecisionInput, Idempotency) (PolicyChange, error)
	CreateHold(context.Context, Principal, CreateHoldInput, Idempotency) (LegalHold, error)
	RequestHoldRelease(context.Context, Principal, RequestReleaseInput, Idempotency) (HoldReleaseRequest, error)
	DecideHoldRelease(context.Context, Principal, Scope, string, bool, DecisionInput, Idempotency) (HoldReleaseRequest, error)
	AdvanceDue(context.Context, string, int) (int, error)
}
