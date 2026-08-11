package refunds

import (
	"context"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/money"
)

type EvidenceRepository interface {
	Settlement(context.Context, TenantID, SettlementID) (Settlement, error)
	VerifiedDestination(context.Context, TenantID, string) (VerifiedDestination, error)
}

type PolicyRepository interface {
	ActivePolicy(context.Context, TenantID, AssetID) (Policy, error)
}

// Repository mutations atomically persist state, immutable audit/outbox rows,
// and enforce both settlement availability and the tenant daily limit under a
// lock. This prevents concurrent idempotency keys from double-refunding.
type Repository interface {
	Create(context.Context, CreateMutation) (Refund, bool, error)
	Get(context.Context, TenantID, RefundID) (Refund, error)
	Update(context.Context, UpdateMutation) (Refund, error)
}

type CreateMutation struct {
	Refund                           Refund
	MaximumRefundable                money.Amount
	LimitWindowStart, LimitWindowEnd time.Time
	DailyLimit                       money.Amount
	Audit                            AuditCommand
	Ledger                           []LedgerCommand
	Outbox                           []OutboxCommand
}

type UpdateMutation struct {
	TenantID            TenantID
	RefundID            RefundID
	ExpectedVersion     int64
	Next                Refund
	Audit               AuditCommand
	Ledger              []LedgerCommand
	ReleaseRefundable   money.Amount
	Outbox              []OutboxCommand
	DecisionOperation   string
	DecisionKey         string
	DecisionFingerprint [32]byte
	DecisionActor       ActorID
}

type UnsignedTransaction struct {
	RefundID                RefundID
	TenantID                TenantID
	SettlementID            SettlementID
	AssetID                 AssetID
	ChainID                 ChainID
	Destination             Address
	Amount                  money.Amount
	Fee                     money.Amount
	Digest, OpaqueReference string
}
type SignedTransaction struct {
	RefundID                                      RefundID
	UnsignedDigest, SignedDigest, OpaqueReference string
}
type BroadcastReceipt struct{ SignedDigest, TransactionHash string }

type Builder interface {
	BuildUnsignedRefund(context.Context, Refund) (UnsignedTransaction, error)
}
type Signer interface {
	// Repeated calls for one RefundID/unsigned digest must be idempotent.
	SignRefund(context.Context, UnsignedTransaction) (SignedTransaction, error)
}
type Broadcaster interface {
	BroadcastRefund(context.Context, SignedTransaction) (BroadcastReceipt, error)
}
type Clock interface{ Now() time.Time }
type IDGenerator interface{ NewID() string }
