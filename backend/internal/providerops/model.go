package providerops

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
	ErrInvalid             = errors.New("invalid provider operation")
	ErrNotFound            = errors.New("provider operation not found")
	ErrConflict            = errors.New("provider operation conflict")
	ErrIdempotencyConflict = errors.New("idempotency key reused with different input")
	ErrDependency          = errors.New("provider operations dependency unavailable")
	ErrQuorumUnavailable   = errors.New("healthy provider quorum unavailable")
	ErrLeaseLost           = errors.New("provider health lease lost")
)

type ProviderKind string

const (
	ProviderOnChain ProviderKind = "on_chain"
	ProviderHosted  ProviderKind = "hosted"
)

type BindingStatus string

const (
	BindingActive   BindingStatus = "active"
	BindingPaused   BindingStatus = "paused"
	BindingDisabled BindingStatus = "disabled"
)

type Operation string

const (
	OperationHealth            Operation = "health"
	OperationHead              Operation = "head"
	OperationRange             Operation = "range"
	OperationTransactionLookup Operation = "transaction_lookup"
	OperationTransferVerify    Operation = "transfer_verify"
	OperationCreate            Operation = "create"
	OperationStatus            Operation = "status"
	OperationCancel            Operation = "cancel"
	OperationRefund            Operation = "refund"
	OperationReconciliation    Operation = "reconciliation"
)

var operations = map[Operation]bool{
	OperationHealth: true, OperationHead: true, OperationRange: true,
	OperationTransactionLookup: true, OperationTransferVerify: true,
	OperationCreate: true, OperationStatus: true, OperationCancel: true,
	OperationRefund: true, OperationReconciliation: true,
}

type CircuitState string

const (
	CircuitClosed   CircuitState = "closed"
	CircuitOpen     CircuitState = "open"
	CircuitHalfOpen CircuitState = "half_open"
)

type ErrorCategory string

const (
	ErrorNone              ErrorCategory = "none"
	ErrorTimeout           ErrorCategory = "timeout"
	ErrorDNS               ErrorCategory = "dns"
	ErrorTLS               ErrorCategory = "tls"
	ErrorConnect           ErrorCategory = "connect"
	ErrorRateLimited       ErrorCategory = "rate_limited"
	ErrorAuthRejected      ErrorCategory = "auth_rejected"
	ErrorUpstream4xx       ErrorCategory = "upstream_4xx"
	ErrorUpstream5xx       ErrorCategory = "upstream_5xx"
	ErrorInvalidResponse   ErrorCategory = "invalid_response"
	ErrorChainMismatch     ErrorCategory = "chain_mismatch"
	ErrorGenesisMismatch   ErrorCategory = "genesis_mismatch"
	ErrorStaleHead         ErrorCategory = "stale_head"
	ErrorDivergentResponse ErrorCategory = "divergent_response"
	ErrorPolicyDenied      ErrorCategory = "policy_denied"
)

var errorCategories = map[ErrorCategory]bool{
	ErrorNone: true, ErrorTimeout: true, ErrorDNS: true, ErrorTLS: true,
	ErrorConnect: true, ErrorRateLimited: true, ErrorAuthRejected: true,
	ErrorUpstream4xx: true, ErrorUpstream5xx: true, ErrorInvalidResponse: true,
	ErrorChainMismatch: true, ErrorGenesisMismatch: true, ErrorStaleHead: true,
	ErrorDivergentResponse: true, ErrorPolicyDenied: true,
}

