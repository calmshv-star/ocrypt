package platformadmin

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/ids"
)

type Scheduler struct {
	repository ActivationSchedulerRepository
	workerID   string
	principal  Principal
	lease      time.Duration
}

func NewScheduler(repository ActivationSchedulerRepository, workerID string, actorID string) (*Scheduler, error) {
	if repository == nil || len(strings.TrimSpace(workerID)) < 3 || !ids.Valid(actorID) {
		return nil, ErrInvalid
	}
	return &Scheduler{repository: repository, workerID: workerID, principal: Principal{ActorID: actorID, SessionID: "scheduled-activation:" + workerID, Audience: "platform-admin-scheduler"}, lease: time.Minute}, nil
}
func (s *Scheduler) RunOnce(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > 100 {
		return 0, ErrInvalid
	}
	jobs, err := s.repository.ClaimDueActivations(ctx, s.principal.ActorID, s.workerID, limit, s.lease)
	if err != nil {
		return 0, err
	}
	completed := 0
	for _, job := range jobs {
		idem, err := Fingerprint(job)
		if err != nil {
			return completed, err
		}
		idem.Key = "scheduled-" + job.ChangeID
		_, err = s.repository.Activate(ctx, s.principal, Scope{TenantID: job.TenantID}, job.ChangeID, ActivateInput{ExpectedRowVersion: job.RowVersion, ExpectedFenceToken: job.ExpectedFenceToken, Reason: "scheduled activation", LeaseOwner: s.workerID, LeaseToken: job.ClaimToken}, idem)
		if err == nil {
			completed++
			continue
		}
		if !errors.Is(err, ErrConflict) && !errors.Is(err, ErrScheduledForFuture) {
			return completed, err
		}
	}
	return completed, nil
}

type OutboxPublisher struct {
	store       OutboxStore
	destination OutboxDestination
	workerID    string
	lease       time.Duration
	now         func() time.Time
}

func NewOutboxPublisher(store OutboxStore, destination OutboxDestination, workerID string) (*OutboxPublisher, error) {
	if store == nil || destination == nil || !ids.Valid(strings.TrimSpace(workerID)) {
		return nil, ErrInvalid
	}
	return &OutboxPublisher{store: store, destination: destination, workerID: workerID, lease: time.Minute, now: func() time.Time { return time.Now().UTC() }}, nil
}
func (p *OutboxPublisher) RunOnce(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > 200 {
		return 0, ErrInvalid
	}
	events, err := p.store.ClaimPlatformOutbox(ctx, p.workerID, limit, p.lease)
	if err != nil {
		return 0, err
	}
	published := 0
	for _, event := range events {
		if err = p.destination.Publish(ctx, event); err != nil {
			delay := time.Duration(1<<min(event.Attempts, 10)) * time.Second
			if releaseErr := p.store.ReleasePlatformOutbox(ctx, event, p.workerID, p.now().Add(delay)); releaseErr != nil {
				return published, releaseErr
			}
			continue
		}
		if err = p.store.MarkPlatformOutboxPublished(ctx, event, p.workerID, p.now()); err != nil {
			return published, err
		}
		published++
	}
	return published, nil
}
