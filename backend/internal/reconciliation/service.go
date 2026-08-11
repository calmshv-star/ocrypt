package reconciliation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

type Service struct {
	repository Repository
	snapshots  SnapshotProvider
	clock      Clock
	ids        IDGenerator
}

func NewService(r Repository, p SnapshotProvider, c Clock, i IDGenerator) (*Service, error) {
	if r == nil || p == nil || c == nil || i == nil {
		return nil, ErrValidation
	}
	return &Service{r, p, c, i}, nil
}

type RequestCommand struct {
	TenantID       TenantID    `json:"tenant_id"`
	AssetIDs       []AssetID   `json:"asset_ids"`
	IdempotencyKey string      `json:"idempotency_key"`
	Auth           AuthContext `json:"-"`
}

type RequestResult struct {
	Run     Run  `json:"run"`
	Created bool `json:"created"`
}

func (s *Service) Request(ctx context.Context, c RequestCommand) (Run, bool, error) {
	now := s.clock.Now().UTC()
	if err := c.Auth.Authorize("reconciliation:run", now); err != nil {
		return Run{}, false, err
	}
	assets, err := canonicalAssetIDs(c.AssetIDs)
	if err != nil {
		return Run{}, false, err
	}
	if c.TenantID == "" || len(c.IdempotencyKey) < 8 || len(c.IdempotencyKey) > 255 {
		return Run{}, false, ErrValidation
	}
	h := hash(struct {
		TenantID TenantID
		AssetIDs []AssetID
	}{c.TenantID, assets})
	_, requestFingerprint, _, _ := reconciliationMutationIdentity(ctx)
	if requestFingerprint != ([32]byte{}) {
		h = hex.EncodeToString(requestFingerprint[:])
	}
	id := RunID(s.ids.NewID())
	run := Run{ID: id, TenantID: c.TenantID, AssetIDs: assets, IdempotencyKey: c.IdempotencyKey, RequestHash: h, Status: StatusRequested, Version: 1, CreatedAt: now, UpdatedAt: now}
	m := CreateMutation{run, s.audit(c.TenantID, string(id), c.Auth.ActorID, "reconciliation.requested", "snapshot run requested", now), []OutboxCommand{s.event(c.TenantID, string(id), "reconciliation.requested", run, now)}}
	return s.repository.Create(ctx, m)
}

type ExecuteCommand struct {
	TenantID        TenantID    `json:"tenant_id"`
	RunID           RunID       `json:"run_id"`
	ExpectedVersion int64       `json:"expected_version"`
	CutoffAt        time.Time   `json:"cutoff_at"`
	Auth            AuthContext `json:"-"`
}

