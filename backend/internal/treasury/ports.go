package treasury

import (
	"context"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/money"
)

// Repository is tenant-scoped. Create and Update must atomically persist the
// aggregate, ledger commands, immutable audit record, and outbox records.
// Create must also lock the tenant/asset usage bucket and source/nonce
// reservations, enforcing Limits against concurrent creates.
type Repository interface {
	Create(context.Context, CreateMutation) (SweepRequest, bool, error)
	Get(context.Context, TenantID, RequestID) (SweepRequest, error)
	Update(context.Context, UpdateMutation) (SweepRequest, error)
}

type Limits struct {
	WindowStart time.Time
	WindowEnd   time.Time
	Maximum     money.Amount
}

type SourceReservation struct {
	Address  Address
	NonceRef string
}

type CreateMutation struct {
	Request SweepRequest
	Limits  Limits
	Sources []SourceReservation
	Audit   AuditCommand
	Ledger  []LedgerCommand
	Outbox  []OutboxCommand
}

type UpdateMutation struct {
	TenantID            TenantID
	RequestID           RequestID
	ExpectedVersion     int64
	Next                SweepRequest
	Audit               AuditCommand
	Ledger              []LedgerCommand
	ReleaseSources      []SourceReservation
	Outbox              []OutboxCommand
	DecisionOperation   string
	DecisionKey         string
	DecisionFingerprint [32]byte
	DecisionActor       ActorID
}

// PolicyRepository returns a versioned snapshot; requests retain its identity
// so later policy edits never silently change an approved transfer.
type PolicyRepository interface {
	ActivePolicy(context.Context, TenantID, AssetID) (Policy, error)
}

type UnsignedTransaction struct {
	RequestID       RequestID
	TenantID        TenantID
	AssetID         AssetID
	ChainID         ChainID
	Destination     Address
	Amount          money.Amount
	Fee             money.Amount
	Digest          string
	OpaqueReference string
}

type SignedTransaction struct {
	RequestID       RequestID
	UnsignedDigest  string
	SignedDigest    string
	OpaqueReference string
}

type BroadcastReceipt struct {
	SignedDigest    string
	TransactionHash string
}

// Builder receives typed, already-approved data. Signer returns only opaque
// signed-transaction references/digests; it can be backed by HSM, MPC, KMS, or
// an external custodian and never accepts or returns a private key.
type Builder interface {
	BuildUnsigned(context.Context, SweepRequest) (UnsignedTransaction, error)
}

type Signer interface {
	// Repeated calls for the same RequestID and unsigned digest must return the
	// same signed reference or a stable already-signed result.
	SignSweep(context.Context, UnsignedTransaction) (SignedTransaction, error)
}

// Broadcaster must make repeated broadcasts of the same signed digest safe.
type Broadcaster interface {
	Broadcast(context.Context, SignedTransaction) (BroadcastReceipt, error)
}

type Clock interface{ Now() time.Time }
type IDGenerator interface{ NewID() string }
