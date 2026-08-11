// Package migrationcontrol implements the fail-closed control plane for
// inventory, shadow comparison, migration cutover and rollback.
package migrationcontrol

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
	ErrInvalid             = errors.New("invalid migration request")
	ErrNotFound            = errors.New("migration not found")
	ErrConflict            = errors.New("migration conflict")
	ErrIdempotencyConflict = errors.New("idempotency key conflict")
	ErrDependency          = errors.New("migration dependency unavailable")
	ErrSignature           = errors.New("migration manifest signature rejected")
)

const (
	PermissionRead    = "migration:read"
	PermissionRequest = "migration:request"
	PermissionApprove = "migration:approve"
	PermissionExecute = "migration:execute"
)

type State string

const (
	StateInventory       State = "inventory"
	StateValidated       State = "validated"
	StateApproved        State = "two_person_approved"
	StateImporting       State = "importing"
	StateShadow          State = "shadow"
	StateCanary          State = "canary"
	StateCutoverReady    State = "cutover_ready"
	StateCutover         State = "cutover"
	StateRollbackWindow  State = "rollback_window"
	StateRollbackPending State = "rollback_pending"
	StateRolledBack      State = "rolled_back"
	StateDecommissioned  State = "decommissioned"
)

var transitionOrder = map[State]State{
	StateInventory: StateValidated, StateValidated: StateApproved,
	StateApproved: StateImporting, StateImporting: StateShadow,
	StateShadow: StateCanary, StateCanary: StateCutoverReady,
	StateCutoverReady: StateCutover, StateCutover: StateRollbackWindow,
	StateRollbackWindow: StateDecommissioned, StateRolledBack: StateShadow,
}

func validTransition(from, to State) bool {
	if transitionOrder[from] == to {
		return true
	}
	return to == StateRollbackPending && (from == StateCanary || from == StateCutoverReady || from == StateCutover || from == StateRollbackWindow)
}

type ManifestKind string

const (
	ManifestInventory    ManifestKind = "inventory"
	ManifestDryRun       ManifestKind = "dry_run"
	ManifestCanary       ManifestKind = "canary"
	ManifestCutover      ManifestKind = "cutover"
	ManifestDecommission ManifestKind = "decommission"
)

type Profile string

const (
	ProfileGeneric      Profile = "generic"
	ProfileWalletLedger Profile = "wallet_ledger"
	ProfileJSONMD5      Profile = "json_md5"
	ProfileFormMD5      Profile = "form_md5"
)

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
	Fingerprint [32]byte
}

func NewIdempotency(key string, value any) (Idempotency, error) {
	b, err := json.Marshal(value)
	if err != nil {
		return Idempotency{}, err
	}
	return Idempotency{Key: key, Fingerprint: sha256.Sum256(b)}, nil
}

type Run struct {
	ID                   string     `json:"id"`
	TenantID             string     `json:"tenant_id"`
	SourceSystemID       string     `json:"source_system_id"`
	Profile              Profile    `json:"profile"`
	State                State      `json:"state"`
	CreateTrafficOwner   string     `json:"create_traffic_owner"`
	CallbackOwner        string     `json:"callback_owner"`
	DesiredActionVersion int64      `json:"desired_action_version"`
	ActuatorAckVersion   int64      `json:"actuator_ack_version"`
	FenceToken           int64      `json:"fence_token"`
	RowVersion           int64      `json:"row_version"`
	RollbackDeadline     *time.Time `json:"rollback_deadline,omitempty"`
	PendingAction        string     `json:"pending_action,omitempty"`
	PendingTargetState   State      `json:"pending_target_state,omitempty"`
	CreatedAt            time.Time  `json:"created_at"`
	UpdatedAt            time.Time  `json:"updated_at"`
}

type SourceDescriptor struct {
	SystemID      string    `json:"system_id"`
	BuildID       string    `json:"build_id"`
	SchemaVersion string    `json:"schema_version"`
	ExportedAt    time.Time `json:"exported_at"`
	WindowStart   time.Time `json:"window_start"`
	WindowEnd     time.Time `json:"window_end"`
}

type InventoryItem struct {
	SourceID string          `json:"source_id"`
	Digest   string          `json:"digest"`
	Data     json.RawMessage `json:"data,omitempty"`
}

type Inventory struct {
	Merchants                  []InventoryItem `json:"merchants"`
	Configurations             []InventoryItem `json:"configurations"`
	Assets                     []InventoryItem `json:"assets"`
	Chains                     []InventoryItem `json:"chains"`
	RPCProviders               []InventoryItem `json:"rpc_providers"`
	Wallets                    []InventoryItem `json:"wallets"`
	OpenOrders                 []InventoryItem `json:"open_orders"`
	PaidOrders                 []InventoryItem `json:"paid_orders"`
	ExpiredOrders              []InventoryItem `json:"expired_orders"`
	AmountReservations         []InventoryItem `json:"amount_reservations"`
	IncomingTransfers          []InventoryItem `json:"incoming_transfers"`
	UnmatchedTransfers         []InventoryItem `json:"unmatched_transfers"`
	CallbackBacklog            []InventoryItem `json:"callback_backlog"`
	ScannerCursors             []InventoryItem `json:"scanner_cursors"`
	ProviderOrders             []InventoryItem `json:"provider_orders"`
	OnChainBalanceObservations []InventoryItem `json:"on_chain_balance_observations"`
}

