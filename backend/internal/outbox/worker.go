package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type Message struct {
	EventID          string          `json:"event_id"`
	TenantID         string          `json:"tenant_id"`
	MerchantID       string          `json:"merchant_id"`
	AggregateType    string          `json:"aggregate_type"`
	AggregateID      string          `json:"aggregate_id"`
	AggregateVersion int64           `json:"aggregate_version"`
	Sequence         int64           `json:"sequence"`
	EventType        string          `json:"event_type"`
	SchemaVersion    string          `json:"schema_version"`
	Payload          json.RawMessage `json:"payload"`
	CorrelationID    string          `json:"correlation_id,omitempty"`
	CausationID      string          `json:"causation_id,omitempty"`
	OccurredAt       time.Time       `json:"occurred_at"`
	RecordedAt       time.Time       `json:"recorded_at"`
}

type Job struct {
	EventID, ClaimToken string
	Attempt             int
	Message             Message
}

type Store interface {
	Claim(context.Context, string, time.Time, time.Duration, int) ([]Job, error)
	MarkPublished(context.Context, Job, time.Time) error
	Retry(context.Context, Job, time.Time, string) error
}

// Publisher must provide at-least-once delivery. Message.EventID is the stable
// downstream deduplication key and remains unchanged when a lease is retried.
type Publisher interface {
	Publish(context.Context, Message) error
}

type Worker struct {
	Store     Store
	Publisher Publisher
	Lease     time.Duration
	// MaxRetryDelay bounds the retry interval. JetStream's duplicate window
	// must be at least this long; durable consumer inboxes remain the ultimate
	// duplicate-business-effect boundary after that window.
	MaxRetryDelay time.Duration
	Clock         func() time.Time
}

func (w Worker) RunBatch(ctx context.Context, workerID string, limit int) (int, error) {
	if w.Store == nil || w.Publisher == nil || workerID == "" || limit < 1 || limit > 500 {
		return 0, errors.New("invalid outbox worker configuration")
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
	var failures []error
	for _, job := range jobs {
		if job.Message.EventID == "" {
			job.Message.EventID = job.EventID
		}
		err := w.Publisher.Publish(ctx, job.Message)
		if err == nil {
			err = w.Store.MarkPublished(ctx, job, now)
		}
		if err != nil {
			reason := err.Error()
			if len(reason) > 512 {
				reason = reason[:512]
			}
			delay := time.Duration(job.Attempt*job.Attempt) * time.Second
			maximum := w.MaxRetryDelay
			if maximum <= 0 {
				maximum = 15 * time.Minute
			}
			if delay > maximum {
				delay = maximum
			}
			if retryErr := w.Store.Retry(ctx, job, now.Add(delay), reason); retryErr != nil {
				failures = append(failures, fmt.Errorf("publish %s: %v; retry: %w", job.EventID, err, retryErr))
				continue
			}
			failures = append(failures, fmt.Errorf("publish %s: %w", job.EventID, err))
		}
	}
	return len(jobs), errors.Join(failures...)
}
