package retentionadmin

import (
	"bytes"
	"context"
	"sort"
	"sync"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/ids"
)

// MemoryRepository is a concurrency-safe unit fixture. Production composition
// uses PostgreSQL so DB-clock fences, RLS, audit and outbox remain mandatory.
type MemoryRepository struct {
	mu       sync.Mutex
	now      func() time.Time
	policies map[string]EffectivePolicy
	changes  map[string]PolicyChange
	holds    map[string]LegalHold
	releases map[string]HoldReleaseRequest
	idem     map[string]memoryIdempotency
}

type memoryIdempotency struct {
	hash     [32]byte
	resource string
}

func NewMemoryRepository(now func() time.Time) *MemoryRepository {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &MemoryRepository{now: now, policies: map[string]EffectivePolicy{}, changes: map[string]PolicyChange{}, holds: map[string]LegalHold{}, releases: map[string]HoldReleaseRequest{}, idem: map[string]memoryIdempotency{}}
}

func (r *MemoryRepository) PingControl(context.Context) error { return nil }

func (r *MemoryRepository) reserve(principal Principal, tenant, operation string, idem Idempotency, id string) (string, error) {
	key := tenant + "\x1f" + principal.ActorID + "\x1f" + operation + "\x1f" + idem.Key
	if prior, ok := r.idem[key]; ok {
		if !bytes.Equal(prior.hash[:], idem.Fingerprint[:]) {
			return "", ErrIdempotencyConflict
		}
		return prior.resource, nil
	}
	r.idem[key] = memoryIdempotency{hash: idem.Fingerprint, resource: id}
	return id, nil
}

func policyKey(tenant string, class DataClass) string { return tenant + "\x1f" + string(class) }

func (r *MemoryRepository) ListPolicies(_ context.Context, _ Principal, scope Scope) ([]EffectivePolicy, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []EffectivePolicy
	for _, value := range r.policies {
		if value.TenantID == scope.TenantID {
			out = append(out, value)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DataClass < out[j].DataClass })
	return out, nil
}

func memoryPage[T any](values []T, cursor string, limit int, id func(T) string) Page[T] {
	sort.Slice(values, func(i, j int) bool { return id(values[i]) < id(values[j]) })
	filtered := values[:0]
	for _, value := range values {
		if cursor == "" || id(value) > cursor {
			filtered = append(filtered, value)
		}
	}
	out := Page[T]{Items: filtered}
	if len(out.Items) > limit {
		out.NextCursor = id(out.Items[limit-1])
		out.Items = out.Items[:limit]
	}
	return out
}

func (r *MemoryRepository) ListPolicyChanges(_ context.Context, _ Principal, scope Scope, cursor string, limit int) (Page[PolicyChange], error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var values []PolicyChange
	for _, value := range r.changes {
		if value.TenantID == scope.TenantID {
			values = append(values, value)
		}
	}
	return memoryPage(values, cursor, limit, func(value PolicyChange) string { return value.ID }), nil
}

func (r *MemoryRepository) ListHolds(_ context.Context, principal Principal, scope Scope, cursor string, limit int) (Page[LegalHold], error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	showCaseReference := principal.authorize("retention:hold_create", scope) == nil || principal.authorize("retention:hold_release", scope) == nil
	var values []LegalHold
	for _, value := range r.holds {
		if value.TenantID == scope.TenantID {
			if !showCaseReference {
				value.CaseReference = ""
			}
			values = append(values, value)
		}
	}
	return memoryPage(values, cursor, limit, func(value LegalHold) string { return value.ID }), nil
}

func (r *MemoryRepository) ListReleaseRequests(_ context.Context, _ Principal, scope Scope, cursor string, limit int) (Page[HoldReleaseRequest], error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var values []HoldReleaseRequest
	for _, value := range r.releases {
		if value.TenantID == scope.TenantID {
			values = append(values, value)
		}
	}
	return memoryPage(values, cursor, limit, func(value HoldReleaseRequest) string { return value.ID }), nil
}

func (r *MemoryRepository) ListBatches(context.Context, Principal, Scope, string, int) (Page[ArchiveBatchEvidence], error) {
	return Page[ArchiveBatchEvidence]{}, nil
}

func (r *MemoryRepository) ListTombstones(context.Context, Principal, Scope, string, int) (Page[TombstoneEvidence], error) {
	return Page[TombstoneEvidence]{}, nil
}

