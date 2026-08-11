package retentionadmin

import (
	"context"
	"regexp"
	"strings"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/ids"
)

var caseReferencePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`)

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

func (s *Service) require(principal Principal, permission string, scope Scope, stepUp bool) error {
	if !ids.Valid(principal.ActorID) || strings.TrimSpace(principal.SessionID) == "" {
		return ErrUnauthenticated
	}
	if err := principal.authorize(permission, scope); err != nil {
		return err
	}
	if stepUp {
		now := s.now()
		if principal.StepUpAt.IsZero() || principal.StepUpAt.After(now.Add(10*time.Second)) || principal.StepUpAt.Before(now.Add(-10*time.Minute)) {
			return ErrStepUpRequired
		}
	}
	return nil
}

func validIdempotency(value Idempotency) bool {
	return len(value.Key) >= 8 && len(value.Key) <= 255 && strings.TrimSpace(value.Key) == value.Key
}

func validReason(value string) bool {
	trimmed := strings.TrimSpace(value)
	return len(trimmed) >= 8 && len(trimmed) <= 2048 && !strings.ContainsRune(trimmed, 0)
}

func validProposal(class DataClass, value PolicyProposal) bool {
	return class.Valid() && value.ArchiveAfterDays >= 1 && value.ArchiveAfterDays <= 3650 &&
		value.PruneGraceDays >= 1 && value.PruneGraceDays <= 90 && value.ObjectLockDays >= 30 &&
		value.ObjectLockDays <= 3650 && value.ObjectLockDays > value.PruneGraceDays &&
		(class.Prunable() || !value.PruneEnabled)
}

func validPage(scope Scope, cursor string, limit int) bool {
	return ids.Valid(scope.TenantID) && (cursor == "" || ids.Valid(cursor)) && limit >= 1 && limit <= 200
}

func (s *Service) ListPolicies(ctx context.Context, principal Principal, scope Scope) ([]EffectivePolicy, error) {
	if err := s.require(principal, "retention:read", scope, false); err != nil {
		return nil, err
	}
	if !ids.Valid(scope.TenantID) {
		return nil, ErrInvalid
	}
	return s.repository.ListPolicies(ctx, principal, scope)
}

func (s *Service) ListPolicyChanges(ctx context.Context, principal Principal, scope Scope, cursor string, limit int) (Page[PolicyChange], error) {
	if err := s.require(principal, "retention:read", scope, false); err != nil {
		return Page[PolicyChange]{}, err
	}
	if !validPage(scope, cursor, limit) {
		return Page[PolicyChange]{}, ErrInvalid
	}
	return s.repository.ListPolicyChanges(ctx, principal, scope, cursor, limit)
}

func (s *Service) ListHolds(ctx context.Context, principal Principal, scope Scope, cursor string, limit int) (Page[LegalHold], error) {
	if err := s.require(principal, "retention:read", scope, false); err != nil {
		return Page[LegalHold]{}, err
	}
	if !validPage(scope, cursor, limit) {
		return Page[LegalHold]{}, ErrInvalid
	}
	return s.repository.ListHolds(ctx, principal, scope, cursor, limit)
}

func (s *Service) ListReleaseRequests(ctx context.Context, principal Principal, scope Scope, cursor string, limit int) (Page[HoldReleaseRequest], error) {
	if err := s.require(principal, "retention:read", scope, false); err != nil {
		return Page[HoldReleaseRequest]{}, err
	}
	if !validPage(scope, cursor, limit) {
		return Page[HoldReleaseRequest]{}, ErrInvalid
	}
	return s.repository.ListReleaseRequests(ctx, principal, scope, cursor, limit)
}

func (s *Service) ListBatches(ctx context.Context, principal Principal, scope Scope, cursor string, limit int) (Page[ArchiveBatchEvidence], error) {
	if err := s.require(principal, "retention:read", scope, false); err != nil {
		return Page[ArchiveBatchEvidence]{}, err
	}
	if !validPage(scope, cursor, limit) {
		return Page[ArchiveBatchEvidence]{}, ErrInvalid
	}
	return s.repository.ListBatches(ctx, principal, scope, cursor, limit)
}

func (s *Service) ListTombstones(ctx context.Context, principal Principal, scope Scope, cursor string, limit int) (Page[TombstoneEvidence], error) {
	if err := s.require(principal, "retention:read", scope, false); err != nil {
		return Page[TombstoneEvidence]{}, err
	}
	if !validPage(scope, cursor, limit) {
		return Page[TombstoneEvidence]{}, ErrInvalid
	}
	return s.repository.ListTombstones(ctx, principal, scope, cursor, limit)
}

func (s *Service) RequestPolicy(ctx context.Context, principal Principal, input RequestPolicyInput, idem Idempotency) (PolicyChange, error) {
	scope := Scope{TenantID: input.TenantID}
	if err := s.require(principal, "retention:policy_request", scope, true); err != nil {
		return PolicyChange{}, err
	}
	now := s.now()
	if !ids.Valid(input.TenantID) || input.ExpectedEffectiveVersion < 0 || input.ExpectedHeadFence < 0 ||
		!validProposal(input.DataClass, input.Proposal) || input.ScheduledFor.Before(now) ||
		input.ScheduledFor.After(now.Add(365*24*time.Hour)) || !validReason(input.Reason) || !validIdempotency(idem) {
		return PolicyChange{}, ErrInvalid
	}
	return s.repository.RequestPolicy(ctx, principal, input, idem)
}

func (s *Service) DecidePolicy(ctx context.Context, principal Principal, scope Scope, id string, approve bool, input DecisionInput, idem Idempotency) (PolicyChange, error) {
	if err := s.require(principal, "retention:policy_approve", scope, true); err != nil {
		return PolicyChange{}, err
	}
	if !ids.Valid(scope.TenantID) || !ids.Valid(id) || input.ExpectedRowVersion < 1 || !validReason(input.Reason) || !validIdempotency(idem) {
		return PolicyChange{}, ErrInvalid
	}
	return s.repository.DecidePolicy(ctx, principal, scope, id, approve, input, idem)
}

func (s *Service) CreateHold(ctx context.Context, principal Principal, input CreateHoldInput, idem Idempotency) (LegalHold, error) {
	scope := Scope{TenantID: input.TenantID}
	if err := s.require(principal, "retention:hold_create", scope, true); err != nil {
		return LegalHold{}, err
	}
	shapeValid := input.ScopeType == HoldTenant && input.MerchantID == "" && input.SourceTable == "" && input.SourceRecordID == "" ||
		input.ScopeType == HoldMerchant && ids.Valid(input.MerchantID) && input.SourceTable == "" && input.SourceRecordID == "" ||
		input.ScopeType == HoldRecord && ids.Valid(input.MerchantID) && ids.Valid(input.SourceRecordID) && input.SourceTable == input.DataClass.sourceTable()
	if !ids.Valid(input.TenantID) || !input.DataClass.Valid() || !shapeValid || !caseReferencePattern.MatchString(input.CaseReference) || !validReason(input.Reason) || !validIdempotency(idem) {
		return LegalHold{}, ErrInvalid
	}
	if input.ExpiresAt != nil {
		now := s.now()
		if input.ExpiresAt.Before(now.Add(time.Hour)) || input.ExpiresAt.After(now.Add(10*365*24*time.Hour)) {
			return LegalHold{}, ErrInvalid
		}
	}
	return s.repository.CreateHold(ctx, principal, input, idem)
}

func (c DataClass) sourceTable() string {
	switch c {
	case CallbackEventBody:
		return "callback_events"
	case EventHistoryPayload:
		return "event_history"
	case PublishedOutbox:
		return "outbox_events"
	default:
		return ""
	}
}

func (s *Service) RequestHoldRelease(ctx context.Context, principal Principal, input RequestReleaseInput, idem Idempotency) (HoldReleaseRequest, error) {
	scope := Scope{TenantID: input.TenantID}
	if err := s.require(principal, "retention:hold_release", scope, true); err != nil {
		return HoldReleaseRequest{}, err
	}
	if !ids.Valid(input.TenantID) || !ids.Valid(input.HoldID) || input.ExpectedHoldVersion < 1 || !validReason(input.Reason) || !validIdempotency(idem) {
		return HoldReleaseRequest{}, ErrInvalid
	}
	return s.repository.RequestHoldRelease(ctx, principal, input, idem)
}

func (s *Service) DecideHoldRelease(ctx context.Context, principal Principal, scope Scope, id string, approve bool, input DecisionInput, idem Idempotency) (HoldReleaseRequest, error) {
	if err := s.require(principal, "retention:hold_release", scope, true); err != nil {
		return HoldReleaseRequest{}, err
	}
	if !ids.Valid(scope.TenantID) || !ids.Valid(id) || input.ExpectedRowVersion < 1 || !validReason(input.Reason) || !validIdempotency(idem) {
		return HoldReleaseRequest{}, ErrInvalid
	}
	return s.repository.DecideHoldRelease(ctx, principal, scope, id, approve, input, idem)
}

func (s *Service) AdvanceDue(ctx context.Context, worker string, limit int) (int, error) {
	if !caseReferencePattern.MatchString(worker) || limit < 1 || limit > 100 {
		return 0, ErrInvalid
	}
	return s.repository.AdvanceDue(ctx, worker, limit)
}
