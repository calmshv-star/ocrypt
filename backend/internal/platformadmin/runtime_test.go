package platformadmin

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type schedulerRepository struct {
	job   ActivationJob
	input ActivateInput
	calls int
}

func (r *schedulerRepository) ClaimDueActivations(context.Context, string, string, int, time.Duration) ([]ActivationJob, error) {
	return []ActivationJob{r.job}, nil
}
func (r *schedulerRepository) Activate(_ context.Context, _ Principal, _ Scope, _ string, input ActivateInput, _ Idempotency) (Snapshot, error) {
	r.calls++
	r.input = input
	if input.LeaseToken != r.job.ClaimToken {
		return Snapshot{}, ErrConflict
	}
	return Snapshot{ID: "activated"}, nil
}

func TestSchedulerCarriesLeaseFenceIntoActivation(t *testing.T) {
	repository := &schedulerRepository{job: ActivationJob{ChangeID: "018f0f65-7a34-7cc4-9f36-7a86496ee480", TenantID: testTenant, RowVersion: 4, ExpectedFenceToken: 9, ClaimToken: 7}}
	scheduler, err := NewScheduler(repository, "scheduler-a", "018f0f65-7a34-7cc4-9f36-7a86496ee481")
	if err != nil {
		t.Fatal(err)
	}
	count, err := scheduler.RunOnce(context.Background(), 1)
	if err != nil || count != 1 {
		t.Fatalf("run count=%d err=%v", count, err)
	}
	if repository.input.LeaseOwner != "scheduler-a" || repository.input.LeaseToken != 7 || repository.input.ExpectedFenceToken != 9 {
		t.Fatalf("claim fence lost: %#v", repository.input)
	}
}

type fencedOutbox struct {
	mu        sync.Mutex
	event     OutboxEvent
	leased    bool
	published bool
}

func (s *fencedOutbox) ClaimPlatformOutbox(context.Context, string, int, time.Duration) ([]OutboxEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.event.ClaimToken++
	s.leased = true
	return []OutboxEvent{s.event}, nil
}
func (s *fencedOutbox) MarkPlatformOutboxPublished(_ context.Context, event OutboxEvent, _ string, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.leased || event.ClaimToken != s.event.ClaimToken {
		return ErrConflict
	}
	s.published = true
	s.leased = false
	return nil
}
func (s *fencedOutbox) ReleasePlatformOutbox(_ context.Context, event OutboxEvent, _ string, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.leased || event.ClaimToken != s.event.ClaimToken {
		return ErrConflict
	}
	s.leased = false
	return nil
}

func TestOutboxStaleSameWorkerCannotAcknowledgeNewClaim(t *testing.T) {
	store := &fencedOutbox{event: OutboxEvent{ID: "018f0f65-7a34-7cc4-9f36-7a86496ee482"}}
	first, _ := store.ClaimPlatformOutbox(context.Background(), "worker", 1, time.Minute)
	store.leased = false
	second, _ := store.ClaimPlatformOutbox(context.Background(), "worker", 1, time.Minute)
	if err := store.MarkPlatformOutboxPublished(context.Background(), first[0], "worker", time.Now()); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale claim acknowledged: %v", err)
	}
	if err := store.MarkPlatformOutboxPublished(context.Background(), second[0], "worker", time.Now()); err != nil {
		t.Fatalf("current claim rejected: %v", err)
	}
}