func (r *MemoryRepository) RequestPolicy(_ context.Context, principal Principal, input RequestPolicyInput, idem Idempotency) (PolicyChange, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id, err := ids.New()
	if err != nil {
		return PolicyChange{}, err
	}
	id, err = r.reserve(principal, input.TenantID, "retention.policy.request", idem, id)
	if err != nil {
		return PolicyChange{}, err
	}
	if prior, exists := r.changes[id]; exists {
		return prior, nil
	}
	head, exists := r.policies[policyKey(input.TenantID, input.DataClass)]
	if exists && (head.Version != input.ExpectedEffectiveVersion || head.HeadFence != input.ExpectedHeadFence) || !exists && (input.ExpectedEffectiveVersion != 0 || input.ExpectedHeadFence != 0) {
		return PolicyChange{}, ErrConflict
	}
	now := r.now()
	out := PolicyChange{ID: id, TenantID: input.TenantID, DataClass: input.DataClass, ExpectedEffectiveVersion: input.ExpectedEffectiveVersion, ExpectedHeadFence: input.ExpectedHeadFence, Proposal: input.Proposal, Status: PolicyPending, Reason: input.Reason, RequestedBy: principal.ActorID, ScheduledFor: input.ScheduledFor, ExpiresAt: now.Add(30 * time.Minute), CreatedAt: now, UpdatedAt: now, RowVersion: 1}
	r.changes[id] = out
	return out, nil
}

func (r *MemoryRepository) DecidePolicy(_ context.Context, principal Principal, scope Scope, id string, approve bool, input DecisionInput, idem Idempotency) (PolicyChange, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	replay, err := r.reserve(principal, scope.TenantID, map[bool]string{true: "retention.policy.approve", false: "retention.policy.reject"}[approve], idem, id)
	if err != nil {
		return PolicyChange{}, err
	}
	change, exists := r.changes[replay]
	if !exists || change.TenantID != scope.TenantID {
		return PolicyChange{}, ErrNotFound
	}
	now := r.now()
	if change.Status == PolicyPending && !change.ExpiresAt.After(now) {
		change.Status, change.DecisionReason, change.DecidedAt, change.UpdatedAt, change.RowVersion = PolicyExpired, "approval_window_expired", &now, now, change.RowVersion+1
		r.changes[id] = change
		return change, nil
	}
	if change.Status != PolicyPending || change.RowVersion != input.ExpectedRowVersion || change.RequestedBy == principal.ActorID {
		return PolicyChange{}, ErrConflict
	}
	change.RowVersion++
	change.UpdatedAt = now
	change.DecidedAt = &now
	change.DecisionReason = input.Reason
	if approve {
		change.Status, change.ApprovedBy, change.ApprovedAt = PolicyScheduled, principal.ActorID, &now
	} else {
		change.Status, change.RejectedBy = PolicyRejected, principal.ActorID
	}
	r.changes[id] = change
	return change, nil
}

func (r *MemoryRepository) CreateHold(_ context.Context, principal Principal, input CreateHoldInput, idem Idempotency) (LegalHold, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id, err := ids.New()
	if err != nil {
		return LegalHold{}, err
	}
	id, err = r.reserve(principal, input.TenantID, "retention.hold.create", idem, id)
	if err != nil {
		return LegalHold{}, err
	}
	if prior, exists := r.holds[id]; exists {
		return prior, nil
	}
	out := LegalHold{ID: id, TenantID: input.TenantID, DataClass: input.DataClass, ScopeType: input.ScopeType, MerchantID: input.MerchantID, SourceTable: input.SourceTable, SourceRecordID: input.SourceRecordID, CaseReference: input.CaseReference, Reason: input.Reason, CreatedBy: principal.ActorID, CreatedAt: r.now(), ExpiresAt: input.ExpiresAt, Version: 1}
	r.holds[id] = out
	return out, nil
}

func (r *MemoryRepository) RequestHoldRelease(_ context.Context, principal Principal, input RequestReleaseInput, idem Idempotency) (HoldReleaseRequest, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id, err := ids.New()
	if err != nil {
		return HoldReleaseRequest{}, err
	}
	id, err = r.reserve(principal, input.TenantID, "retention.hold_release.request", idem, id)
	if err != nil {
		return HoldReleaseRequest{}, err
	}
	if prior, exists := r.releases[id]; exists {
		return prior, nil
	}
	hold, exists := r.holds[input.HoldID]
	if !exists || hold.TenantID != input.TenantID {
		return HoldReleaseRequest{}, ErrNotFound
	}
	if hold.State() != "active" || hold.Version != input.ExpectedHoldVersion {
		return HoldReleaseRequest{}, ErrConflict
	}
	for _, prior := range r.releases {
		if prior.HoldID == input.HoldID && prior.Status == ReleasePending {
			return HoldReleaseRequest{}, ErrConflict
		}
	}
	now := r.now()
	out := HoldReleaseRequest{ID: id, TenantID: input.TenantID, HoldID: input.HoldID, ExpectedVersion: input.ExpectedHoldVersion, Status: ReleasePending, Reason: input.Reason, RequestedBy: principal.ActorID, CreatedAt: now, ExpiresAt: now.Add(30 * time.Minute), RowVersion: 1}
	r.releases[id] = out
	return out, nil
}

