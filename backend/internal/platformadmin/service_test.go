package platformadmin

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"
)

func principal(actor string, now time.Time, tenant string, permissions ...string) Principal {
	grants := make([]Grant, 0, len(permissions))
	for _, permission := range permissions {
		grants = append(grants, Grant{Permission: permission, TenantID: tenant})
	}
	return Principal{ActorID: actor, SessionID: "session", Audience: "platform-admin", StepUpAt: now, Grants: grants}
}
func validChainInput() CreateInput {
	return CreateInput{TenantID: testTenant, Kind: KindChain, LogicalKey: "chain/ethereum-mainnet", BasedOnVersion: 0, Payload: json.RawMessage(`{"family":"evm","network":"ethereum-mainnet","status":"active"}`), Reason: "admit production chain"}
}
func idem(key string, value any) Idempotency {
	out, err := NewIdempotency(key, value)
	if err != nil {
		panic(err)
	}
	return out
}

func TestVersionedWorkflowFourEyesScheduleActivationAndRollback(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	repository := newMemoryRepository(clock)
	service, _ := NewService(repository, clock)
	creator := principal(testActor, now, testTenant, "platform_config:write", "platform_config:request", "platform_config:approve", "platform_config:rollback")
	approver := principal("018f0f65-7a34-7cc4-9f36-7a86496ee466", now, testTenant, "platform_config:approve", "platform_config:schedule", "platform_config:activate")
	input := validChainInput()
	draft, err := service.CreateDraft(context.Background(), creator, input, idem("draft-key", input))
	if err != nil {
		t.Fatal(err)
	}
	requested, err := service.RequestApproval(context.Background(), creator, Scope{testTenant}, draft.ID, DecisionInput{ExpectedRowVersion: 1, Reason: "ready for review"}, idem("request-key", draft.ID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Decide(context.Background(), creator, Scope{testTenant}, draft.ID, true, DecisionInput{ExpectedRowVersion: requested.RowVersion, Reason: "self approve"}, idem("self-key", draft.ID)); !errors.Is(err, ErrForbidden) {
		t.Fatalf("self approval: %v", err)
	}
	approved, err := service.Decide(context.Background(), approver, Scope{testTenant}, draft.ID, true, DecisionInput{ExpectedRowVersion: requested.RowVersion, Reason: "independent approval"}, idem("approve-key", draft.ID))
	if err != nil {
		t.Fatal(err)
	}
	activateAt := now.Add(time.Minute)
	scheduled, err := service.Schedule(context.Background(), approver, Scope{testTenant}, draft.ID, ScheduleInput{ExpectedRowVersion: approved.RowVersion, ActivateAt: activateAt, Reason: "controlled release"}, idem("schedule-key", draft.ID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Activate(context.Background(), approver, Scope{testTenant}, draft.ID, ActivateInput{ExpectedRowVersion: scheduled.RowVersion, ExpectedFenceToken: 0, Reason: "too early"}, idem("early-key", draft.ID)); !errors.Is(err, ErrScheduledForFuture) {
		t.Fatalf("early activation: %v", err)
	}
	now = activateAt
	approver.StepUpAt = now
	snapshot, err := service.Activate(context.Background(), approver, Scope{testTenant}, draft.ID, ActivateInput{ExpectedRowVersion: scheduled.RowVersion, ExpectedFenceToken: 0, Reason: "scheduled release"}, idem("activate-key", draft.ID))
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.FenceToken != 1 || snapshot.Version != 1 {
		t.Fatalf("unexpected snapshot %#v", snapshot)
	}
	creator.StepUpAt = now
	rollback, err := service.Rollback(context.Background(), creator, RollbackInput{TenantID: testTenant, SnapshotID: snapshot.ID, Reason: "restore admitted history"}, idem("rollback-key", snapshot.ID))
	if err != nil {
		t.Fatal(err)
	}
	if rollback.Version != 2 || rollback.BasedOnVersion != 1 || rollback.RollbackOfSnapshotID != snapshot.ID || rollback.Status != StatusDraft {
		t.Fatalf("rollback is not a new version: %#v", rollback)
	}
	if repository.snapshots[snapshot.ID].ID == "" {
		t.Fatal("historic snapshot was mutated or removed")
	}
}

func TestScopePermissionsAreBoundAndRevocationTakesEffect(t *testing.T) {
	now := time.Now().UTC()
	repository := newMemoryRepository(func() time.Time { return now })
	service, _ := NewService(repository, func() time.Time { return now })
	actor := principal(testActor, now, testTenant, "platform_config:write")
	input := validChainInput()
	input.TenantID = "018f0f65-7a34-7cc4-9f36-7a86496ee499"
	if _, err := service.CreateDraft(context.Background(), actor, input, idem("wrong-tenant", input)); !errors.Is(err, ErrForbidden) {
		t.Fatalf("cross-tenant write: %v", err)
	}
	actor.Grants = nil
	input = validChainInput()
	if _, err := service.CreateDraft(context.Background(), actor, input, idem("revoked-key", input)); !errors.Is(err, ErrForbidden) {
		t.Fatalf("removed DB-derived grant still worked: %v", err)
	}
}

func TestConcurrentFirstUseIdempotencyAndScheduleRace(t *testing.T) {
	now := time.Now().UTC()
	repository := newMemoryRepository(func() time.Time { return now })
	p := principal(testActor, now, testTenant, "platform_config:write")
	input := validChainInput()
	token := idem("concurrent-draft", input)
	results := make(chan ChangeRequest, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := repository.CreateDraft(context.Background(), p, input, token)
			results <- v
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var id string
	for result := range results {
		if id == "" {
			id = result.ID
		} else if result.ID != id {
			t.Fatalf("idempotent requests created %s and %s", id, result.ID)
		}
	}
	changed := token
	changed.Fingerprint[0] ^= 0xff
	if _, err := repository.CreateDraft(context.Background(), p, input, changed); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("different hash was not rejected: %v", err)
	}
	repository.mu.Lock()
	v := repository.changes[id]
	v.Status = StatusApproved
	v.RowVersion = 2
	repository.changes[id] = v
	repository.mu.Unlock()
	success := 0
	conflicts := 0
	wg = sync.WaitGroup{}
	var guard sync.Mutex
	for index := range 2 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := repository.Schedule(context.Background(), p, Scope{testTenant}, id, ScheduleInput{ExpectedRowVersion: 2, ActivateAt: now.Add(time.Minute), Reason: "race"}, idem("schedule-race-"+string(rune('a'+i)), i))
			guard.Lock()
			defer guard.Unlock()
			if err == nil {
				success++
			} else if errors.Is(err, ErrConflict) {
				conflicts++
			}
		}(index)
	}
	wg.Wait()
	if success != 1 || conflicts != 1 {
		t.Fatalf("schedule race success=%d conflict=%d", success, conflicts)
	}
}
