package matchingadmin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/management"
)

const (
	tenantID    = "0198a6d7-42b5-7a10-8000-000000000001"
	merchantID  = "0198a6d7-42b5-7a10-8000-000000000002"
	requesterID = "0198a6d7-42b5-7a10-8000-000000000003"
	approverID  = "0198a6d7-42b5-7a10-8000-000000000004"
	changeID    = "0198a6d7-42b5-7a10-8000-000000000005"
)

type memoryRepository struct{ value PolicyChange }

func (s *memoryRepository) Create(_ context.Context, p management.Principal, input PolicyInput, _ Idempotency) (PolicyChange, bool, error) {
	s.value = PolicyChange{ID: changeID, ProposedVersion: 1, AccumulatePartials: input.AccumulatePartials, UnderpaymentToleranceBPS: input.UnderpaymentToleranceBPS, OverpaymentMode: input.OverpaymentMode, AcceptLateWithinGrace: input.AcceptLateWithinGrace, RequireSameSender: input.RequireSameSender, GasFreeEnabled: input.GasFreeEnabled, GasFreeFeeCollectors: input.GasFreeFeeCollectors, Status: "draft", CreatedBy: p.ActorID, Version: 1}
	return s.value, false, nil
}
func (s *memoryRepository) Get(context.Context, management.Principal, string) (PolicyChange, error) {
	return s.value, nil
}
func (s *memoryRepository) List(context.Context, management.Principal, string, int) (Page, error) {
	return Page{Data: []PolicyChange{s.value}}, nil
}
func (s *memoryRepository) RequestApproval(_ context.Context, p management.Principal, _ string, input Mutation, _ Idempotency) (PolicyChange, bool, error) {
	if s.value.Status != "draft" || s.value.Version != input.Version {
		return PolicyChange{}, false, ErrConflict
	}
	s.value.Status, s.value.RequestedBy, s.value.Version = "pending_approval", p.ActorID, s.value.Version+1
	return s.value, false, nil
}
func (s *memoryRepository) Approve(_ context.Context, p management.Principal, _ string, input Mutation, _ Idempotency) (PolicyChange, bool, error) {
	if s.value.Status != "pending_approval" || s.value.Version != input.Version || s.value.RequestedBy == p.ActorID {
		return PolicyChange{}, false, ErrConflict
	}
	s.value.Status, s.value.ApprovedBy, s.value.Version = "approved", p.ActorID, s.value.Version+1
	return s.value, false, nil
}
func (s *memoryRepository) Activate(_ context.Context, p management.Principal, _ string, input Activation, _ Idempotency) (PolicyChange, bool, error) {
	if s.value.Status != "approved" || s.value.Version != input.Version || s.value.RequestedBy == p.ActorID {
		return PolicyChange{}, false, ErrConflict
	}
	s.value.Status, s.value.ActivatedBy, s.value.Version = "activated", p.ActorID, s.value.Version+1
	return s.value, false, nil
}

func adminPrincipal(actor, scope string, now time.Time) management.Principal {
	return management.Principal{TenantID: tenantID, MerchantID: merchantID, ActorID: actor, SessionID: "session", AuthMethod: "admin_assertion", Scopes: map[string]bool{scope: true}, StepUpAt: now}
}

func TestPolicyLifecycleRequiresMFAAndDistinctOperator(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	repository := &memoryRepository{}
	service, _ := NewService(repository)
	service.now = func() time.Time { return now }
	input := PolicyInput{AccumulatePartials: true, UnderpaymentToleranceBPS: 100, OverpaymentMode: "manual_review", RequireSameSender: true}
	requester := adminPrincipal(requesterID, ScopeWrite, now)
	created, _, err := service.Create(t.Context(), requester, input, Idempotency{})
	if err != nil || created.Status != "draft" {
		t.Fatalf("create: %#v %v", created, err)
	}
	requested, _, err := service.RequestApproval(t.Context(), requester, changeID, Mutation{Version: 1, Reason: "enable aggregation"}, Idempotency{})
	if err != nil || requested.Status != "pending_approval" {
		t.Fatalf("request: %#v %v", requested, err)
	}
	requester.Scopes = map[string]bool{ScopeApprove: true}
	if _, _, err := service.Approve(t.Context(), requester, changeID, Mutation{Version: 2, Reason: "reviewed policy"}, Idempotency{}); err == nil {
		t.Fatal("requester approved their own financial policy")
	}
	approver := adminPrincipal(approverID, ScopeApprove, now)
	approved, _, err := service.Approve(t.Context(), approver, changeID, Mutation{Version: 2, Reason: "reviewed policy"}, Idempotency{})
	if err != nil || approved.Status != "approved" {
		t.Fatalf("approve: %#v %v", approved, err)
	}
	requester.Scopes = map[string]bool{ScopeActivate: true}
	if _, _, err := service.Activate(t.Context(), requester, changeID, Activation{Version: 3, Reason: "activate policy", EffectiveAt: now.Add(time.Minute)}, Idempotency{}); err == nil {
		t.Fatal("requester activated their own financial policy")
	}
	approver.Scopes = map[string]bool{ScopeActivate: true}
	activated, _, err := service.Activate(t.Context(), approver, changeID, Activation{Version: 3, Reason: "activate policy", EffectiveAt: now.Add(time.Minute)}, Idempotency{})
	if err != nil || activated.Status != "activated" {
		t.Fatalf("activate: %#v %v", activated, err)
	}
	machine := requester
	machine.AuthMethod = "management_key"
	if _, _, err := service.Create(t.Context(), machine, input, Idempotency{}); err == nil {
		t.Fatal("machine credential bypassed interactive MFA")
	}
}

type authFunc func(context.Context, *http.Request, []byte) (management.Principal, error)

func (f authFunc) Authenticate(ctx context.Context, r *http.Request, body []byte) (management.Principal, error) {
	return f(ctx, r, body)
}

func TestHTTPRejectsDuplicateJSONBeforeRepository(t *testing.T) {
	now := time.Now().UTC()
	service, _ := NewService(&memoryRepository{})
	service.now = func() time.Time { return now }
	server, _ := NewServer(service, authFunc(func(context.Context, *http.Request, []byte) (management.Principal, error) {
		return adminPrincipal(requesterID, ScopeWrite, now), nil
	}), 64<<10)
	request := httptest.NewRequest(http.MethodPost, "/v1/management/matching-policies", strings.NewReader(`{"accumulate_partials":true,"accumulate_partials":false}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "policy-idem-0001")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("duplicate JSON accepted: %d %s", response.Code, response.Body.String())
	}
}

func TestBFFContractMapsOnlyDBPermissionsToNarrowScopes(t *testing.T) {
	seen := map[string]bool{}
	for _, route := range BFFRoutes {
		if !strings.HasPrefix(route.AdminPrefix, "/admin/v1/matching-policies") || !strings.HasPrefix(route.InternalPrefix, "/v1/management/matching-policies") || !strings.HasPrefix(route.AdminPermission, "matching_policy:") || !strings.HasPrefix(route.InternalScope, "matching-policies:") || seen[route.AdminPermission+"\x1f"+route.InternalScope] {
			t.Fatalf("invalid BFF mapping: %#v", route)
		}
		seen[route.AdminPermission+"\x1f"+route.InternalScope] = true
	}
}