func (s *Service) Execute(ctx context.Context, c ExecuteCommand) (Run, error) {
	now := s.clock.Now().UTC()
	if err := c.Auth.Authorize("reconciliation:execute", now); err != nil {
		return Run{}, err
	}
	decisionKey, decisionFingerprint, decisionReason, hasMutationIdentity := reconciliationMutationIdentity(ctx)
	if c.TenantID == "" || c.RunID == "" || c.ExpectedVersion < 1 || c.CutoffAt.IsZero() || c.CutoffAt.After(now) || hasMutationIdentity && (strings.TrimSpace(decisionReason) == "" || len(decisionReason) > 2048) {
		return Run{}, ErrValidation
	}
	if replay, ok, err := s.replayDecision(ctx, c.TenantID, c.Auth.ActorID, "reconciliation.execute", decisionKey, decisionFingerprint); err != nil || ok {
		return replay, err
	}
	current, err := s.repository.Get(ctx, c.TenantID, c.RunID)
	if err != nil {
		return Run{}, err
	}
	if current.Version != c.ExpectedVersion {
		return Run{}, ErrVersionConflict
	}
	if current.Status != StatusRequested && current.Status != StatusFailed {
		return Run{}, ErrValidation
	}
	items := make([]Item, 0, len(current.AssetIDs))
	integrityItems := make([]IntegrityItem, 0)
	for _, asset := range current.AssetIDs {
		snapshot, err := s.snapshots.Snapshot(ctx, c.TenantID, asset, c.CutoffAt.UTC())
		if err != nil {
			return Run{}, fmt.Errorf("snapshot %s: %w", asset, err)
		}
		if err := snapshot.Validate(c.TenantID); err != nil {
			return Run{}, err
		}
		if snapshot.AssetID != asset || !snapshot.CutoffAt.Equal(c.CutoffAt.UTC()) {
			return Run{}, fmt.Errorf("%w: provider returned wrong asset or cutoff", ErrValidation)
		}
		item, err := Classify(snapshot)
		if err != nil {
			return Run{}, err
		}
		item.RunID = current.ID
		items = append(items, item)
		integrity, err := s.snapshots.IntegritySnapshots(ctx, c.TenantID, asset, c.CutoffAt.UTC())
		if err != nil {
			return Run{}, fmt.Errorf("integrity snapshot %s: %w", asset, err)
		}
		for _, evidence := range integrity {
			if evidence.TenantID != c.TenantID || evidence.AssetID != asset {
				return Run{}, fmt.Errorf("%w: integrity provider crossed tenant or asset", ErrValidation)
			}
			classified, err := ClassifyIntegrity(evidence, c.CutoffAt.UTC())
			if err != nil {
				return Run{}, err
			}
			classified.RunID = current.ID
			integrityItems = append(integrityItems, classified)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].AssetID < items[j].AssetID })
	sort.Slice(integrityItems, func(i, j int) bool {
		if integrityItems[i].AssetID == integrityItems[j].AssetID {
			return integrityItems[i].SubjectID < integrityItems[j].SubjectID
		}
		return integrityItems[i].AssetID < integrityItems[j].AssetID
	})
	next := current
	next.Status = StatusCompleted
	next.Items = items
	next.IntegrityItems = integrityItems
	next.ReportDigest = reportDigest(items, integrityItems)
	next.FailureCode = ""
	next.Version++
	next.UpdatedAt = now
	reason := "deterministic snapshot comparison completed"
	if hasMutationIdentity {
		reason = strings.TrimSpace(decisionReason)
	}
	m := UpdateMutation{TenantID: current.TenantID, RunID: current.ID, ExpectedVersion: current.Version, Next: next, Audit: s.audit(current.TenantID, string(current.ID), c.Auth.ActorID, "reconciliation.completed", reason, now), Outbox: []OutboxCommand{s.event(current.TenantID, string(current.ID), "reconciliation.report.ready", next, now)}, DecisionOperation: "reconciliation.execute", DecisionKey: decisionKey, DecisionFingerprint: decisionFingerprint, DecisionActor: c.Auth.ActorID}
	return s.repository.Update(ctx, m)
}

type reconciliationDecisionReplay interface {
	ReplayDecision(context.Context, TenantID, ActorID, string, string, [32]byte) (Run, bool, error)
}

type reconciliationMutationIdentityKey struct{}
type reconciliationMutationIdentityValue struct {
	Key         string
	Fingerprint [32]byte
	Reason      string
}

func WithMutationIdentity(ctx context.Context, key string, fingerprint [32]byte, reason string) context.Context {
	return context.WithValue(ctx, reconciliationMutationIdentityKey{}, reconciliationMutationIdentityValue{key, fingerprint, reason})
}

func reconciliationMutationIdentity(ctx context.Context) (string, [32]byte, string, bool) {
	value, ok := ctx.Value(reconciliationMutationIdentityKey{}).(reconciliationMutationIdentityValue)
	return value.Key, value.Fingerprint, value.Reason, ok
}

func (s *Service) replayDecision(ctx context.Context, tenant TenantID, actor ActorID, operation, key string, fingerprint [32]byte) (Run, bool, error) {
	if key == "" && fingerprint == ([32]byte{}) {
		return Run{}, false, nil
	}
	if len(key) < 8 || len(key) > 255 || key != strings.TrimSpace(key) || fingerprint == ([32]byte{}) {
		return Run{}, false, ErrValidation
	}
	repository, ok := s.repository.(reconciliationDecisionReplay)
	if !ok {
		return Run{}, false, ErrValidation
	}
	return repository.ReplayDecision(ctx, tenant, actor, operation, key, fingerprint)
}

func hash(v any) string {
	b, e := json.Marshal(v)
	if e != nil {
		panic(e)
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func reportDigest(items []Item, integrity []IntegrityItem) string {
	balances := append([]Item(nil), items...)
	checks := append([]IntegrityItem(nil), integrity...)
	for i := range balances {
		balances[i].RunID = ""
	}
	for i := range checks {
		checks[i].RunID = ""
	}
	return hash(struct {
		Balances  []Item          `json:"balances"`
		Integrity []IntegrityItem `json:"integrity"`
	}{balances, checks})
}
func (s *Service) audit(t TenantID, a string, actor ActorID, action, reason string, at time.Time) AuditCommand {
	return AuditCommand{s.ids.NewID(), t, a, actor, action, reason, at}
}
func (s *Service) event(t TenantID, a, typ string, p any, at time.Time) OutboxCommand {
	b, _ := json.Marshal(p)
	return OutboxCommand{s.ids.NewID(), t, a, typ, b, at}
}