type CutoverEvidence struct {
	BalancesDigest        string    `json:"balances_digest"`
	OpenOrdersDigest      string    `json:"open_orders_digest"`
	PaidOrdersDigest      string    `json:"paid_orders_digest"`
	UnmatchedDigest       string    `json:"unmatched_digest"`
	CallbackBacklogDigest string    `json:"callback_backlog_digest"`
	ScannerCursorsDigest  string    `json:"scanner_cursors_digest"`
	RollbackDeadline      time.Time `json:"rollback_deadline"`
}

type DecommissionEvidence struct {
	BacklogCount           int64  `json:"backlog_count"`
	BacklogExceptionRef    string `json:"backlog_exception_ref,omitempty"`
	ArchiveDigest          string `json:"archive_digest"`
	RestoreTestReference   string `json:"restore_test_reference"`
	KeyRevocationReference string `json:"key_revocation_reference"`
}

type CanaryEvidence struct {
	Percentage  int      `json:"percentage"`
	MerchantIDs []string `json:"merchant_ids"`
	AssetIDs    []string `json:"asset_ids"`
}

type Manifest struct {
	SchemaVersion        string                `json:"schema_version"`
	ManifestID           string                `json:"manifest_id"`
	MigrationID          string                `json:"migration_id"`
	TenantID             string                `json:"tenant_id"`
	Kind                 ManifestKind          `json:"kind"`
	Profile              Profile               `json:"profile"`
	Source               SourceDescriptor      `json:"source"`
	Inventory            *Inventory            `json:"inventory,omitempty"`
	Canary               *CanaryEvidence       `json:"canary,omitempty"`
	Cutover              *CutoverEvidence      `json:"cutover,omitempty"`
	Decommission         *DecommissionEvidence `json:"decommission,omitempty"`
	UnexplainedDiffCount int64                 `json:"unexplained_diff_count"`
	Warnings             []string              `json:"warnings"`
}

type Signature struct {
	KeyID     string `json:"key_id"`
	Signature string `json:"signature"`
}

type SignedManifest struct {
	Manifest   json.RawMessage `json:"manifest"`
	Signatures []Signature     `json:"signatures"`
}

type StoredManifest struct {
	ID           string       `json:"id"`
	MigrationID  string       `json:"migration_id"`
	Kind         ManifestKind `json:"kind"`
	PayloadHash  string       `json:"payload_hash"`
	SignerKeyIDs []string     `json:"signer_key_ids"`
	CreatedAt    time.Time    `json:"created_at"`
}

type CreateRunInput struct {
	TenantID       string  `json:"tenant_id"`
	SourceSystemID string  `json:"source_system_id"`
	Profile        Profile `json:"profile"`
	Reason         string  `json:"reason"`
	DryRun         *bool   `json:"dry_run,omitempty"`
}

func (i CreateRunInput) IsDryRun() bool { return i.DryRun == nil || *i.DryRun }

type AttachManifestInput struct {
	ExpectedRowVersion int64          `json:"expected_row_version"`
	Document           SignedManifest `json:"document"`
	Reason             string         `json:"reason"`
	DryRun             *bool          `json:"dry_run,omitempty"`
}

func (i AttachManifestInput) IsDryRun() bool { return i.DryRun == nil || *i.DryRun }

type TransitionInput struct {
	TargetState        State  `json:"target_state"`
	ExpectedRowVersion int64  `json:"expected_row_version"`
	ExpectedFenceToken int64  `json:"expected_fence_token"`
	ManifestID         string `json:"manifest_id"`
	Reason             string `json:"reason"`
	DryRun             *bool  `json:"dry_run,omitempty"`
}

func (i TransitionInput) IsDryRun() bool { return i.DryRun == nil || *i.DryRun }

type TransitionRequest struct {
	ID                 string     `json:"id"`
	MigrationID        string     `json:"migration_id"`
	FromState          State      `json:"from_state"`
	TargetState        State      `json:"target_state"`
	Status             string     `json:"status"`
	RequestedBy        string     `json:"requested_by"`
	ApprovedBy         string     `json:"approved_by,omitempty"`
	ExpectedRowVersion int64      `json:"expected_row_version"`
	ExpectedFenceToken int64      `json:"expected_fence_token"`
	CreatedAt          time.Time  `json:"created_at"`
	ExpiresAt          time.Time  `json:"expires_at"`
	DecidedAt          *time.Time `json:"decided_at,omitempty"`
	Version            int64      `json:"version"`
}

