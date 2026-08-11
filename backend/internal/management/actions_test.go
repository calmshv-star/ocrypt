package management

import (
	"context"
	"errors"
	"testing"
	"time"
)

type actionRepositoryStub struct {
	Repository
	action        ManagementActionRequest
	mutationCalls int
	stale         bool
}

func (r *actionRepositoryStub) CreateManagementAction(_ context.Context, _ Principal, value ManagementActionRequest) (ManagementActionRequest, bool, error) {
	if r.action.ID != "" {
		if r.action.RequestHash != value.RequestHash {
			return ManagementActionRequest{}, false, ErrIdempotencyConflict
		}
		return r.action, true, nil
	}
	r.action = value
	return value, false, nil
}
func (r *actionRepositoryStub) GetManagementAction(_ context.Context, _ Principal, operation, id string) (ManagementActionRequest, error) {
	if r.action.ID != id || r.action.Operation != operation {
		return ManagementActionRequest{}, ErrNotFound
	}
	return r.action, nil
}
func (r *actionRepositoryStub) ListManagementActions(context.Context, Principal, string, string, int) (Page[ManagementActionRequest], error) {
	return Page[ManagementActionRequest]{Data: []ManagementActionRequest{r.action}}, nil
}
func (r *actionRepositoryStub) ClaimManagementAction(_ context.Context, p Principal, operation, id, lease, reason string, hash [32]byte, now time.Time) (ManagementActionRequest, bool, error) {
	if r.action.ID != id || r.action.Operation != operation {
		return ManagementActionRequest{}, false, ErrNotFound
	}
	if r.action.RequestedBy == p.ActorID {
		return ManagementActionRequest{}, false, ErrForbidden
	}
	if !r.action.ExpiresAt.After(now) || r.action.Status == "failed" || r.action.Status == "rejected" {
		return ManagementActionRequest{}, false, ErrConflict
	}
	if r.action.Status == "completed" {
		if r.action.ApprovedBy != p.ActorID || r.action.ApprovalReason != reason || r.action.ApprovalHash != hash {
			return ManagementActionRequest{}, false, ErrConflict
		}
		return r.action, true, nil
	}
	if r.action.ApprovedBy != "" && (r.action.ApprovedBy != p.ActorID || r.action.ApprovalReason != reason || r.action.ApprovalHash != hash) {
		return ManagementActionRequest{}, false, ErrConflict
	}
	r.action.ApprovedBy, r.action.ApprovalReason, r.action.ApprovalHash = p.ActorID, reason, hash
	r.action.LeaseToken, r.action.Status = lease, "executing"
	return r.action, false, nil
}
func (r *actionRepositoryStub) RejectManagementAction(_ context.Context, p Principal, operation, id, reason string, hash [32]byte, now time.Time) (ManagementActionRequest, bool, error) {
	if r.action.ID != id || r.action.Operation != operation || r.action.RequestedBy == p.ActorID || !r.action.ExpiresAt.After(now) {
		return ManagementActionRequest{}, false, ErrForbidden
	}
	r.action.ApprovedBy, r.action.ApprovalReason, r.action.ApprovalHash, r.action.Status = p.ActorID, reason, hash, "rejected"
	return r.action, false, nil
}
func (r *actionRepositoryStub) CompleteManagementAction(_ context.Context, _ Principal, id, lease string, succeeded bool, failure string, now time.Time) (ManagementActionRequest, error) {
	if r.action.ID != id || r.action.LeaseToken != lease || r.action.Status != "executing" {
		return ManagementActionRequest{}, ErrConflict
	}
	r.action.Status, r.action.FailureCode = "completed", ""
	if !succeeded {
		r.action.Status, r.action.FailureCode = "failed", failure
	}
	r.action.CompletedAt = &now
	return r.action, nil
}
func (r *actionRepositoryStub) RevokeAPIClient(_ context.Context, _ Principal, _ string, _ int64, _ string, _ Idempotency) (APIClient, bool, error) {
	r.mutationCalls++
	if r.stale {
		return APIClient{}, false, ErrConflict
	}
	return APIClient{}, false, nil
}

type verifierNoop struct{}

func (verifierNoop) Verify(context.Context, string, string) error { return nil }