type Scope struct {
	TenantID string `json:"tenant_id,omitempty"`
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

// Binding is the deliberately secret-free operator view. It never includes an
// upstream URL, credential reference, account address, or raw provider error.
type Binding struct {
	ID           string        `json:"id"`
	ProviderKind ProviderKind  `json:"provider_kind"`
	ProviderID   string        `json:"provider_id"`
	TenantID     string        `json:"tenant_id,omitempty"`
	MerchantID   string        `json:"merchant_id,omitempty"`
	ChainID      string        `json:"chain_id,omitempty"`
	Status       BindingStatus `json:"status"`
	Version      int64         `json:"version"`
	UpdatedAt    time.Time     `json:"updated_at"`
	Health       []HealthState `json:"health"`
}

type HealthState struct {
	Operation      Operation     `json:"operation"`
	State          CircuitState  `json:"state"`
	ErrorCategory  ErrorCategory `json:"error_category"`
	LastSuccessAt  *time.Time    `json:"last_success_at,omitempty"`
	LastObservedAt *time.Time    `json:"last_observed_at,omitempty"`
	LagBlocks      *int64        `json:"lag_blocks,omitempty"`
	Version        int64         `json:"version"`
}

type Policy struct {
	BindingID         string
	TenantID          string
	Operation         Operation
	Timeout           time.Duration
	MaxAttempts       int
	Backoff           time.Duration
	RateLimit         int
	RateWindow        time.Duration
	MaxHealthAge      time.Duration
	MaxLagBlocks      uint64
	FailureThreshold  int
	OpenFor           time.Duration
	HalfOpenSuccesses int
	Priority          int
	FailureDomain     string
	Version           int64
}

type Candidate struct {
	BindingID          string
	ProviderID         string
	ProviderKind       ProviderKind
	TenantID           string
	MerchantID         string
	ChainID            string
	ConfigLogicalKey   string
	PlatformSnapshotID string
	ProbeReference     string
	Status             BindingStatus
	Policy             Policy
	Circuit            Circuit
}

type Circuit struct {
	State               CircuitState
	ConsecutiveFailures int
	HalfOpenSuccesses   int
	OpenedUntil         *time.Time
	LeaseOwner          string
	LeaseToken          int64
	LeaseUntil          *time.Time
	LastSuccessAt       *time.Time
	LastObservedAt      *time.Time
	FenceToken          int64
	Version             int64
}

type AdmissionRequest struct {
	Scope       Scope
	Kind        ProviderKind
	ProviderIDs []string
	Operation   Operation
	Quorum      int
	Now         time.Time
}

type Admission struct {
	Candidates []Candidate
	Quorum     int
}

type ChangeStatus string

const (
	ChangePending   ChangeStatus = "pending_approval"
	ChangeCompleted ChangeStatus = "completed"
	ChangeRejected  ChangeStatus = "rejected"
	ChangeExpired   ChangeStatus = "expired"
)

type ChangeRequest struct {
	ID                     string        `json:"id"`
	BindingID              string        `json:"binding_id"`
	TenantID               string        `json:"tenant_id,omitempty"`
	RequestedStatus        BindingStatus `json:"requested_status"`
	ExpectedBindingVersion int64         `json:"expected_binding_version"`
	Status                 ChangeStatus  `json:"status"`
	Reason                 string        `json:"reason"`
	RequestedBy            string        `json:"requested_by"`
	ApprovedBy             string        `json:"approved_by,omitempty"`
	RejectedBy             string        `json:"rejected_by,omitempty"`
	DecisionReason         string        `json:"decision_reason,omitempty"`
	CreatedAt              time.Time     `json:"created_at"`
	ExpiresAt              time.Time     `json:"expires_at"`
	DecidedAt              *time.Time    `json:"decided_at,omitempty"`
	UpdatedAt              time.Time     `json:"updated_at"`
	Version                int64         `json:"version"`
}

type HostedPolicyStatus string

const (
	HostedPolicyPending      HostedPolicyStatus = "pending_approval"
	HostedPolicyPendingProbe HostedPolicyStatus = "approved_pending_probe"
	HostedPolicyActive       HostedPolicyStatus = "active"
	HostedPolicyRejected     HostedPolicyStatus = "rejected"
	HostedPolicySuperseded   HostedPolicyStatus = "superseded"
	HostedPolicyExpired      HostedPolicyStatus = "expired"
)

type PolicyParameters struct {
	TimeoutMS           int    `json:"timeout_ms"`
	MaxAttempts         int    `json:"max_attempts"`
	BackoffMS           int    `json:"backoff_ms"`
	RateLimit           int    `json:"rate_limit"`
	RateWindowSeconds   int    `json:"rate_window_seconds"`
	MaxHealthAgeSeconds int    `json:"max_health_age_seconds"`
	FailureThreshold    int    `json:"failure_threshold"`
	OpenSeconds         int    `json:"open_seconds"`
	HalfOpenSuccesses   int    `json:"half_open_successes"`
	Priority            int    `json:"priority"`
	MaxLagBlocks        uint64 `json:"max_lag_blocks"`
	FailureDomain       string `json:"failure_domain"`
}

type HostedPolicyVersion struct {
	ID                     string                         `json:"id"`
	BindingID              string                         `json:"binding_id"`
	TenantID               string                         `json:"tenant_id"`
	PolicyVersion          int64                          `json:"policy_version"`
	Policies               map[Operation]PolicyParameters `json:"policies"`
	PayloadHash            string                         `json:"payload_hash"`
	Status                 HostedPolicyStatus             `json:"status"`
	ExpectedBindingVersion int64                          `json:"expected_binding_version"`
	Reason                 string                         `json:"reason"`
	RequestedBy            string                         `json:"requested_by"`
	ApprovedBy             string                         `json:"approved_by,omitempty"`
	RejectedBy             string                         `json:"rejected_by,omitempty"`
	DecisionReason         string                         `json:"decision_reason,omitempty"`
	CreatedAt              time.Time                      `json:"created_at"`
	ExpiresAt              time.Time                      `json:"expires_at"`
	DecidedAt              *time.Time                     `json:"decided_at,omitempty"`
	ActivatedAt            *time.Time                     `json:"activated_at,omitempty"`
	UpdatedAt              time.Time                      `json:"updated_at"`
	RowVersion             int64                          `json:"row_version"`
}

type Page[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
}

type RequestChangeInput struct {
	TenantID               string        `json:"tenant_id,omitempty"`
	BindingID              string        `json:"binding_id"`
	RequestedStatus        BindingStatus `json:"requested_status"`
	ExpectedBindingVersion int64         `json:"expected_binding_version"`
	Reason                 string        `json:"reason"`
}

type DecideInput struct {
	ExpectedRequestVersion int64  `json:"expected_request_version"`
	Reason                 string `json:"reason"`
}

type RequestHostedPolicyInput struct {
	TenantID                string                         `json:"tenant_id,omitempty"`
	BindingID               string                         `json:"binding_id"`
	ExpectedBindingVersion  int64                          `json:"expected_binding_version"`
	Policies                map[Operation]PolicyParameters `json:"policies"`
	BootstrapProbeReference string                         `json:"-"`
	Reason                  string                         `json:"reason"`
}

type Idempotency struct {
	Key         string
	Fingerprint [32]byte
}

func NewIdempotency(key string, input any) (Idempotency, error) {
	encoded, err := json.Marshal(input)
	if err != nil {
		return Idempotency{}, err
	}
	return Idempotency{Key: key, Fingerprint: sha256.Sum256(encoded)}, nil
}

type Probe struct {
	Candidate  Candidate
	LeaseOwner string
	LeaseToken int64
	FenceToken int64
}

type Observation struct {
	BindingID      string
	TenantID       string
	Operation      Operation
	LeaseOwner     string
	LeaseToken     int64
	FenceToken     int64
	Success        bool
	Error          ErrorCategory
	Latency        time.Duration
	LagBlocks      *int64
	ObservedAt     time.Time
	HeadHeight     *uint64
	HeadObservedAt *time.Time
}

// WorkerStatus is intentionally aggregate-only so readiness and metrics cannot
// expose provider, tenant or upstream identities.
type WorkerStatus struct {
	Ready                bool
	AdmissiblePeerGroups int64
	OpenCircuits         int64
}

// HostedProbeTarget is private worker input. It must never be serialized to an
// operator response, metric, audit detail or log.
type HostedProbeTarget struct {
	ProviderID, TenantID, MerchantID, AdapterKind, APIOrigin      string   `json:"-"`
	CreatePath, CancelPath, StatusPath, RefundPath, ReconcilePath string   `json:"-"`
	PaymentURLOrigins                                             []string `json:"-"`
	CredentialRef, APIKeyID, CallbackSecretRef, CallbackKeyID     string   `json:"-"`
	SignatureScheme, AssetID, Currency, Status, ProviderReference string   `json:"-"`
	AssetDecimals                                                 uint8    `json:"-"`
}
