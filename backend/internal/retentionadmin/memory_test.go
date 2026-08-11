package retentionadmin

import (
	"context"
	"crypto/sha256"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestConcurrentPolicyReplayAllocatesOneRequest(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	repository := NewMemoryRepository(func() time.Time { return now })
	service, _ := NewService(repository, func() time.Time { return now })
	input := RequestPolicyInput{TenantID: testTenant, DataClass: PublishedOutbox, Proposal: PolicyProposal{ArchiveAfterDays: 30, PruneGraceDays: 7, ObjectLockDays: 90, PruneEnabled: true}, ScheduledFor: now.Add(time.Hour), Reason: "approved retention policy request"}
	key := Idempotency{Key: "concurrent-policy-001", Fingerprint: sha256.Sum256([]byte("same"))}
	const callers = 16
	results := make(chan PolicyChange, callers)
	errorsSeen := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			value, err := service.RequestPolicy(context.Background(), principal(now, "retention:policy_request"), input, key)
			results <- value
			errorsSeen <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errorsSeen)
	var id string
	for err := range errorsSeen {
		if err != nil {
			t.Fatal(err)
		}
	}
	for value := range results {
		if id == "" {
			id = value.ID
		} else if value.ID != id {
			t.Fatalf("replay allocated %s and %s", id, value.ID)
		}
	}
	page, _ := repository.ListPolicyChanges(context.Background(), principal(now, "retention:read"), Scope{TenantID: testTenant}, "", 50)
	if len(page.Items) != 1 {
		t.Fatalf("got %d logical requests", len(page.Items))
	}
}

func TestScheduledPolicyDoesNotActivateBeforeApprovedSchedule(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	clock := now
	repository := NewMemoryRepository(func() time.Time { return clock })
	requester := principal(now, "retention:policy_request")
	change, err := repository.RequestPolicy(context.Background(), requester, RequestPolicyInput{TenantID: testTenant, DataClass: PublishedOutbox, Proposal: PolicyProposal{ArchiveAfterDays: 30, PruneGraceDays: 7, ObjectLockDays: 90, PruneEnabled: true}, ScheduledFor: now.Add(time.Hour), Reason: "approved retention policy request"}, Idempotency{Key: "schedule-request-01", Fingerprint: sha256.Sum256([]byte("request"))})
	if err != nil {
		t.Fatal(err)
	}
	approver := Principal{ActorID: "20000000-0000-7000-8000-000000000002", SessionID: "session-2", StepUpAt: now}
	change, err = repository.DecidePolicy(context.Background(), approver, Scope{TenantID: testTenant}, change.ID, true, DecisionInput{ExpectedRowVersion: 1, Reason: "independent approval complete"}, Idempotency{Key: "schedule-approve-01", Fingerprint: sha256.Sum256([]byte("approve"))})
	if err != nil {
		t.Fatal(err)
	}
	if processed, _ := repository.AdvanceDue(context.Background(), "worker-1", 10); processed != 0 {
		t.Fatal("policy activated before scheduled time")
	}
	clock = now.Add(time.Hour)
	if processed, _ := repository.AdvanceDue(context.Background(), "worker-1", 10); processed != 1 {
		t.Fatalf("due policy was not activated: %d", processed)
	}
	policies, _ := repository.ListPolicies(context.Background(), principal(now, "retention:read"), Scope{TenantID: testTenant})
	if len(policies) != 1 || change.Status != PolicyScheduled {
		t.Fatal("scheduled policy lifecycle is inconsistent")
	}
}

