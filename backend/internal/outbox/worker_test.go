package outbox_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/outbox"
)

type storeFixture struct {
	jobs            []outbox.Job
	marked, retried []string
	retryAt         []time.Time
	fail            map[string]error
}

func (s *storeFixture) Claim(context.Context, string, time.Time, time.Duration, int) ([]outbox.Job, error) {
	return s.jobs, nil
}
func (s *storeFixture) MarkPublished(_ context.Context, job outbox.Job, _ time.Time) error {
	s.marked = append(s.marked, job.EventID)
	return s.fail[job.EventID]
}
func (s *storeFixture) Retry(_ context.Context, job outbox.Job, retryAt time.Time, _ string) error {
	s.retried = append(s.retried, job.EventID)
	s.retryAt = append(s.retryAt, retryAt)
	return nil
}

func TestOutboxWorkerIsolatesFailedEvents(t *testing.T) {
	store := &storeFixture{jobs: []outbox.Job{{EventID: "failed", ClaimToken: "a", Attempt: 1}, {EventID: "healthy", ClaimToken: "b", Attempt: 1}}, fail: map[string]error{"failed": errors.New("database unavailable")}}
	publisher := &publisherFixture{fail: map[string]error{"failed": errors.New("broker unavailable")}}
	worker := outbox.Worker{Store: store, Publisher: publisher}
	count, err := worker.RunBatch(t.Context(), "worker", 2)
	if count != 2 || err == nil || len(publisher.published) != 2 || len(store.marked) != 1 || store.marked[0] != "healthy" || len(store.retried) != 1 || store.retried[0] != "failed" {
		t.Fatalf("count=%d err=%v sent=%v marked=%v retried=%v", count, err, publisher.published, store.marked, store.retried)
	}
}

type publisherFixture struct {
	published []string
	fail      map[string]error
}

func (p *publisherFixture) Publish(_ context.Context, message outbox.Message) error {
	p.published = append(p.published, message.EventID)
	return p.fail[message.EventID]
}

func TestOutboxWorkerRetriesLostPublishAckWithStableID(t *testing.T) {
	store := &storeFixture{jobs: []outbox.Job{{EventID: "event-1", Attempt: 1}}, fail: map[string]error{}}
	publisher := &deduplicatingPublisher{loseFirstAck: true, seen: map[string]struct{}{}}
	worker := outbox.Worker{Store: store, Publisher: publisher, MaxRetryDelay: 15 * time.Minute}
	if _, err := worker.RunBatch(t.Context(), "worker", 1); err == nil {
		t.Fatal("expected simulated lost acknowledgement")
	}
	if _, err := worker.RunBatch(t.Context(), "worker", 1); err != nil {
		t.Fatalf("duplicate publish should be acknowledged: %v", err)
	}
	if publisher.calls != 2 || publisher.unique != 1 || len(store.marked) != 1 || len(store.retried) != 1 {
		t.Fatalf("calls=%d unique=%d marked=%v retried=%v", publisher.calls, publisher.unique, store.marked, store.retried)
	}
}

func TestOutboxWorkerSafelyRetriesAfterPostAckDatabaseFailure(t *testing.T) {
	store := &failFirstMarkStore{job: outbox.Job{EventID: "event-1", Attempt: 1}}
	publisher := &deduplicatingPublisher{seen: map[string]struct{}{}}
	worker := outbox.Worker{Store: store, Publisher: publisher, MaxRetryDelay: 15 * time.Minute}
	if _, err := worker.RunBatch(t.Context(), "worker", 1); err == nil {
		t.Fatal("expected fenced database mark failure")
	}
	if _, err := worker.RunBatch(t.Context(), "worker", 1); err != nil {
		t.Fatalf("retry after database recovery failed: %v", err)
	}
	if publisher.calls != 2 || publisher.unique != 1 || store.marks != 2 || store.retries != 1 {
		t.Fatalf("calls=%d unique=%d marks=%d retries=%d", publisher.calls, publisher.unique, store.marks, store.retries)
	}
}

func TestOutboxWorkerCapsBackpressureRetryAtAdmittedMaximum(t *testing.T) {
	now := time.Date(2026, 7, 27, 6, 0, 0, 0, time.UTC)
	store := &storeFixture{jobs: []outbox.Job{{EventID: "event-1", Attempt: 100}}, fail: map[string]error{}}
	publisher := &publisherFixture{fail: map[string]error{"event-1": errors.New("JetStream backpressure")}}
	worker := outbox.Worker{Store: store, Publisher: publisher, MaxRetryDelay: 2 * time.Minute, Clock: func() time.Time { return now }}
	if _, err := worker.RunBatch(t.Context(), "worker", 1); err == nil {
		t.Fatal("expected publish failure")
	}
	if len(store.retryAt) != 1 || !store.retryAt[0].Equal(now.Add(2*time.Minute)) {
		t.Fatalf("retry at %v", store.retryAt)
	}
}

type deduplicatingPublisher struct {
	seen         map[string]struct{}
	loseFirstAck bool
	calls        int
	unique       int
}

func (p *deduplicatingPublisher) Publish(_ context.Context, message outbox.Message) error {
	p.calls++
	_, duplicate := p.seen[message.EventID]
	if !duplicate {
		p.seen[message.EventID] = struct{}{}
		p.unique++
	}
	if p.loseFirstAck && p.calls == 1 {
		return errors.New("publish stored but acknowledgement was lost")
	}
	return nil
}

type failFirstMarkStore struct {
	job            outbox.Job
	marks, retries int
}

func (s *failFirstMarkStore) Claim(context.Context, string, time.Time, time.Duration, int) ([]outbox.Job, error) {
	return []outbox.Job{s.job}, nil
}

func (s *failFirstMarkStore) MarkPublished(context.Context, outbox.Job, time.Time) error {
	s.marks++
	if s.marks == 1 {
		return errors.New("database unavailable after publish acknowledgement")
	}
	return nil
}

func (s *failFirstMarkStore) Retry(context.Context, outbox.Job, time.Time, string) error {
	s.retries++
	return nil
}
