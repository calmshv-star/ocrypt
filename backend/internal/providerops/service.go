package providerops

import (
	"context"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/ids"
)

var providerIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,127}$`)
var hostedProviderIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
var failureDomainPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
var bootstrapReferencePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,254}$`)

var hostedPolicyOperations = [...]Operation{
	OperationHealth, OperationCreate, OperationStatus, OperationCancel, OperationRefund, OperationReconciliation,
}

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
	if !ids.Valid(principal.ActorID) || principal.SessionID == "" {
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
	return len(trimmed) >= 8 && len(trimmed) <= 1000 && !strings.ContainsAny(trimmed, "\x00")
}

func (s *Service) ListBindings(ctx context.Context, principal Principal, scope Scope, cursor string, limit int) (Page[Binding], error) {
	if err := s.require(principal, "provider_ops:read", scope, false); err != nil {
		return Page[Binding]{}, err
	}
	if cursor != "" && !ids.Valid(cursor) || limit < 1 || limit > 200 {
		return Page[Binding]{}, ErrInvalid
	}
	return s.repository.ListBindings(ctx, scope, cursor, limit)
}

func (s *Service) GetBinding(ctx context.Context, principal Principal, scope Scope, id string) (Binding, error) {
	if err := s.require(principal, "provider_ops:read", scope, false); err != nil {
		return Binding{}, err
	}
	if !ids.Valid(id) {
		return Binding{}, ErrInvalid
	}
	return s.repository.GetBinding(ctx, scope, id)
}

func (s *Service) ListChanges(ctx context.Context, principal Principal, scope Scope, cursor string, limit int) (Page[ChangeRequest], error) {
	if err := s.require(principal, "provider_ops:read", scope, false); err != nil {
		return Page[ChangeRequest]{}, err
	}
	if cursor != "" && !ids.Valid(cursor) || limit < 1 || limit > 200 {
		return Page[ChangeRequest]{}, ErrInvalid
	}
	return s.repository.ListChanges(ctx, scope, cursor, limit)
}

func (s *Service) ListHostedPolicies(ctx context.Context, principal Principal, scope Scope, cursor string, limit int) (Page[HostedPolicyVersion], error) {
	if err := s.require(principal, "provider_ops:read", scope, false); err != nil {
		return Page[HostedPolicyVersion]{}, err
	}
	if cursor != "" && !ids.Valid(cursor) || limit < 1 || limit > 200 {
		return Page[HostedPolicyVersion]{}, ErrInvalid
	}
	return s.repository.ListHostedPolicies(ctx, scope, cursor, limit)
}

func validPolicyParameters(value PolicyParameters) bool {
	return value.TimeoutMS >= 100 && value.TimeoutMS <= 30000 &&
		value.MaxAttempts >= 1 && value.MaxAttempts <= 5 &&
		value.BackoffMS >= 0 && value.BackoffMS <= 30000 &&
		value.RateLimit >= 1 && value.RateLimit <= 10000 &&
		value.RateWindowSeconds >= 1 && value.RateWindowSeconds <= 3600 &&
		value.MaxHealthAgeSeconds >= 5 && value.MaxHealthAgeSeconds <= 3600 &&
		value.FailureThreshold >= 1 && value.FailureThreshold <= 20 &&
		value.OpenSeconds >= 1 && value.OpenSeconds <= 3600 &&
		value.HalfOpenSuccesses >= 1 && value.HalfOpenSuccesses <= 20 &&
		value.Priority >= 0 && value.Priority <= 1000 && value.MaxLagBlocks <= 1_000_000_000 &&
		failureDomainPattern.MatchString(value.FailureDomain)
}

func validHostedPolicy(values map[Operation]PolicyParameters) bool {
	if len(values) != len(hostedPolicyOperations) {
		return false
	}
	for _, operation := range hostedPolicyOperations {
		value, exists := values[operation]
		if !exists || !validPolicyParameters(value) {
			return false
		}
	}
	return true
}

func (s *Service) RequestHostedPolicy(ctx context.Context, principal Principal, input RequestHostedPolicyInput, idem Idempotency) (HostedPolicyVersion, error) {
	scope := Scope{TenantID: input.TenantID}
	if err := s.require(principal, "provider_ops:request", scope, true); err != nil {
		return HostedPolicyVersion{}, err
	}
	if !ids.Valid(input.BindingID) || !ids.Valid(input.TenantID) || input.ExpectedBindingVersion < 1 || !validHostedPolicy(input.Policies) || !bootstrapReferencePattern.MatchString(input.BootstrapProbeReference) || !validReason(input.Reason) || !validIdempotency(idem) {
		return HostedPolicyVersion{}, ErrInvalid
	}
	return s.repository.RequestHostedPolicy(ctx, principal, input, idem)
}

func (s *Service) DecideHostedPolicy(ctx context.Context, principal Principal, scope Scope, id string, approve bool, input DecideInput, idem Idempotency) (HostedPolicyVersion, error) {
	if err := s.require(principal, "provider_ops:approve", scope, true); err != nil {
		return HostedPolicyVersion{}, err
	}
	if !ids.Valid(scope.TenantID) || !ids.Valid(id) || input.ExpectedRequestVersion < 1 || !validReason(input.Reason) || !validIdempotency(idem) {
		return HostedPolicyVersion{}, ErrInvalid
	}
	return s.repository.DecideHostedPolicy(ctx, principal, scope, id, approve, input, idem)
}

