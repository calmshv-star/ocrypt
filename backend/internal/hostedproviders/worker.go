package hostedproviders

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/domain"
)

var ErrRecoveryIntentIneligible = errors.New("hosted provider recovery intent is no longer eligible")

type RecoveryKind string

const (
	RecoveryCreate  RecoveryKind = "create"
	RecoveryStatus  RecoveryKind = "status"
	RecoveryPrebind RecoveryKind = "prebind"
)

type RecoveryJob struct {
	Kind          RecoveryKind
	ID            string
	ClaimToken    string
	Attempt       int
	Config        domain.HostedProviderConfig
	CreateRequest domain.HostedCreateRequest
	CreateResult  domain.HostedCreateResult
	RouteID       string
	IntentStatus  string
	Payment       domain.VerifiedProviderPayment
}

type RecoveryStore interface {
	ClaimHostedRecoveries(context.Context, string, time.Time, time.Duration, int) ([]RecoveryJob, error)
	AdmitHostedOperation(context.Context, RecoveryJob, string) error
	CompleteHostedCreateRecovery(context.Context, RecoveryJob, domain.HostedCreateResult) (RecoveryJob, error)
	BindHostedCreateRecovery(context.Context, RecoveryJob) error
	MarkHostedCreateExpired(context.Context, RecoveryJob) error
	MarkHostedCreateCancelled(context.Context, RecoveryJob, string) error
	RecordHostedReconcileObservation(context.Context, RecoveryJob, ProviderState) error
	ReplayHostedPrebind(context.Context, RecoveryJob) error
	ExpireHostedPrebind(context.Context, RecoveryJob) error
	RetryHostedRecovery(context.Context, RecoveryJob, time.Time, string, bool) error
}

// RecoveryWorker owns only provider lifecycle recovery. It never settles an
// intent from an unsigned status response: financial settlement remains behind
// the verified, append-only callback inbox boundary.
type RecoveryWorker struct {
	Store       RecoveryStore
	Adapter     Adapter
	Lease       time.Duration
	MaxAttempts int
	Clock       func() time.Time
}

func (w RecoveryWorker) RunBatch(ctx context.Context, workerID string, limit int) (int, error) {
	if w.Store == nil || w.Adapter == nil || workerID == "" || limit < 1 || limit > 100 {
		return 0, errors.New("invalid hosted provider recovery worker configuration")
	}
	now := time.Now().UTC()
	if w.Clock != nil {
		now = w.Clock().UTC()
	}
	lease := w.Lease
	if lease <= 0 {
		lease = 30 * time.Second
	}
	maximum := w.MaxAttempts
	if maximum <= 0 {
		maximum = 20
	}
	jobs, err := w.Store.ClaimHostedRecoveries(ctx, workerID, now, lease, limit)
	if err != nil {
		return 0, err
	}
	var failures []error
	for _, job := range jobs {
		err := w.process(ctx, job, now)
		if err == nil {
			continue
		}
		dead := job.Attempt >= maximum
		delay := time.Duration(job.Attempt*job.Attempt) * time.Second
		if delay > 15*time.Minute {
			delay = 15 * time.Minute
		}
		if retryErr := w.Store.RetryHostedRecovery(ctx, job, now.Add(delay), recoveryErrorCode(err), dead); retryErr != nil {
			failures = append(failures, fmt.Errorf("hosted recovery %s: %v; retry fence: %w", job.ID, err, retryErr))
			continue
		}
		failures = append(failures, fmt.Errorf("hosted recovery %s: %w", job.ID, err))
	}
	return len(jobs), errors.Join(failures...)
}

func (w RecoveryWorker) process(ctx context.Context, job RecoveryJob, now time.Time) error {
	switch job.Kind {
	case RecoveryCreate:
		if job.CreateResult.ProviderReference == "" {
			if !job.CreateRequest.ExpiresAt.After(now) {
				return w.Store.MarkHostedCreateExpired(ctx, job)
			}
			if err := w.Store.AdmitHostedOperation(ctx, job, "create"); err != nil {
				return err
			}
			result, err := w.Adapter.Create(ctx, job.Config, job.CreateRequest)
			if err != nil {
				return err
			}
			job, err = w.Store.CompleteHostedCreateRecovery(ctx, job, result)
			if err != nil {
				return err
			}
		}
		if err := w.Store.BindHostedCreateRecovery(ctx, job); err != nil {
			if !errors.Is(err, ErrRecoveryIntentIneligible) {
				return err
			}
			if err := w.Store.AdmitHostedOperation(ctx, job, "cancel"); err != nil {
				return err
			}
			cancelKey := "hosted-cancel-" + job.ID
			if err := w.Adapter.Cancel(ctx, job.Config, job.CreateResult.ProviderReference, cancelKey); err != nil {
				return err
			}
			return w.Store.MarkHostedCreateCancelled(ctx, job, "intent_ineligible")
		}
		return nil
	case RecoveryStatus:
		if err := w.Store.AdmitHostedOperation(ctx, job, "reconciliation"); err != nil {
			return err
		}
		state, err := w.Adapter.Reconcile(ctx, job.Config, job.CreateResult.ProviderReference)
		if err != nil {
			return err
		}
		return w.Store.RecordHostedReconcileObservation(ctx, job, state)
	case RecoveryPrebind:
		if job.RouteID == "" {
			return w.Store.ExpireHostedPrebind(ctx, job)
		}
		return w.Store.ReplayHostedPrebind(ctx, job)
	default:
		return errors.New("unknown hosted recovery job kind")
	}
}

func recoveryErrorCode(err error) string {
	switch {
	case errors.Is(err, domain.ErrDependency):
		return "provider_dependency"
	case errors.Is(err, domain.ErrIdempotencyConflict):
		return "idempotency_conflict"
	case errors.Is(err, domain.ErrStateConflict), errors.Is(err, ErrRecoveryIntentIneligible):
		return "state_conflict"
	default:
		return "recovery_failed"
	}
}
