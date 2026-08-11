package retentionadmin

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"
)

const (
	testTenant = "10000000-0000-7000-8000-000000000001"
	testActor  = "20000000-0000-7000-8000-000000000001"
	testHold   = "30000000-0000-7000-8000-000000000001"
)

type fakeRepository struct {
	Repository
	requestedPolicy *RequestPolicyInput
	createdHold     *CreateHoldInput
	release         *RequestReleaseInput
}

func (r *fakeRepository) RequestPolicy(_ context.Context, _ Principal, input RequestPolicyInput, _ Idempotency) (PolicyChange, error) {
	r.requestedPolicy = &input
	return PolicyChange{ID: testHold}, nil
}

func (r *fakeRepository) CreateHold(_ context.Context, _ Principal, input CreateHoldInput, _ Idempotency) (LegalHold, error) {
	r.createdHold = &input
	return LegalHold{ID: testHold}, nil
}

func (r *fakeRepository) RequestHoldRelease(_ context.Context, _ Principal, input RequestReleaseInput, _ Idempotency) (HoldReleaseRequest, error) {
	r.release = &input
	return HoldReleaseRequest{ID: testHold}, nil
}

func principal(now time.Time, permission string) Principal {
	return Principal{ActorID: testActor, SessionID: "session-1", StepUpAt: now.Add(-time.Minute), Grants: []Grant{{Permission: permission, TenantID: testTenant}}}
}

func idem() Idempotency {
	return Idempotency{Key: "request-key-0001", Fingerprint: sha256.Sum256([]byte("request"))}
}

func TestPolicyRejectsPruningArchiveOnlyClasses(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	repository := &fakeRepository{}
	service, _ := NewService(repository, func() time.Time { return now })
	_, err := service.RequestPolicy(context.Background(), principal(now, "retention:policy_request"), RequestPolicyInput{
		TenantID: testTenant, DataClass: CallbackEventBody, Proposal: PolicyProposal{ArchiveAfterDays: 30, PruneGraceDays: 7, ObjectLockDays: 90, PruneEnabled: true}, ScheduledFor: now.Add(time.Hour), Reason: "approved retention policy request",
	}, idem())
	if !errors.Is(err, ErrInvalid) || repository.requestedPolicy != nil {
		t.Fatalf("archive-only prune must fail before repository, got %v", err)
	}
}

func TestPolicyRequiresFreshMFASpecificPermission(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	service, _ := NewService(&fakeRepository{}, func() time.Time { return now })
	input := RequestPolicyInput{TenantID: testTenant, DataClass: PublishedOutbox, Proposal: PolicyProposal{ArchiveAfterDays: 30, PruneGraceDays: 7, ObjectLockDays: 90, PruneEnabled: true}, ScheduledFor: now.Add(time.Hour), Reason: "approved retention policy request"}
	wrong := principal(now, "retention:read")
	if _, err := service.RequestPolicy(context.Background(), wrong, input, idem()); !errors.Is(err, ErrForbidden) {
		t.Fatalf("wrong permission: %v", err)
	}
	stale := principal(now, "retention:policy_request")
	stale.StepUpAt = now.Add(-11 * time.Minute)
	if _, err := service.RequestPolicy(context.Background(), stale, input, idem()); !errors.Is(err, ErrStepUpRequired) {
		t.Fatalf("stale MFA: %v", err)
	}
}

func TestLegalHoldScopeIsExactlyOneShape(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	repository := &fakeRepository{}
	service, _ := NewService(repository, func() time.Time { return now })
	p := principal(now, "retention:hold_create")
	input := CreateHoldInput{TenantID: testTenant, DataClass: EventHistoryPayload, ScopeType: HoldTenant, MerchantID: "40000000-0000-7000-8000-000000000001", CaseReference: "CASE-2026-001", Reason: "regulatory preservation order"}
	if _, err := service.CreateHold(context.Background(), p, input, idem()); !errors.Is(err, ErrInvalid) {
		t.Fatalf("mixed tenant and merchant scope accepted: %v", err)
	}
	input.ScopeType, input.MerchantID, input.SourceTable, input.SourceRecordID = HoldRecord, "40000000-0000-7000-8000-000000000001", "event_history", "50000000-0000-7000-8000-000000000001"
	if _, err := service.CreateHold(context.Background(), p, input, idem()); err != nil || repository.createdHold == nil {
		t.Fatalf("valid record hold rejected: %v", err)
	}
}

func TestReleaseRequiresReleasePermissionAndExpectedVersion(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	repository := &fakeRepository{}
	service, _ := NewService(repository, func() time.Time { return now })
	input := RequestReleaseInput{TenantID: testTenant, HoldID: testHold, ExpectedHoldVersion: 1, Reason: "legal matter is conclusively closed"}
	if _, err := service.RequestHoldRelease(context.Background(), principal(now, "retention:hold_create"), input, idem()); !errors.Is(err, ErrForbidden) {
		t.Fatalf("hold creator permission improperly released hold: %v", err)
	}
	input.ExpectedHoldVersion = 0
	if _, err := service.RequestHoldRelease(context.Background(), principal(now, "retention:hold_release"), input, idem()); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing expected version accepted: %v", err)
	}
}