func (r *MemoryRepository) DecideHoldRelease(_ context.Context, principal Principal, scope Scope, id string, approve bool, input DecisionInput, idem Idempotency) (HoldReleaseRequest, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	replay, err := r.reserve(principal, scope.TenantID, map[bool]string{true: "retention.hold_release.approve", false: "retention.hold_release.reject"}[approve], idem, id)
	if err != nil {
		return HoldReleaseRequest{}, err
	}
	release, exists := r.releases[replay]
	if !exists || release.TenantID != scope.TenantID {
		return HoldReleaseRequest{}, ErrNotFound
	}
	hold := r.holds[release.HoldID]
	now := r.now()
	if release.Status == ReleasePending && !release.ExpiresAt.After(now) {
		release.Status, release.DecisionReason, release.DecidedAt, release.RowVersion = ReleaseExpired, "approval_window_expired", &now, release.RowVersion+1
		r.releases[id] = release
		return release, nil
	}
	if release.Status != ReleasePending || release.RowVersion != input.ExpectedRowVersion || release.RequestedBy == principal.ActorID || hold.CreatedBy == principal.ActorID {
		return HoldReleaseRequest{}, ErrConflict
	}
	release.RowVersion++
	release.DecidedAt = &now
	release.DecisionReason = input.Reason
	if !approve {
		release.Status, release.RejectedBy = ReleaseRejected, principal.ActorID
	} else if hold.State() != "active" || hold.Version != release.ExpectedVersion {
		release.Status, release.ApprovedBy = ReleaseConflict, principal.ActorID
	} else {
		release.Status, release.ApprovedBy = ReleaseCompleted, principal.ActorID
		hold.ReleasedAt, hold.ReleasedBy, hold.Version = &now, principal.ActorID, hold.Version+1
		r.holds[hold.ID] = hold
	}
	r.releases[id] = release
	return release, nil
}

func (r *MemoryRepository) AdvanceDue(_ context.Context, _ string, limit int) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now, processed := r.now(), 0
	for id, change := range r.changes {
		if processed >= limit {
			break
		}
		if change.Status == PolicyPending && !change.ExpiresAt.After(now) {
			change.Status, change.DecisionReason, change.DecidedAt, change.UpdatedAt, change.RowVersion = PolicyExpired, "approval_window_expired", &now, now, change.RowVersion+1
			r.changes[id] = change
			processed++
		}
	}
	for id, release := range r.releases {
		if processed >= limit {
			break
		}
		if release.Status == ReleasePending && !release.ExpiresAt.After(now) {
			release.Status, release.DecisionReason, release.DecidedAt, release.RowVersion = ReleaseExpired, "approval_window_expired", &now, release.RowVersion+1
			r.releases[id] = release
			processed++
		}
	}
	for id, change := range r.changes {
		if processed >= limit {
			break
		}
		if change.Status != PolicyScheduled || change.ApprovedAt == nil || change.ApprovedAt.After(now) || change.ScheduledFor.After(now) {
			continue
		}
		head, exists := r.policies[policyKey(change.TenantID, change.DataClass)]
		if exists && (head.Version != change.ExpectedEffectiveVersion || head.HeadFence != change.ExpectedHeadFence) || !exists && (change.ExpectedEffectiveVersion != 0 || change.ExpectedHeadFence != 0) {
			change.Status, change.DecisionReason = PolicyConflict, "stale_policy_head"
		} else {
			version, fence := int64(1), int64(1)
			if exists {
				version, fence = head.Version+1, head.HeadFence+1
			}
			policyID, _ := ids.New()
			r.policies[policyKey(change.TenantID, change.DataClass)] = EffectivePolicy{ID: policyID, TenantID: change.TenantID, DataClass: change.DataClass, Version: version, ArchiveAfterDays: change.Proposal.ArchiveAfterDays, PruneGraceDays: change.Proposal.PruneGraceDays, ObjectLockDays: change.Proposal.ObjectLockDays, PruneEnabled: change.Proposal.PruneEnabled, EffectiveAt: now, HeadFence: fence, LastActivatedAt: &now}
			change.Status, change.ActivatedAt = PolicyActive, &now
		}
		change.RowVersion++
		change.UpdatedAt = now
		r.changes[id] = change
		processed++
	}
	for id, hold := range r.holds {
		if processed >= limit {
			break
		}
		if hold.State() == "active" && hold.ExpiresAt != nil && !hold.ExpiresAt.After(now) {
			hold.ExpiredAt, hold.Version = &now, hold.Version+1
			r.holds[id] = hold
			processed++
		}
	}
	return processed, nil
}

var _ Repository = (*MemoryRepository)(nil)
