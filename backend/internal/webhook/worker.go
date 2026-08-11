package webhook

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type Job struct {
	DeliveryID    string
	ClaimToken    string
	EventID       string
	EndpointURL   string
	SigningKeyID  string
	SigningSecret []byte
	CanonicalBody []byte
	Attempt       uint32
	// PreparationError is a non-secret failure category populated when a
	// claimed job cannot be prepared (for example, KMS decryption fails).
	PreparationError string
}
type SendResult struct {
	StatusCode   int
	ResponseBody []byte
}
type Sender interface {
	Send(context.Context, Job, map[string]string) (SendResult, error)
}
type WorkerStore interface {
	Claim(context.Context, string, time.Time, time.Duration, int) ([]Job, error)
	Acknowledge(context.Context, string, string, int, []byte) error
	ScheduleRetry(context.Context, string, string, time.Time, string) error
	MoveToDeadLetter(context.Context, string, string, string) error
}
type Worker struct {
	Store  WorkerStore
	Sender Sender
	Policy RetryPolicy
	Clock  func() time.Time
	Lease  time.Duration
}

func (w *Worker) RunBatch(ctx context.Context, workerID string, limit int) (int, error) {
	if w.Store == nil || w.Sender == nil {
		return 0, errors.New("callback worker dependencies are required")
	}
	now := time.Now().UTC()
	if w.Clock != nil {
		now = w.Clock().UTC()
	}
	lease := w.Lease
	if lease <= 0 {
		lease = 30 * time.Second
	}
	jobs, err := w.Store.Claim(ctx, workerID, now, lease, limit)
	if err != nil {
		return 0, err
	}
	var jobErrors []error
	for _, job := range jobs {
		if err := w.process(ctx, job, now); err != nil {
			jobErrors = append(jobErrors, fmt.Errorf("callback delivery %s: %w", job.DeliveryID, err))
		}
	}
	return len(jobs), errors.Join(jobErrors...)
}
func (w *Worker) process(ctx context.Context, job Job, now time.Time) error {
	if job.PreparationError != "" {
		if w.Policy.Limit > 0 && job.Attempt >= w.Policy.Limit {
			return w.Store.MoveToDeadLetter(ctx, job.DeliveryID, job.ClaimToken, job.PreparationError)
		}
		return w.Store.ScheduleRetry(ctx, job.DeliveryID, job.ClaimToken, now.Add(w.Policy.Delay(job.Attempt)), job.PreparationError)
	}
	signature := Sign(job.SigningSecret, job.SigningKeyID, job.EventID, now, job.CanonicalBody)
	headers := DeliveryHeaders(signature, job.DeliveryID, job.CanonicalBody)
	result, sendErr := w.Sender.Send(ctx, job, headers)
	if sendErr == nil && result.StatusCode >= 200 && result.StatusCode < 300 {
		if err := ValidateAcknowledgement(result.ResponseBody, job.EventID); err != nil {
			sendErr = err
		}
		if sendErr == nil {
			return w.Store.Acknowledge(ctx, job.DeliveryID, job.ClaimToken, result.StatusCode, result.ResponseBody)
		}
	}
	reason := "delivery_failed"
	if sendErr != nil {
		reason = "transport_or_acknowledgement_error"
	} else {
		reason = fmt.Sprintf("http_status_%d", result.StatusCode)
	}
	if w.Policy.Limit > 0 && job.Attempt >= w.Policy.Limit {
		return w.Store.MoveToDeadLetter(ctx, job.DeliveryID, job.ClaimToken, reason)
	}
	return w.Store.ScheduleRetry(ctx, job.DeliveryID, job.ClaimToken, now.Add(w.Policy.Delay(job.Attempt)), reason)
}