func actionServiceFixture(t *testing.T, repo Repository, now time.Time) *Service {
	t.Helper()
	box, err := NewWebhookSecretBox([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(repo, box, box, verifierNoop{}, "https://checkout.example")
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return now }
	return service
}

func TestManagementActionRequiresDistinctApproverAndIsReplaySafe(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	repo := &actionRepositoryStub{}
	service := actionServiceFixture(t, repo, now)
	requester := Principal{TenantID: "0198a6d7-42b5-7a10-8000-000000000001", MerchantID: "0198a6d7-42b5-7a10-8000-000000000002", ActorID: "0198a6d7-42b5-7a10-8000-000000000003", SessionID: "requester-session", AuthMethod: "admin_assertion", Scopes: map[string]bool{"credentials:revoke": true}, StepUpAt: now}
	resourceID := "0198a6d7-42b5-7a10-8000-000000000004"
	var requestHash [32]byte
	requestHash[0] = 1
	action, _, err := service.RequestManagementAction(context.Background(), requester, actionRevokeClient, resourceID, 7, "credential compromised", Idempotency{Key: "revoke-request-001", Fingerprint: requestHash})
	if err != nil {
		t.Fatal(err)
	}
	var approvalHash [32]byte
	approvalHash[0] = 2
	if _, _, err = service.ApproveManagementAction(context.Background(), requester, actionRevokeClient, action.ID, "independently verified", approvalHash); !errors.Is(err, ErrForbidden) {
		t.Fatalf("self approval accepted: %v", err)
	}
	approver := requester
	approver.ActorID, approver.SessionID = "0198a6d7-42b5-7a10-8000-000000000005", "approver-session"
	completed, replay, err := service.ApproveManagementAction(context.Background(), approver, actionRevokeClient, action.ID, "independently verified", approvalHash)
	if err != nil || replay || completed.Status != "completed" || repo.mutationCalls != 1 {
		t.Fatalf("approval failed: status=%s replay=%v calls=%d err=%v", completed.Status, replay, repo.mutationCalls, err)
	}
	completed, replay, err = service.ApproveManagementAction(context.Background(), approver, actionRevokeClient, action.ID, "independently verified", approvalHash)
	if err != nil || !replay || completed.Status != "completed" || repo.mutationCalls != 1 {
		t.Fatalf("approval replay was not fenced: status=%s replay=%v calls=%d err=%v", completed.Status, replay, repo.mutationCalls, err)
	}
	swapped := approvalHash
	swapped[0]++
	if _, _, err = service.ApproveManagementAction(context.Background(), approver, actionRevokeClient, action.ID, "independently verified", swapped); !errors.Is(err, ErrConflict) {
		t.Fatalf("approval body/path hash swap accepted: %v", err)
	}
}

func TestManagementActionRejectsExpiredAndRecordsStaleVersion(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	principal := Principal{TenantID: "0198a6d7-42b5-7a10-8000-000000000011", MerchantID: "0198a6d7-42b5-7a10-8000-000000000012", ActorID: "0198a6d7-42b5-7a10-8000-000000000013", SessionID: "requester-session", AuthMethod: "admin_assertion", Scopes: map[string]bool{"credentials:revoke": true}, StepUpAt: now}
	approver := principal
	approver.ActorID = "0198a6d7-42b5-7a10-8000-000000000014"
	repo := &actionRepositoryStub{action: ManagementActionRequest{ID: "0198a6d7-42b5-7a10-8000-000000000015", Operation: actionRevokeClient, RequestedBy: principal.ActorID, ExpiresAt: now.Add(-time.Second), Status: "pending_approval"}}
	service := actionServiceFixture(t, repo, now)
	if _, _, err := service.ApproveManagementAction(context.Background(), approver, actionRevokeClient, repo.action.ID, "reviewed", [32]byte{1}); !errors.Is(err, ErrConflict) {
		t.Fatalf("expired action accepted: %v", err)
	}
	repo.action = ManagementActionRequest{ID: repo.action.ID, Operation: actionRevokeClient, ResourceID: "0198a6d7-42b5-7a10-8000-000000000016", ResourceVersion: 3, RequestReason: "revoke", RequestedBy: principal.ActorID, RequestedSession: principal.SessionID, RequestedStepUpAt: now, MutationIdempotencyKey: "stale-revoke-001", RequestBody: []byte(`{"version":3,"reason":"revoke"}`), ExpiresAt: now.Add(time.Minute), Status: "pending_approval"}
	repo.stale = true
	failed, _, err := service.ApproveManagementAction(context.Background(), approver, actionRevokeClient, repo.action.ID, "reviewed", [32]byte{2})
	if !errors.Is(err, ErrConflict) || failed.Status != "failed" || failed.FailureCode != "stale_resource_version" {
		t.Fatalf("stale version was not durably failed: status=%s code=%s err=%v", failed.Status, failed.FailureCode, err)
	}
}