func (s *Service) RequestChange(ctx context.Context, principal Principal, input RequestChangeInput, idem Idempotency) (ChangeRequest, error) {
	scope := Scope{TenantID: input.TenantID}
	if err := s.require(principal, "provider_ops:request", scope, true); err != nil {
		return ChangeRequest{}, err
	}
	if !ids.Valid(input.BindingID) || input.ExpectedBindingVersion < 1 || !validReason(input.Reason) || !validIdempotency(idem) || input.RequestedStatus != BindingActive && input.RequestedStatus != BindingPaused {
		return ChangeRequest{}, ErrInvalid
	}
	return s.repository.RequestChange(ctx, principal, input, idem)
}

func (s *Service) DecideChange(ctx context.Context, principal Principal, scope Scope, id string, approve bool, input DecideInput, idem Idempotency) (ChangeRequest, error) {
	if err := s.require(principal, "provider_ops:approve", scope, true); err != nil {
		return ChangeRequest{}, err
	}
	if !ids.Valid(id) || input.ExpectedRequestVersion < 1 || !validReason(input.Reason) || !validIdempotency(idem) {
		return ChangeRequest{}, ErrInvalid
	}
	return s.repository.DecideChange(ctx, principal, scope, id, approve, input, idem)
}

// Admit returns a deterministic, closed set of candidates. No caller-provided
// grant or bypass flag is accepted. Half-open circuits are reserved for the
// private health worker and are not production traffic candidates.
func (s *Service) Admit(ctx context.Context, input AdmissionRequest) (Admission, error) {
	if !operations[input.Operation] || input.Kind != ProviderOnChain && input.Kind != ProviderHosted || input.Quorum < 1 || input.Quorum > 16 || len(input.ProviderIDs) < input.Quorum || len(input.ProviderIDs) > 16 {
		return Admission{}, ErrInvalid
	}
	if input.Now.IsZero() {
		input.Now = s.now()
	}
	seen := make(map[string]struct{}, len(input.ProviderIDs))
	for _, id := range input.ProviderIDs {
		if !providerIDPattern.MatchString(id) || input.Kind == ProviderHosted && !hostedProviderIDPattern.MatchString(id) {
			return Admission{}, ErrInvalid
		}
		if _, exists := seen[id]; exists {
			return Admission{}, ErrInvalid
		}
		seen[id] = struct{}{}
	}
	candidates, err := s.repository.AdmissionCandidates(ctx, input)
	if err != nil {
		return Admission{}, err
	}
	admitted := make([]Candidate, 0, len(candidates))
	for _, candidate := range candidates {
		if _, requested := seen[candidate.ProviderID]; !requested || candidate.ProviderKind != input.Kind || candidate.Status != BindingActive || candidate.Policy.Operation != input.Operation || candidate.Circuit.State != CircuitClosed || candidate.Circuit.LastSuccessAt == nil || input.Now.Sub(*candidate.Circuit.LastSuccessAt) > candidate.Policy.MaxHealthAge || input.Now.Before(*candidate.Circuit.LastSuccessAt) {
			continue
		}
		admitted = append(admitted, candidate)
	}
	// A stable priority plus failure-domain order makes failover deterministic
	// while preventing a lower priority from leapfrogging a healthy primary.
	sort.Slice(admitted, func(i, j int) bool {
		if admitted[i].Policy.Priority != admitted[j].Policy.Priority {
			return admitted[i].Policy.Priority < admitted[j].Policy.Priority
		}
		if admitted[i].Policy.FailureDomain != admitted[j].Policy.FailureDomain {
			return admitted[i].Policy.FailureDomain < admitted[j].Policy.FailureDomain
		}
		return admitted[i].ProviderID < admitted[j].ProviderID
	})
	if len(admitted) < input.Quorum {
		return Admission{}, ErrQuorumUnavailable
	}
	return Admission{Candidates: admitted, Quorum: input.Quorum}, nil
}

func (s *Service) ClaimProbes(ctx context.Context, owner string, limit int) ([]Probe, error) {
	if !providerIDPattern.MatchString(owner) || limit < 2 || limit > 128 {
		return nil, ErrInvalid
	}
	return s.repository.ClaimProbes(ctx, owner, limit)
}

func (s *Service) CompleteProbe(ctx context.Context, observation Observation) error {
	if !ids.Valid(observation.BindingID) || !operations[observation.Operation] || !providerIDPattern.MatchString(observation.LeaseOwner) || observation.LeaseToken < 1 || observation.FenceToken < 1 || observation.ObservedAt.IsZero() || observation.Latency < 0 || observation.Latency > 30*time.Second || !errorCategories[observation.Error] || observation.Success != (observation.Error == ErrorNone) {
		return ErrInvalid
	}
	return s.repository.CompleteProbe(ctx, observation)
}

// NextCircuit is the deterministic transition used by the repository after a
// fenced probe. The database transaction still compares lease and fence tokens.
func NextCircuit(current Circuit, policy Policy, success bool, observedAt time.Time) Circuit {
	next := current
	next.LastObservedAt = &observedAt
	next.Version++
	next.LeaseOwner = ""
	next.LeaseUntil = nil
	if success {
		next.LastSuccessAt = &observedAt
		next.ConsecutiveFailures = 0
		if current.State == CircuitHalfOpen {
			next.HalfOpenSuccesses++
			if next.HalfOpenSuccesses >= policy.HalfOpenSuccesses {
				next.State = CircuitClosed
				next.HalfOpenSuccesses = 0
				next.OpenedUntil = nil
				next.FenceToken++
			}
		} else {
			next.State = CircuitClosed
			next.HalfOpenSuccesses = 0
			next.OpenedUntil = nil
		}
		return next
	}
	next.HalfOpenSuccesses = 0
	next.ConsecutiveFailures++
	if current.State == CircuitHalfOpen || next.ConsecutiveFailures >= policy.FailureThreshold {
		next.State = CircuitOpen
		until := observedAt.Add(policy.OpenFor)
		next.OpenedUntil = &until
		next.FenceToken++
	}
	return next
}
