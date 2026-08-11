package matchingadmin

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/application"
	"github.com/calmshv-star/ocrypt/backend/internal/ids"
	"github.com/calmshv-star/ocrypt/backend/internal/management"
)

type Service struct {
	repository Repository
	now        func() time.Time
}

func NewService(repository Repository) (*Service, error) {
	if repository == nil {
		return nil, ErrInvalid
	}
	return &Service{repository: repository, now: func() time.Time { return time.Now().UTC() }}, nil
}

func (s *Service) requireInteractiveStepUp(p management.Principal, scope string) error {
	if !ids.Valid(p.TenantID) || !ids.Valid(p.MerchantID) || !ids.Valid(p.ActorID) || p.AuthMethod != "admin_assertion" {
		return ErrUnauthenticated
	}
	if !p.Has(scope) {
		return ErrForbidden
	}
	now := s.now()
	if p.StepUpAt.IsZero() || p.StepUpAt.After(now.Add(15*time.Second)) || now.Sub(p.StepUpAt) > 10*time.Minute {
		return ErrForbidden
	}
	return nil
}

func (s *Service) Create(ctx context.Context, p management.Principal, input PolicyInput, idem Idempotency) (PolicyChange, bool, error) {
	if err := s.requireInteractiveStepUp(p, ScopeWrite); err != nil {
		return PolicyChange{}, false, err
	}
	if !validPolicy(input) {
		return PolicyChange{}, false, ErrInvalid
	}
	return s.repository.Create(ctx, p, input, idem)
}

func (s *Service) Get(ctx context.Context, p management.Principal, id string) (PolicyChange, error) {
	if !ids.Valid(p.TenantID) || !ids.Valid(p.MerchantID) || !ids.Valid(p.ActorID) || p.AuthMethod != "admin_assertion" {
		return PolicyChange{}, ErrUnauthenticated
	}
	if !p.Has(ScopeRead) {
		return PolicyChange{}, ErrForbidden
	}
	if !ids.Valid(id) {
		return PolicyChange{}, ErrInvalid
	}
	return s.repository.Get(ctx, p, id)
}

func (s *Service) List(ctx context.Context, p management.Principal, cursor string, limit int) (Page, error) {
	if !ids.Valid(p.TenantID) || !ids.Valid(p.MerchantID) || !ids.Valid(p.ActorID) || p.AuthMethod != "admin_assertion" {
		return Page{}, ErrUnauthenticated
	}
	if !p.Has(ScopeRead) {
		return Page{}, ErrForbidden
	}
	if cursor != "" && !ids.Valid(cursor) || limit < 1 || limit > 100 {
		return Page{}, ErrInvalid
	}
	return s.repository.List(ctx, p, cursor, limit)
}

func (s *Service) RequestApproval(ctx context.Context, p management.Principal, id string, input Mutation, idem Idempotency) (PolicyChange, bool, error) {
	if err := s.requireInteractiveStepUp(p, ScopeWrite); err != nil {
		return PolicyChange{}, false, err
	}
	if !validMutation(id, input.Version, input.Reason) {
		return PolicyChange{}, false, ErrInvalid
	}
	input.Reason = strings.TrimSpace(input.Reason)
	return s.repository.RequestApproval(ctx, p, id, input, idem)
}

func (s *Service) Approve(ctx context.Context, p management.Principal, id string, input Mutation, idem Idempotency) (PolicyChange, bool, error) {
	if err := s.requireInteractiveStepUp(p, ScopeApprove); err != nil {
		return PolicyChange{}, false, err
	}
	if !validMutation(id, input.Version, input.Reason) {
		return PolicyChange{}, false, ErrInvalid
	}
	input.Reason = strings.TrimSpace(input.Reason)
	return s.repository.Approve(ctx, p, id, input, idem)
}

func (s *Service) Activate(ctx context.Context, p management.Principal, id string, input Activation, idem Idempotency) (PolicyChange, bool, error) {
	if err := s.requireInteractiveStepUp(p, ScopeActivate); err != nil {
		return PolicyChange{}, false, err
	}
	if !validMutation(id, input.Version, input.Reason) || input.EffectiveAt.Before(s.now().Add(-15*time.Second)) || input.EffectiveAt.After(s.now().Add(30*24*time.Hour)) {
		return PolicyChange{}, false, ErrInvalid
	}
	input.Reason = strings.TrimSpace(input.Reason)
	return s.repository.Activate(ctx, p, id, input, idem)
}

func validMutation(id string, version int64, reason string) bool {
	reason = strings.TrimSpace(reason)
	return ids.Valid(id) && version > 0 && len(reason) >= 8 && len(reason) <= 1000
}

func validPolicy(input PolicyInput) bool {
	collectors := append([]string(nil), input.GasFreeFeeCollectors...)
	sort.Strings(collectors)
	for i, value := range collectors {
		if value == "" || strings.TrimSpace(value) != value || i > 0 && value == collectors[i-1] || len(value) > 256 {
			return false
		}
	}
	policy := application.AutomatedMatchingPolicy{ID: "validation", Version: 1, AccumulatePartials: input.AccumulatePartials, UnderpaymentToleranceBPS: input.UnderpaymentToleranceBPS, OverpaymentMode: input.OverpaymentMode, AcceptLateWithinGrace: input.AcceptLateWithinGrace, RequireSameSender: input.RequireSameSender, GasFreeEnabled: input.GasFreeEnabled, GasFreeFeeCollectors: collectors}
	return policy.Validate() == nil
}
