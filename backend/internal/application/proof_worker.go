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
		lease = 30 * time.Second
	}
	jobs, err := w.Queue.ClaimProofs(ctx, workerID, chainID, now, lease, limit)
	if err != nil {
		return 0, err
	}
	maxAttempts := w.Limit
	if maxAttempts < 1 {
		maxAttempts = 20
	}
	var failures []error
	for _, job := range jobs {
		events, verifyErr := w.Verifier.LookupTransaction(ctx, job.Proof.ChainID, job.Proof.TransactionID)
		if verifyErr == nil && len(events) == 0 {
			status := domain.ProofQueued
			if job.Attempt >= maxAttempts {
				status = domain.ProofNotFound
			}
			next := now.Add(time.Duration(job.Attempt*job.Attempt) * time.Second)
			if retryErr := w.Queue.RetryProof(ctx, job, next, "transaction contains no supported transfer events", status); retryErr != nil {
				failures = append(failures, fmt.Errorf("proof %s not-found acknowledgement: %w", job.Proof.ID, retryErr))
			}
			continue
		}
		var eventIDs []string
		if verifyErr == nil {
			for _, event := range events {
				if _, err := w.Process.Process(ctx, event); err != nil {
					verifyErr = err
					break
				}
				eventIDs = append(eventIDs, event.ID)
			}
		}
		if verifyErr == nil {
			if err := w.Queue.CompleteProof(ctx, job, eventIDs, now); err != nil {
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
		terminal := domain.ProofQueued
		if job.Attempt >= maxAttempts {
			terminal = domain.ProofInvalid
		}
		next := now.Add(time.Duration(job.Attempt*job.Attempt) * time.Second)
		if retryErr := w.Queue.RetryProof(ctx, job, next, reason, terminal); retryErr != nil {
			failures = append(failures, fmt.Errorf("proof %s: %v; retry: %w", job.Proof.ID, verifyErr, retryErr))
			continue
		}
		failures = append(failures, fmt.Errorf("proof %s: %w", job.Proof.ID, verifyErr))
	}
	return len(jobs), errors.Join(failures...)
}