type DecisionInput struct {
	ExpectedRequestVersion int64  `json:"expected_request_version"`
	Reason                 string `json:"reason"`
	DryRun                 *bool  `json:"dry_run,omitempty"`
}

func (i DecisionInput) IsDryRun() bool { return i.DryRun == nil || *i.DryRun }

type ExecuteInput struct {
	ExpectedRequestVersion int64  `json:"expected_request_version"`
	ExpectedRowVersion     int64  `json:"expected_row_version"`
	ExpectedFenceToken     int64  `json:"expected_fence_token"`
	Reason                 string `json:"reason"`
	DryRun                 *bool  `json:"dry_run,omitempty"`
}

func (i ExecuteInput) IsDryRun() bool { return i.DryRun == nil || *i.DryRun }

type DryRunReport struct {
	DryRun      bool     `json:"dry_run"`
	Admissible  bool     `json:"admissible"`
	Checks      []string `json:"checks"`
	Blockers    []string `json:"blockers"`
	PayloadHash string   `json:"payload_hash,omitempty"`
	TargetState State    `json:"target_state,omitempty"`
}

type ActuatorAckInput struct {
	ActionVersion int64  `json:"action_version"`
	FenceToken    int64  `json:"fence_token"`
	Action        string `json:"action"`
	EvidenceHash  string `json:"evidence_hash"`
	KeyID         string `json:"key_id"`
	Signature     string `json:"signature"`
}

type DesiredAction struct {
	MigrationID   string `json:"migration_id"`
	ActionVersion int64  `json:"action_version"`
	FenceToken    int64  `json:"fence_token"`
	Action        string `json:"action"`
	TargetState   State  `json:"target_state"`
}

type EntityType string

const (
	EntityMerchant           EntityType = "merchant"
	EntityConfiguration      EntityType = "configuration"
	EntityAsset              EntityType = "asset"
	EntityChain              EntityType = "chain"
	EntityRPCProvider        EntityType = "rpc_provider"
	EntityWallet             EntityType = "wallet"
	EntityOpenOrder          EntityType = "open_order"
	EntityPaidOrder          EntityType = "paid_order"
	EntityExpiredOrder       EntityType = "expired_order"
	EntityAmountReservation  EntityType = "amount_reservation"
	EntityIncomingTransfer   EntityType = "incoming_transfer"
	EntityUnmatchedTransfer  EntityType = "unmatched_transfer"
	EntityCallbackBacklog    EntityType = "callback_backlog"
	EntityScannerCursor      EntityType = "scanner_cursor"
	EntityProviderOrder      EntityType = "provider_order"
	EntityBalanceObservation EntityType = "balance_observation"
)

var entityTypes = map[EntityType]bool{
	EntityMerchant: true, EntityConfiguration: true, EntityAsset: true, EntityChain: true,
	EntityRPCProvider: true, EntityWallet: true, EntityOpenOrder: true, EntityPaidOrder: true,
	EntityExpiredOrder: true, EntityAmountReservation: true, EntityIncomingTransfer: true,
	EntityUnmatchedTransfer: true, EntityCallbackBacklog: true, EntityScannerCursor: true,
	EntityProviderOrder: true, EntityBalanceObservation: true,
}

type ShadowClassification string

const (
	ShadowEqual            ShadowClassification = "equal"
	ShadowExplained        ShadowClassification = "explained"
	ShadowMissingPlatform  ShadowClassification = "missing_platform"
	ShadowMissingSource    ShadowClassification = "missing_source"
	ShadowValueMismatch    ShadowClassification = "value_mismatch"
	ShadowIdentityConflict ShadowClassification = "identity_conflict"
)

var shadowClassifications = map[ShadowClassification]bool{
	ShadowEqual: true, ShadowExplained: true, ShadowMissingPlatform: true,
	ShadowMissingSource: true, ShadowValueMismatch: true, ShadowIdentityConflict: true,
}

func (c ShadowClassification) BlocksCutover() bool { return c != ShadowEqual && c != ShadowExplained }

type ShadowComparisonInput struct {
	SourceSequence int64                `json:"source_sequence"`
	EntityType     EntityType           `json:"entity_type"`
	SourceID       string               `json:"source_id"`
	SourceDigest   string               `json:"source_digest"`
	PlatformDigest string               `json:"platform_digest"`
	Classification ShadowClassification `json:"classification"`
	ExplanationRef string               `json:"explanation_ref,omitempty"`
	Observation    json.RawMessage      `json:"observation"`
}

type ImportItem struct {
	SourceSequence int64           `json:"source_sequence"`
	EntityType     EntityType      `json:"entity_type"`
	SourceID       string          `json:"source_id"`
	Payload        json.RawMessage `json:"payload"`
}

type WorkloadLease struct {
	WorkerID   string    `json:"worker_id"`
	LeaseToken string    `json:"lease_token"`
	FenceToken int64     `json:"fence_token"`
	LeaseUntil time.Time `json:"lease_until"`
}
