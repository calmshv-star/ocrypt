package providerconfig

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
	ErrInvalid             = errors.New("invalid provider configuration")
	ErrNotFound            = errors.New("provider configuration not found")
	ErrConflict            = errors.New("provider configuration conflict")
	ErrIdempotencyConflict = errors.New("idempotency key reused with different input")
	ErrDependency          = errors.New("provider configuration dependency unavailable")
	ErrLeaseLost           = errors.New("provider configuration probe lease lost")
)

type Status string

const (
	StatusPending      Status = "pending_approval"
	StatusPendingProbe Status = "approved_pending_probe"
	StatusActive       Status = "active"
	StatusRejected     Status = "rejected"
	StatusSuperseded   Status = "superseded"
	StatusExpired      Status = "expired"
	StatusProbeFailed  Status = "probe_failed"
	StatusLegacy       Status = "legacy_unadmitted"
	StatusLegacyOld    Status = "legacy_superseded"
)

type ChangeKind string

const (
	ChangeProvision ChangeKind = "provision"
	ChangeRotate    ChangeKind = "rotate"
	ChangeRollback  ChangeKind = "rollback"
	ChangeDisable   ChangeKind = "disable"
)

type Grant struct {
	Permission string
	TenantID   string
}

type Principal struct {
	ActorID   string
	SessionID string
	StepUpAt  time.Time
	Grants    []Grant
}

type Scope struct{ TenantID string }

// ManifestInput is accepted only on the write path. External file references
// and the bootstrap reference cannot be serialized into public responses,
// idempotency response bodies, logs, metrics, audit, or outbox payloads.
type ManifestInput struct {
	ChangeKind             ChangeKind `json:"change_kind"`
	AdapterKind            string     `json:"adapter_kind"`
	APIOrigin              string     `json:"-"`
	CreatePath             string     `json:"-"`
	CancelPath             string     `json:"-"`
	StatusPath             string     `json:"-"`
	RefundPath             string     `json:"-"`
	ReconcilePath          string     `json:"-"`
	PaymentURLOrigins      []string   `json:"-"`
	APICredentialRef       string     `json:"-"`
	APIKeyID               string     `json:"api_key_id"`
	CallbackSecretRef      string     `json:"-"`
	CallbackKeyID          string     `json:"callback_key_id"`
	SignatureScheme        string     `json:"signature_scheme"`
	AssetID                string     `json:"asset_id"`
	AssetDecimals          int        `json:"asset_decimals"`
	Currency               string     `json:"currency"`
	CallbackOverlapSeconds int        `json:"callback_overlap_seconds"`
	ProbeReference         string     `json:"-"`
}

type RequestInput struct {
	TenantID            string
	MerchantID          string
	ProviderID          string
	ExpectedHeadVersion int64
	Manifest            ManifestInput
	Reason              string
}

type DecideInput struct {
	ExpectedRowVersion int64
	Reason             string
}

// Version is the closed, secret-free operator projection. It deliberately
// omits origins, endpoint paths, external file references and probe references.
type Version struct {
	ID                     string     `json:"id"`
	ProviderID             string     `json:"provider_id"`
	TenantID               string     `json:"tenant_id"`
	MerchantID             string     `json:"merchant_id"`
	ManifestVersion        int64      `json:"manifest_version"`
	ChangeKind             ChangeKind `json:"change_kind"`
	ExpectedHeadVersion    int64      `json:"expected_head_version"`
	Status                 Status     `json:"status"`
	AdapterKind            string     `json:"adapter_kind"`
	AssetID                string     `json:"asset_id"`
	AssetDecimals          int        `json:"asset_decimals"`
	Currency               string     `json:"currency"`
	APIKeyID               string     `json:"api_key_id"`
	CallbackKeyID          string     `json:"callback_key_id"`
	CallbackOverlapSeconds int        `json:"callback_overlap_seconds"`
	PayloadHash            string     `json:"payload_hash"`
	Reason                 string     `json:"reason"`
	RequestedBy            string     `json:"requested_by"`
	ApprovedBy             *string    `json:"approved_by,omitempty"`
	RejectedBy             *string    `json:"rejected_by,omitempty"`
	DecisionReason         *string    `json:"decision_reason,omitempty"`
	CreatedAt              time.Time  `json:"created_at"`
	ExpiresAt              time.Time  `json:"expires_at"`
	DecidedAt              *time.Time `json:"decided_at,omitempty"`
	ActivatedAt            *time.Time `json:"activated_at,omitempty"`
	CallbackAcceptUntil    *time.Time `json:"callback_accept_until,omitempty"`
	ProbeResponseDigest    *string    `json:"probe_response_digest,omitempty"`
	ProbeTLSSPKIDigest     *string    `json:"probe_tls_spki_digest,omitempty"`
	ProbeObservedAt        *time.Time `json:"probe_observed_at,omitempty"`
	HeadVersion            int64      `json:"head_version"`
	RowVersion             int64      `json:"row_version"`
}

type Page struct {
	Items      []Version `json:"items"`
	NextCursor string    `json:"next_cursor,omitempty"`
}

type Idempotency struct {
	Key         string
	Fingerprint [32]byte
}

func NewIdempotency(key string, input any) (Idempotency, error) {
	body, err := json.Marshal(input)
	if err != nil {
		return Idempotency{}, err
	}
	return Idempotency{Key: key, Fingerprint: sha256.Sum256(body)}, nil
}

type ProbeTarget struct {
	ManifestID, TenantID, MerchantID, ProviderID string `json:"-"`
	ManifestVersion, LeaseToken                  int64  `json:"-"`
	APIOrigin, StatusPath, APICredentialRef      string `json:"-"`
	APIKeyID, ProbeReference, AdapterKind        string `json:"-"`
	AssetID                                      string `json:"-"`
	AssetDecimals                                int    `json:"-"`
}

type ProbeResult struct {
	ManifestID     string
	Owner          string
	LeaseToken     int64
	Success        bool
	ErrorCategory  string
	ResponseDigest [32]byte
	TLSSPKIDigest  [32]byte
	ObservedAt     time.Time
}
