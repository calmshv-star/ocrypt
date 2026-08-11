package webhook

import (
	"context"
	"errors"
	"testing"
	"time"
)

type workerStoreFixture struct {
	jobs             []Job
	ack, retry, dead int
	ackErrors        map[string]error
}

func (s *workerStoreFixture) Claim(context.Context, string, time.Time, time.Duration, int) ([]Job, error) {
	return s.jobs, nil
}
func (s *workerStoreFixture) Acknowledge(_ context.Context, id, _ string, _ int, _ []byte) error {
	s.ack++
	return s.ackErrors[id]
}
func (s *workerStoreFixture) ScheduleRetry(context.Context, string, string, time.Time, string) error {
	s.retry++
	return nil
}
func (s *workerStoreFixture) MoveToDeadLetter(context.Context, string, string, string) error {
	s.dead++
	return nil
}

type senderFixture struct {
	result SendResult
	err    error
}

func (s senderFixture) Send(context.Context, Job, map[string]string) (SendResult, error) {
	return s.result, s.err
}
func TestCallbackWorkerAcknowledgesOnlyMatchingEvent(t *testing.T) {
	store := &workerStoreFixture{jobs: []Job{{DeliveryID: "d", ClaimToken: "claim", EventID: "evt", SigningKeyID: "key", SigningSecret: []byte("secret"), CanonicalBody: []byte(`{}`), Attempt: 1}}}
	worker := Worker{Store: store, Sender: senderFixture{result: SendResult{StatusCode: 200, ResponseBody: []byte(`{"acknowledged_event_id":"evt"}`)}}, Policy: RetryPolicy{Initial: time.Second, Limit: 3}}
	if _, err := worker.RunBatch(context.Background(), "worker", 1); err != nil {
		t.Fatal(err)
	}
	if store.ack != 1 || store.retry != 0 {
		t.Fatalf("unexpected state: %+v", store)
	}
}
func TestCallbackWorkerRetriesAndDeadLetters(t *testing.T) {
	job := Job{DeliveryID: "d", ClaimToken: "claim", EventID: "evt", SigningKeyID: "key", SigningSecret: []byte("secret"), CanonicalBody: []byte(`{}`), Attempt: 1}
	store := &workerStoreFixture{jobs: []Job{job}}
	worker := Worker{Store: store, Sender: senderFixture{err: errors.New("timeout")}, Policy: RetryPolicy{Initial: time.Second, Limit: 2}}
	if _, err := worker.RunBatch(context.Background(), "worker", 1); err != nil {
		t.Fatal(err)
	}
	if store.retry != 1 {
		t.Fatal("transient failure was not scheduled")
	}
	store.jobs[0].Attempt = 2
	if _, err := worker.RunBatch(context.Background(), "worker", 1); err != nil {
		t.Fatal(err)
	}
	if store.dead != 1 {
		t.Fatal("exhausted delivery was not dead-lettered")
	}
}

func TestCallbackWorkerRejectsEmptySuccessfulResponse(t *testing.T) {
	store := &workerStoreFixture{jobs: []Job{{DeliveryID: "d", ClaimToken: "claim", EventID: "evt", SigningKeyID: "key", SigningSecret: []byte("secret"), CanonicalBody: []byte(`{}`), Attempt: 1}}}
	worker := Worker{Store: store, Sender: senderFixture{result: SendResult{StatusCode: 204}}, Policy: RetryPolicy{Initial: time.Second, Limit: 3}}
	if _, err := worker.RunBatch(context.Background(), "worker", 1); err != nil {
		t.Fatal(err)
	}
	if store.ack != 0 || store.retry != 1 {
		t.Fatalf("empty acknowledgement was accepted: %+v", store)
	}
}

type senderByEvent struct{ responses map[string]SendResult }

func (s senderByEvent) Send(_ context.Context, job Job, _ map[string]string) (SendResult, error) {
	return s.responses[job.EventID], nil
}

func TestCallbackWorkerIsolatesPreparationAndOutcomeFailures(t *testing.T) {
	store := &workerStoreFixture{
		jobs: []Job{
			{DeliveryID: "decrypt-failed", ClaimToken: "claim-1", EventID: "evt-1", Attempt: 1, PreparationError: "signing_secret_decryption_failed"},
			{DeliveryID: "store-failed", ClaimToken: "claim-2", EventID: "evt-2", SigningKeyID: "key", SigningSecret: []byte("secret"), CanonicalBody: []byte(`{}`), Attempt: 1},
			{DeliveryID: "healthy", ClaimToken: "claim-3", EventID: "evt-3", SigningKeyID: "key", SigningSecret: []byte("secret"), CanonicalBody: []byte(`{}`), Attempt: 1},
		},
		ackErrors: map[string]error{"store-failed": errors.New("temporary database error")},
	}
	sender := senderByEvent{responses: map[string]SendResult{
		"evt-2": {StatusCode: 200, ResponseBody: []byte(`{"acknowledged_event_id":"evt-2"}`)},
		"evt-3": {StatusCode: 200, ResponseBody: []byte(`{"acknowledged_event_id":"evt-3"}`)},
	}}
	worker := Worker{Store: store, Sender: sender, Policy: RetryPolicy{Initial: time.Second, Limit: 3}}
	count, err := worker.RunBatch(context.Background(), "worker", 3)
	if count != 3 || err == nil {
		t.Fatalf("batch count=%d err=%v", count, err)
	}
	if store.retry != 1 || store.ack != 2 {
		t.Fatalf("healthy jobs were stranded: retry=%d ack=%d", store.retry, store.ack)
	}
}
