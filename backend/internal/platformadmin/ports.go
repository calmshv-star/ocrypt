package platformadmin

import (
	"context"
	"time"
)

type AssertionReplayStore interface {
	ConsumePlatformAdminAssertion(context.Context, string, string, time.Time) (bool, error)
}

type Repository interface {
	AssertionReplayStore
	Ping(context.Context) error
	CreateDraft(context.Context, Principal, CreateInput, Idempotency) (ChangeRequest, error)
	GetChange(context.Context, Principal, Scope, string) (ChangeRequest, error)
	ListChanges(context.Context, Principal, Scope, Kind, Status, string, int) (Page[ChangeRequest], error)
	RequestApproval(context.Context, Principal, Scope, string, DecisionInput, Idempotency) (ChangeRequest, error)
	Decide(context.Context, Principal, Scope, string, bool, DecisionInput, Idempotency) (ChangeRequest, error)
	Schedule(context.Context, Principal, Scope, string, ScheduleInput, Idempotency) (ChangeRequest, error)
	Activate(context.Context, Principal, Scope, string, ActivateInput, Idempotency) (Snapshot, error)
	CreateRollback(context.Context, Principal, RollbackInput, Idempotency) (ChangeRequest, error)
	ListSnapshots(context.Context, Principal, Scope, Kind, string, string, int) (Page[Snapshot], error)
	EmergencyPause(context.Context, Principal, PauseInput, Idempotency) error
}

type ActivationJob struct {
	ChangeID           string
	TenantID           string
	RowVersion         int64
	ExpectedFenceToken int64
	ClaimToken         int
}
type ActivationSchedulerRepository interface {
	ClaimDueActivations(context.Context, string, string, int, time.Duration) ([]ActivationJob, error)
	Activate(context.Context, Principal, Scope, string, ActivateInput, Idempotency) (Snapshot, error)
}

// ActiveSnapshotReader is the only runtime configuration contract. Consumers
// read the immutable snapshot selected by the fenced head and fail closed when
// no admitted active version exists.
type ActiveSnapshotReader interface {
	ActiveSnapshot(context.Context, Scope, Kind, string) (Snapshot, error)
}

// RuntimeSnapshotKey identifies one head that a runtime must consume. A
// RuntimeStateReader returns all requested heads and their pause state from a
// single database snapshot, so a worker never assembles a mixed-generation
// configuration with one query per resource.
type RuntimeSnapshotKey struct {
	Kind       Kind
	LogicalKey string
}

type RuntimeState struct {
	Snapshots []Snapshot
	Paused    map[RuntimeSnapshotKey]bool
}

type RuntimeStateReader interface {
	ActiveRuntimeState(context.Context, Scope, []RuntimeSnapshotKey) (RuntimeState, error)
}

type OutboxEvent struct {
	ID               string
	TenantID         string
	EventType        string
	AggregateType    string
	AggregateID      string
	AggregateVersion int64
	Payload          []byte
	OccurredAt       time.Time
	Attempts         int
	ClaimToken       int64
}
type OutboxStore interface {
	ClaimPlatformOutbox(context.Context, string, int, time.Duration) ([]OutboxEvent, error)
	MarkPlatformOutboxPublished(context.Context, OutboxEvent, string, time.Time) error
	ReleasePlatformOutbox(context.Context, OutboxEvent, string, time.Time) error
}

type OutboxDestination interface {
	Publish(context.Context, OutboxEvent) error
}
