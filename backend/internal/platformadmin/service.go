package platformadmin

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/ids"
)

type Service struct {
	repository Repository
	now        func() time.Time
}

func NewService(repository Repository, now func() time.Time) (*Service, error) {
	if repository == nil {
		return nil, ErrDependency
	}
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Service{repository: repository, now: now}, nil
}

func (s *Service) require(p Principal, permission string, scope Scope, stepUp bool) error {
	if !ids.Valid(p.ActorID) || p.SessionID == "" {
		return ErrUnauthenticated
	}
	if err := p.authorize(permission, scope); err != nil {
		return err
	}
	if stepUp {
		now := s.now()
		if p.StepUpAt.IsZero() || p.StepUpAt.After(now.Add(10*time.Second)) || p.StepUpAt.Before(now.Add(-10*time.Minute)) {
			return ErrStepUpRequired
		}
	}
	return nil
}

func validateIdempotency(idem Idempotency) error {
	if len(idem.Key) < 8 || len(idem.Key) > 255 {
		return ErrInvalid
	}
	return nil
}

func (s *Service) CreateDraft(ctx context.Context, p Principal, input CreateInput, idem Idempotency) (ChangeRequest, error) {
	scope := Scope{TenantID: input.TenantID}
	if err := s.require(p, "platform_config:write", scope, false); err != nil {
		return ChangeRequest{}, err
	}
	if err := ValidateCreate(input); err != nil {
		return ChangeRequest{}, err
	}
	if err := validateIdempotency(idem); err != nil {
		return ChangeRequest{}, err
	}
	return s.repository.CreateDraft(ctx, p, input, idem)
}
func (s *Service) GetChange(ctx context.Context, p Principal, scope Scope, id string) (ChangeRequest, error) {
	if err := s.require(p, "platform_config:read", scope, false); err != nil {
		return ChangeRequest{}, err
	}
	if !ids.Valid(id) {
		return ChangeRequest{}, ErrInvalid
	}
	return s.repository.GetChange(ctx, p, scope, id)
}
func (s *Service) ListChanges(ctx context.Context, p Principal, scope Scope, kind Kind, status Status, cursor string, limit int) (Page[ChangeRequest], error) {
	if err := s.require(p, "platform_config:read", scope, false); err != nil {
		return Page[ChangeRequest]{}, err
	}
	validStatus := status == "" || status == StatusDraft || status == StatusApprovalRequested || status == StatusApproved || status == StatusRejected || status == StatusScheduled || status == StatusActive || status == StatusSuperseded
	if kind != "" && !allKinds[kind] || !validStatus || cursor != "" && !ids.Valid(cursor) || limit < 1 || limit > 200 {
		return Page[ChangeRequest]{}, ErrInvalid
	}
	return s.repository.ListChanges(ctx, p, scope, kind, status, cursor, limit)
}
func (s *Service) RequestApproval(ctx context.Context, p Principal, scope Scope, id string, input DecisionInput, idem Idempotency) (ChangeRequest, error) {
	if err := s.require(p, "platform_config:request", scope, true); err != nil {
		return ChangeRequest{}, err
	}
	if !ids.Valid(id) || input.ExpectedRowVersion < 1 || len(strings.TrimSpace(input.Reason)) < 3 || validateIdempotency(idem) != nil {
		return ChangeRequest{}, ErrInvalid
	}
	return s.repository.RequestApproval(ctx, p, scope, id, input, idem)
}
func (s *Service) Decide(ctx context.Context, p Principal, scope Scope, id string, approve bool, input DecisionInput, idem Idempotency) (ChangeRequest, error) {
	if err := s.require(p, "platform_config:approve", scope, true); err != nil {
		return ChangeRequest{}, err
	}
	if !ids.Valid(id) || input.ExpectedRowVersion < 1 || len(strings.TrimSpace(input.Reason)) < 3 || validateIdempotency(idem) != nil {
		return ChangeRequest{}, ErrInvalid
	}
	return s.repository.Decide(ctx, p, scope, id, approve, input, idem)
}
func (s *Service) Schedule(ctx context.Context, p Principal, scope Scope, id string, input ScheduleInput, idem Idempotency) (ChangeRequest, error) {
	if err := s.require(p, "platform_config:schedule", scope, true); err != nil {
		return ChangeRequest{}, err
	}
	now := s.now()
	if !ids.Valid(id) || input.ExpectedRowVersion < 1 || input.ActivateAt.Before(now.Add(30*time.Second)) || input.ActivateAt.After(now.Add(366*24*time.Hour)) || len(strings.TrimSpace(input.Reason)) < 3 || validateIdempotency(idem) != nil {
		return ChangeRequest{}, ErrInvalid
	}
	return s.repository.Schedule(ctx, p, scope, id, input, idem)
}
func (s *Service) Activate(ctx context.Context, p Principal, scope Scope, id string, input ActivateInput, idem Idempotency) (Snapshot, error) {
	if err := s.require(p, "platform_config:activate", scope, true); err != nil {
		return Snapshot{}, err
	}
	if !ids.Valid(id) || input.ExpectedRowVersion < 1 || input.ExpectedFenceToken < 0 || len(strings.TrimSpace(input.Reason)) < 3 || validateIdempotency(idem) != nil {
		return Snapshot{}, ErrInvalid
	}
	return s.repository.Activate(ctx, p, scope, id, input, idem)
}
func (s *Service) Rollback(ctx context.Context, p Principal, input RollbackInput, idem Idempotency) (ChangeRequest, error) {
	scope := Scope{TenantID: input.TenantID}
	if err := s.require(p, "platform_config:rollback", scope, true); err != nil {
		return ChangeRequest{}, err
	}
	if !ids.Valid(input.SnapshotID) || len(strings.TrimSpace(input.Reason)) < 3 || validateIdempotency(idem) != nil {
		return ChangeRequest{}, ErrInvalid
	}
	return s.repository.CreateRollback(ctx, p, input, idem)
}
func (s *Service) ListSnapshots(ctx context.Context, p Principal, scope Scope, kind Kind, key, cursor string, limit int) (Page[Snapshot], error) {
	if err := s.require(p, "platform_config:read", scope, false); err != nil {
		return Page[Snapshot]{}, err
	}
	if kind != "" && !allKinds[kind] || key != "" && !logicalKeyPattern.MatchString(key) || cursor != "" && !ids.Valid(cursor) || limit < 1 || limit > 200 {
		return Page[Snapshot]{}, ErrInvalid
	}
	return s.repository.ListSnapshots(ctx, p, scope, kind, key, cursor, limit)
}
func (s *Service) EmergencyPause(ctx context.Context, p Principal, input PauseInput, idem Idempotency) error {
	scope := Scope{TenantID: input.TenantID}
	if err := s.require(p, "platform_config:emergency", scope, true); err != nil {
		return err
	}
	if !allKinds[input.Kind] || !logicalKeyPattern.MatchString(input.LogicalKey) || (input.Action != "pause" && input.Action != "resume") || len(strings.TrimSpace(input.Reason)) < 8 || validateIdempotency(idem) != nil {
		return ErrInvalid
	}
	return s.repository.EmergencyPause(ctx, p, input, idem)
}

func NewIdempotency(key string, input any) (Idempotency, error) {
	encoded, err := json.Marshal(input)
	if err != nil {
		return Idempotency{}, err
	}
	value, err := Fingerprint(json.RawMessage(encoded))
	if err != nil {
		return Idempotency{}, err
	}
	value.Key = key
	return value, nil
}
