package platformadmin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/ids"
)

type memoryReplay struct {
	mu   sync.Mutex
	seen map[string]bool
}

func (m *memoryReplay) ConsumePlatformAdminAssertion(_ context.Context, audience, nonce string, _ time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.seen == nil {
		m.seen = map[string]bool{}
	}
	key := audience + "\x1f" + nonce
	if m.seen[key] {
		return false, nil
	}
	m.seen[key] = true
	return true, nil
}

type memoryRepository struct {
	mu        sync.Mutex
	now       func() time.Time
	changes   map[string]ChangeRequest
	snapshots map[string]Snapshot
	heads     map[string]Snapshot
	idem      map[string]struct {
		hash [32]byte
		body []byte
	}
	replay memoryReplay
}

func newMemoryRepository(now func() time.Time) *memoryRepository {
	return &memoryRepository{now: now, changes: map[string]ChangeRequest{}, snapshots: map[string]Snapshot{}, heads: map[string]Snapshot{}, idem: map[string]struct {
		hash [32]byte
		body []byte
	}{}}
}
func (m *memoryRepository) Ping(context.Context) error { return nil }
func (m *memoryRepository) ConsumePlatformAdminAssertion(ctx context.Context, a, n string, e time.Time) (bool, error) {
	return m.replay.ConsumePlatformAdminAssertion(ctx, a, n, e)
}
func memKey(tenant string, kind Kind, key string) string {
	return tenant + "\x1f" + string(kind) + "\x1f" + key
}
func (m *memoryRepository) run(p Principal, scope Scope, op string, idem Idempotency, out any, fn func() error) error {
	key := scope.TenantID + "\x1f" + p.ActorID + "\x1f" + op + "\x1f" + idem.Key
	if prior, ok := m.idem[key]; ok {
		if prior.hash != idem.Fingerprint {
			return ErrIdempotencyConflict
		}
		return json.Unmarshal(prior.body, out)
	}
	if err := fn(); err != nil {
		return err
	}
	body, _ := json.Marshal(out)
	m.idem[key] = struct {
		hash [32]byte
		body []byte
	}{idem.Fingerprint, body}
	return nil
}
func (m *memoryRepository) CreateDraft(_ context.Context, p Principal, input CreateInput, idem Idempotency) (out ChangeRequest, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	err = m.run(p, Scope{input.TenantID}, "create-draft", idem, &out, func() error {
		head := m.heads[memKey(input.TenantID, input.Kind, input.LogicalKey)]
		if head.Version != input.BasedOnVersion {
			return ErrConflict
		}
		version := int64(1)
		for _, c := range m.changes {
			if c.TenantID == input.TenantID && c.Kind == input.Kind && c.LogicalKey == input.LogicalKey && c.Version >= version {
				version = c.Version + 1
			}
		}
		id, _ := ids.New()
		now := m.now()
		digest := sha256.Sum256(input.Payload)
		out = ChangeRequest{ID: id, TenantID: input.TenantID, Kind: input.Kind, LogicalKey: input.LogicalKey, Version: version, BasedOnVersion: input.BasedOnVersion, Payload: bytes.Clone(input.Payload), PayloadHash: hex.EncodeToString(digest[:]), Status: StatusDraft, Reason: input.Reason, RequestedBy: p.ActorID, CreatedAt: now, UpdatedAt: now, RowVersion: 1}
		m.changes[id] = out
		return nil
	})
	return
}
func (m *memoryRepository) GetChange(_ context.Context, _ Principal, scope Scope, id string) (ChangeRequest, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.changes[id]
	if !ok || v.TenantID != scope.TenantID {
		return ChangeRequest{}, ErrNotFound
	}
	return v, nil
}
func (m *memoryRepository) ListChanges(context.Context, Principal, Scope, Kind, Status, string, int) (Page[ChangeRequest], error) {
	return Page[ChangeRequest]{Items: []ChangeRequest{}}, nil
}
func (m *memoryRepository) transition(p Principal, scope Scope, id, op string, input DecisionInput, idem Idempotency, from, to Status) (out ChangeRequest, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	err = m.run(p, scope, op, idem, &out, func() error {
		v, ok := m.changes[id]
		if !ok || v.TenantID != scope.TenantID {
			return ErrNotFound
		}
		if v.Status != from || v.RowVersion != input.ExpectedRowVersion {
			return ErrConflict
		}
		if (to == StatusApproved || to == StatusRejected) && v.RequestedBy == p.ActorID {
			return ErrForbidden
		}
		v.Status = to
		v.RowVersion++
		v.UpdatedAt = m.now()
		if to == StatusApproved {
			v.ApprovedBy = p.ActorID
		}
		if to == StatusRejected {
			v.RejectedBy = p.ActorID
		}
		m.changes[id] = v
		out = v
		return nil
	})
	return
}
func (m *memoryRepository) RequestApproval(_ context.Context, p Principal, s Scope, id string, input DecisionInput, idem Idempotency) (ChangeRequest, error) {
	return m.transition(p, s, id, "request-approval", input, idem, StatusDraft, StatusApprovalRequested)
}
func (m *memoryRepository) Decide(_ context.Context, p Principal, s Scope, id string, approve bool, input DecisionInput, idem Idempotency) (ChangeRequest, error) {
	if approve {
		return m.transition(p, s, id, "approve", input, idem, StatusApprovalRequested, StatusApproved)
	}
	return m.transition(p, s, id, "reject", input, idem, StatusApprovalRequested, StatusRejected)
}
func (m *memoryRepository) Schedule(_ context.Context, p Principal, scope Scope, id string, input ScheduleInput, idem Idempotency) (out ChangeRequest, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	err = m.run(p, scope, "schedule", idem, &out, func() error {
		v, ok := m.changes[id]
		if !ok {
			return ErrNotFound
		}
		if v.Status != StatusApproved || v.RowVersion != input.ExpectedRowVersion {
			return ErrConflict
		}
		v.Status = StatusScheduled
		v.ScheduledFor = &input.ActivateAt
		v.RowVersion++
		m.changes[id] = v
		out = v
		return nil
	})
	return
}
func (m *memoryRepository) Activate(_ context.Context, p Principal, scope Scope, id string, input ActivateInput, idem Idempotency) (out Snapshot, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	err = m.run(p, scope, "activate", idem, &out, func() error {
		v, ok := m.changes[id]
		if !ok {
			return ErrNotFound
		}
		if v.Status != StatusScheduled || v.RowVersion != input.ExpectedRowVersion {
			return ErrConflict
		}
		if v.ScheduledFor == nil || v.ScheduledFor.After(m.now()) {
			return ErrScheduledForFuture
		}
		head := m.heads[memKey(v.TenantID, v.Kind, v.LogicalKey)]
		if head.Version != v.BasedOnVersion || head.FenceToken != input.ExpectedFenceToken {
			return ErrConflict
		}
		snapshotID, _ := ids.New()
		out = Snapshot{ID: snapshotID, TenantID: v.TenantID, ChangeRequestID: v.ID, Kind: v.Kind, LogicalKey: v.LogicalKey, Version: v.Version, Payload: bytes.Clone(v.Payload), PayloadHash: v.PayloadHash, RollbackOfSnapshotID: v.RollbackOfSnapshotID, ActivatedAt: m.now(), FenceToken: head.FenceToken + 1}
		m.snapshots[out.ID] = out
		m.heads[memKey(v.TenantID, v.Kind, v.LogicalKey)] = out
		now := m.now()
		v.Status = StatusActive
		v.ActivatedAt = &now
		v.RowVersion++
		m.changes[id] = v
		return nil
	})
	return
}
func (m *memoryRepository) CreateRollback(_ context.Context, p Principal, input RollbackInput, idem Idempotency) (out ChangeRequest, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	scope := Scope{input.TenantID}
	err = m.run(p, scope, "rollback-draft", idem, &out, func() error {
		historic, ok := m.snapshots[input.SnapshotID]
		if !ok || historic.TenantID != input.TenantID {
			return ErrNotFound
		}
		head := m.heads[memKey(input.TenantID, historic.Kind, historic.LogicalKey)]
		id, _ := ids.New()
		now := m.now()
		out = ChangeRequest{ID: id, TenantID: input.TenantID, Kind: historic.Kind, LogicalKey: historic.LogicalKey, Version: head.Version + 1, BasedOnVersion: head.Version, RollbackOfSnapshotID: historic.ID, Payload: bytes.Clone(historic.Payload), PayloadHash: historic.PayloadHash, Status: StatusDraft, Reason: input.Reason, RequestedBy: p.ActorID, CreatedAt: now, UpdatedAt: now, RowVersion: 1}
		m.changes[id] = out
		return nil
	})
	return
}
func (m *memoryRepository) ListSnapshots(context.Context, Principal, Scope, Kind, string, string, int) (Page[Snapshot], error) {
	return Page[Snapshot]{Items: []Snapshot{}}, nil
}
func (m *memoryRepository) EmergencyPause(context.Context, Principal, PauseInput, Idempotency) error {
	return nil
}