func TestHoldExpiryIsNotSilentAndReleaseNeedsThirdParty(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	clock := now
	repository := NewMemoryRepository(func() time.Time { return clock })
	creator := principal(now, "retention:hold_create")
	expires := now.Add(time.Hour)
	hold, err := repository.CreateHold(context.Background(), creator, CreateHoldInput{TenantID: testTenant, DataClass: CallbackEventBody, ScopeType: HoldTenant, CaseReference: "CASE-EXPIRE-1", Reason: "preserve callback evidence", ExpiresAt: &expires}, Idempotency{Key: "hold-create-0001", Fingerprint: sha256.Sum256([]byte("hold"))})
	if err != nil {
		t.Fatal(err)
	}
	clock = now.Add(2 * time.Hour)
	page, _ := repository.ListHolds(context.Background(), principal(now, "retention:read"), Scope{TenantID: testTenant}, "", 10)
	if page.Items[0].State() != "active" {
		t.Fatal("wall clock silently disabled hold")
	}
	if processed, _ := repository.AdvanceDue(context.Background(), "worker-1", 10); processed != 1 {
		t.Fatal("expiry transition did not run")
	}
	page, _ = repository.ListHolds(context.Background(), principal(now, "retention:read"), Scope{TenantID: testTenant}, "", 10)
	if page.Items[0].State() != "expired" {
		t.Fatal("expiry evidence was not recorded")
	}

	// A separate active hold proves the release decision boundary.
	hold, _ = repository.CreateHold(context.Background(), creator, CreateHoldInput{TenantID: testTenant, DataClass: EventHistoryPayload, ScopeType: HoldTenant, CaseReference: "CASE-RELEASE-1", Reason: "preserve immutable history"}, Idempotency{Key: "hold-create-0002", Fingerprint: sha256.Sum256([]byte("hold2"))})
	release, _ := repository.RequestHoldRelease(context.Background(), creator, RequestReleaseInput{TenantID: testTenant, HoldID: hold.ID, ExpectedHoldVersion: 1, Reason: "legal matter is conclusively closed"}, Idempotency{Key: "hold-release-req", Fingerprint: sha256.Sum256([]byte("release"))})
	if _, err = repository.DecideHoldRelease(context.Background(), creator, Scope{TenantID: testTenant}, release.ID, true, DecisionInput{ExpectedRowVersion: 1, Reason: "self approval must be refused"}, Idempotency{Key: "hold-release-self", Fingerprint: sha256.Sum256([]byte("self"))}); !errors.Is(err, ErrConflict) {
		t.Fatalf("creator/requester approved own release: %v", err)
	}
	thirdParty := Principal{ActorID: "20000000-0000-7000-8000-000000000003", SessionID: "session-3", StepUpAt: clock}
	if _, err = repository.DecideHoldRelease(context.Background(), thirdParty, Scope{TenantID: testTenant}, release.ID, true, DecisionInput{ExpectedRowVersion: 1, Reason: "independent legal release approval"}, Idempotency{Key: "hold-release-third", Fingerprint: sha256.Sum256([]byte("third"))}); err != nil {
		t.Fatal(err)
	}
}

func TestHoldCaseReferenceRequiresHoldPermission(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	repository := NewMemoryRepository(func() time.Time { return now })
	creator := principal(now, "retention:hold_create")
	_, err := repository.CreateHold(context.Background(), creator, CreateHoldInput{TenantID: testTenant, DataClass: CallbackEventBody, ScopeType: HoldTenant, CaseReference: "CASE-PRIVATE-1", Reason: "preserve callback evidence"}, Idempotency{Key: "hold-private-01", Fingerprint: sha256.Sum256([]byte("private"))})
	if err != nil {
		t.Fatal(err)
	}
	readOnly, _ := repository.ListHolds(context.Background(), principal(now, "retention:read"), Scope{TenantID: testTenant}, "", 10)
	if readOnly.Items[0].CaseReference != "" {
		t.Fatal("read-only retention operator received case reference")
	}
	privileged := principal(now, "retention:read")
	privileged.Grants = append(privileged.Grants, Grant{Permission: "retention:hold_release", TenantID: testTenant})
	withHoldPermission, _ := repository.ListHolds(context.Background(), privileged, Scope{TenantID: testTenant}, "", 10)
	if withHoldPermission.Items[0].CaseReference != "CASE-PRIVATE-1" {
		t.Fatal("hold operator did not receive bounded case reference")
	}
}
