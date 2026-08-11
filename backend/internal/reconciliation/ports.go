package reconciliation

import (
	"context"
	"time"
)

type SnapshotProvider interface {
	Snapshot(context.Context, TenantID, AssetID, time.Time) (BalanceSnapshot, error)
	IntegritySnapshots(context.Context, TenantID, AssetID, time.Time) ([]IntegritySnapshot, error)
}

// Repository mutations must store aggregate, items, immutable audit, and
// outbox records in one transaction. All reads/writes are tenant-qualified.
type Repository interface {
	Create(context.Context, CreateMutation) (Run, bool, error)
	Get(context.Context, TenantID, RunID) (Run, error)
	Update(context.Context, UpdateMutation) (Run, error)
}
type CreateMutation struct {
	Run    Run
	Audit  AuditCommand
	Outbox []OutboxCommand
}
type UpdateMutation struct {
	TenantID            TenantID
	RunID               RunID
	ExpectedVersion     int64
	Next                Run
	Audit               AuditCommand
	Outbox              []OutboxCommand
	DecisionOperation   string
	DecisionKey         string
	DecisionFingerprint [32]byte
	DecisionActor       ActorID
}
type Clock interface{ Now() time.Time }
type IDGenerator interface{ NewID() string }
