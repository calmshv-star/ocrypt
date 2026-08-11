package platformadmin

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"time"
)

var (
	ErrUnauthenticated     = errors.New("authentication required")
	ErrForbidden           = errors.New("permission denied")
	ErrStepUpRequired      = errors.New("recent MFA step-up required")
	ErrInvalid             = errors.New("invalid platform configuration")
	ErrNotFound            = errors.New("platform configuration not found")
	ErrConflict            = errors.New("platform configuration conflict")
	ErrIdempotencyConflict = errors.New("idempotency key reused with different input")
	ErrDependency          = errors.New("platform admin dependency unavailable")
	ErrScheduledForFuture  = errors.New("activation is not due")
)

type Kind string

const (
	KindTenant              Kind = "tenant"
	KindMerchantEnvironment Kind = "merchant_environment"
	KindChain               Kind = "chain"
	KindAssetContract       Kind = "asset_contract"
	KindWalletPool          Kind = "wallet_pool"
	KindRPCProvider         Kind = "rpc_provider"
	KindRateSource          Kind = "rate_source"
	KindRatePolicy          Kind = "rate_policy"
	KindFinalityPolicy      Kind = "finality_policy"
	KindMatchingPolicy      Kind = "matching_policy"
	KindQuota               Kind = "quota"
	KindNotificationChannel Kind = "notification_channel"
	KindFeatureFlag         Kind = "feature_flag"
	KindMaintenanceWindow   Kind = "maintenance_window"
)

var allKinds = map[Kind]bool{
	KindTenant: true, KindMerchantEnvironment: true, KindChain: true, KindAssetContract: true,
	KindWalletPool: true, KindRPCProvider: true, KindRateSource: true, KindRatePolicy: true,
	KindFinalityPolicy: true, KindMatchingPolicy: true, KindQuota: true,
	KindNotificationChannel: true, KindFeatureFlag: true, KindMaintenanceWindow: true,
}

type Status string

const (
	StatusDraft             Status = "draft"
	StatusApprovalRequested Status = "approval_requested"
	StatusApproved          Status = "approved"
	StatusRejected          Status = "rejected"
	StatusScheduled         Status = "scheduled"
	StatusActive            Status = "active"
	StatusSuperseded        Status = "superseded"
)

type Scope struct {
	TenantID string `json:"tenant_id,omitempty"`
}

type Grant struct {
	Permission string `json:"permission"`
	TenantID   string `json:"tenant_id,omitempty"`
}

type Principal struct {
	ActorID       string    `json:"actor_id"`
	SessionID     string    `json:"session_id"`
	Audience      string    `json:"audience"`
	StepUpAt      time.Time `json:"step_up_at,omitempty"`
	Grants        []Grant   `json:"grants"`
	ScopeTenantID string    `json:"scope_tenant_id,omitempty"`
}

func (p Principal) authorize(permission string, scope Scope) error {
	for _, grant := range p.Grants {
		if grant.Permission != permission {
			continue
		}
		if grant.TenantID == "" || grant.TenantID == scope.TenantID {
			return nil
		}
	}
	return ErrForbidden
}

type ChangeRequest struct {
	ID                   string          `json:"id"`
	TenantID             string          `json:"tenant_id,omitempty"`
	Kind                 Kind            `json:"kind"`
	LogicalKey           string          `json:"logical_key"`
	Version              int64           `json:"version"`
	BasedOnVersion       int64           `json:"based_on_version"`
	RollbackOfSnapshotID string          `json:"rollback_of_snapshot_id,omitempty"`
	Payload              json.RawMessage `json:"payload"`
	PayloadHash          string          `json:"payload_hash"`
	Status               Status          `json:"status"`
	Reason               string          `json:"reason"`
	RequestedBy          string          `json:"requested_by"`
	ApprovedBy           string          `json:"approved_by,omitempty"`
	RejectedBy           string          `json:"rejected_by,omitempty"`
	ScheduledFor         *time.Time      `json:"scheduled_for,omitempty"`
	ActivatedAt          *time.Time      `json:"activated_at,omitempty"`
	CreatedAt            time.Time       `json:"created_at"`
	UpdatedAt            time.Time       `json:"updated_at"`
	RowVersion           int64           `json:"row_version"`
}

type Snapshot struct {
	ID                   string          `json:"id"`
	TenantID             string          `json:"tenant_id,omitempty"`
	ChangeRequestID      string          `json:"change_request_id"`
	Kind                 Kind            `json:"kind"`
	LogicalKey           string          `json:"logical_key"`
	Version              int64           `json:"version"`
	Payload              json.RawMessage `json:"payload"`
	PayloadHash          string          `json:"payload_hash"`
	RollbackOfSnapshotID string          `json:"rollback_of_snapshot_id,omitempty"`
	ActivatedAt          time.Time       `json:"activated_at"`
	FenceToken           int64           `json:"fence_token"`
}

type Page[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
}

type Idempotency struct {
	Key         string
	Fingerprint [32]byte
}

func Fingerprint(value any) (Idempotency, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return Idempotency{}, err
	}
	return Idempotency{Fingerprint: sha256.Sum256(encoded)}, nil
}

type CreateInput struct {
	TenantID       string          `json:"tenant_id,omitempty"`
	Kind           Kind            `json:"kind"`
	LogicalKey     string          `json:"logical_key"`
	BasedOnVersion int64           `json:"based_on_version"`
	Payload        json.RawMessage `json:"payload"`
	Reason         string          `json:"reason"`
}

type DecisionInput struct {
	ExpectedRowVersion int64  `json:"expected_row_version"`
	Reason             string `json:"reason"`
}
type ScheduleInput struct {
	ExpectedRowVersion int64     `json:"expected_row_version"`
	ActivateAt         time.Time `json:"activate_at"`
	Reason             string    `json:"reason"`
}
type ActivateInput struct {
	ExpectedRowVersion int64  `json:"expected_row_version"`
	ExpectedFenceToken int64  `json:"expected_fence_token"`
	Reason             string `json:"reason"`
	LeaseOwner         string `json:"-"`
	LeaseToken         int    `json:"-"`
}
type RollbackInput struct {
	TenantID   string `json:"tenant_id,omitempty"`
	SnapshotID string `json:"snapshot_id"`
	Reason     string `json:"reason"`
}
type PauseInput struct {
	TenantID   string `json:"tenant_id,omitempty"`
	Kind       Kind   `json:"kind"`
	LogicalKey string `json:"logical_key"`
	Action     string `json:"action"`
	Reason     string `json:"reason"`
}
