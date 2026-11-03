package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/domain"
)

type TransactionVerifier interface {
	LookupTransaction(context.Context, string, string) ([]domain.TransferEvent, error)
}

type ProofJob struct {
	Proof      domain.PaymentProof
	ClaimToken string
	Attempt    int
}

type ProofQueueStore interface {
	ClaimProofs(context.Context, string, string, time.Time, time.Duration, int) ([]ProofJob, error)
	CompleteProof(context.Context, ProofJob, []string, time.Time) error
	RetryProof(context.Context, ProofJob, time.Time, string, domain.ProofStatus) error
}

type ProofWorker struct {
	Verifier TransactionVerifier
	Queue    ProofQueueStore
	Process  *TransferProcessor
	Clock    func() time.Time
	Lease    time.Duration
	Limit    int
}

func (w ProofWorker) RunBatch(ctx context.Context, workerID, chainID string, limit int) (int, error) {
	if w.Verifier == nil || w.Queue == nil || w.Process == nil || workerID == "" || chainID == "" || limit < 1 || limit > 100 {
		return 0, errors.New("invalid proof worker configuration")
	}
	now := time.Now().UTC()
	if w.Clock != nil {
		now = w.Clock().UTC()
	}
	lease := w.Lease
	if lease <= 0 {
		lease = 3 * time.Minute
	}
	// Claim only work we can start immediately. Reserving a batch gives its
	// last job the same expiry as its first job, before verification even starts.
	started := time.Now()
	jobs, err := w.Queue.ClaimProofs(ctx, workerID, chainID, now, lease, 1)
	if err != nil {
		return 0, err
	}
	maxAttempts := w.Limit
	if maxAttempts < 1 {
		maxAttempts = 20
	}
	var failures []error
	for _, job := range jobs {
		// Leave part of the lease for recording the result or scheduling a retry.
		jobCtx, cancel := context.WithDeadline(ctx, started.Add(lease-lease/5))
		defer cancel()
		events, verifyErr := w.Verifier.LookupTransaction(jobCtx, job.Proof.ChainID, job.Proof.TransactionID)
		if verifyErr == nil && len(events) == 0 {
			status := domain.ProofQueued
			if job.Attempt >= maxAttempts {
				status = domain.ProofNotFound
			}
			next := w.currentTime().Add(proofRetryDelay(job.Attempt))
			if retryErr := w.Queue.RetryProof(ctx, job, next, "transaction contains no supported transfer events", status); retryErr != nil {
				failures = append(failures, fmt.Errorf("proof %s not-found acknowledgement: %w", job.Proof.ID, retryErr))
			}
			continue
		}
		var eventIDs []string
		if verifyErr == nil {
			for _, event := range events {
				if _, err := w.Process.Process(jobCtx, event); err != nil {
					verifyErr = err
					break
				}
				eventIDs = append(eventIDs, event.ID)
			}
		}
		if verifyErr == nil {
			if err := w.Queue.CompleteProof(jobCtx, job, eventIDs, w.currentTime()); err != nil {
				verifyErr = err
			}
		}
		if verifyErr == nil {
			continue
		}
		reason := verifyErr.Error()
		if len(reason) > 512 {
			reason = reason[:512]
		}
		// Provider, database and fencing errors are not evidence of an invalid
		// customer payment. Keep it retryable, with bounded load during outages.
		next := w.currentTime().Add(proofRetryDelay(job.Attempt))
		if retryErr := w.Queue.RetryProof(ctx, job, next, reason, domain.ProofQueued); retryErr != nil {
			failures = append(failures, fmt.Errorf("proof %s: %v; retry: %w", job.Proof.ID, verifyErr, retryErr))
			continue
		}
		failures = append(failures, fmt.Errorf("proof %s: %w", job.Proof.ID, verifyErr))
	}
	return len(jobs), errors.Join(failures...)
}

func (w ProofWorker) currentTime() time.Time {
	if w.Clock != nil {
		return w.Clock().UTC()
	}
	return time.Now().UTC()
}

func proofRetryDelay(attempt int) time.Duration {
	if attempt >= 18 {
		return 5 * time.Minute
	}
	if attempt < 1 {
		attempt = 1
	}
	return time.Duration(attempt*attempt) * time.Second
}
