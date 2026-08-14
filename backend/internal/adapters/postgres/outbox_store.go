package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/ids"
	"github.com/calmshv-star/ocrypt/backend/internal/outbox"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type OutboxStore struct{ pool *pgxpool.Pool }

func NewOutboxStore(pool *pgxpool.Pool) (*OutboxStore, error) {
	if pool == nil {
		return nil, errors.New("outbox store requires PostgreSQL")
	}
	return &OutboxStore{pool: pool}, nil
}

func (s *OutboxStore) Claim(ctx context.Context, workerID string, now time.Time, lease time.Duration, limit int) (jobs []outbox.Job, err error) {
	if workerID == "" || lease < time.Second || limit < 1 || limit > 500 {
		return nil, errors.New("invalid outbox claim")
	}
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text,attempt_count+1,tenant_id::text,merchant_id::text,aggregate_type,aggregate_id::text,aggregate_version,aggregate_sequence,event_type,schema_version,payload,COALESCE(correlation_id,''),COALESCE(causation_id::text,''),occurred_at,recorded_at
FROM outbox_events WHERE published_at IS NULL AND available_at<=$1 AND (locked_until IS NULL OR locked_until<$1)
ORDER BY available_at,id LIMIT $2 FOR UPDATE SKIP LOCKED`, now, limit)
		if err != nil {
			return err
		}
		for rows.Next() {
			var job outbox.Job
			if err := rows.Scan(&job.EventID, &job.Attempt, &job.Message.TenantID, &job.Message.MerchantID, &job.Message.AggregateType, &job.Message.AggregateID, &job.Message.AggregateVersion, &job.Message.Sequence, &job.Message.EventType, &job.Message.SchemaVersion, &job.Message.Payload, &job.Message.CorrelationID, &job.Message.CausationID, &job.Message.OccurredAt, &job.Message.RecordedAt); err != nil {
				rows.Close()
				return err
			}
			job.Message.EventID = job.EventID
			jobs = append(jobs, job)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		for index := range jobs {
			claimToken, err := ids.New()
			if err != nil {
				return err
			}
			jobs[index].ClaimToken = claimToken
			command, err := tx.Exec(ctx, `UPDATE outbox_events SET locked_by=$1,locked_until=$2,lease_token=$3,attempt_count=attempt_count+1 WHERE id=$4 AND published_at IS NULL AND (locked_until IS NULL OR locked_until<$5)`, workerID, now.Add(lease), claimToken, jobs[index].EventID, now)
			if err != nil {
				return err
			}
			if command.RowsAffected() != 1 {
				return domain.ErrVersionConflict
			}
		}
		return nil
	})
	return jobs, err
}

func (s *OutboxStore) MarkPublished(ctx context.Context, job outbox.Job, now time.Time) error {
	if !ids.Valid(job.EventID) || !ids.Valid(job.ClaimToken) {
		return errors.New("invalid outbox job identity")
	}
	return pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx pgx.Tx) error {
		command, err := tx.Exec(ctx, `INSERT INTO event_history (event_id,tenant_id,merchant_id,aggregate_type,aggregate_id,aggregate_version,aggregate_sequence,event_type,schema_version,payload,correlation_id,causation_id,occurred_at,recorded_at,published_at)
SELECT id,tenant_id,merchant_id,aggregate_type,aggregate_id,aggregate_version,aggregate_sequence,event_type,schema_version,payload,correlation_id,causation_id,occurred_at,recorded_at,$3
FROM outbox_events WHERE id=$1 AND published_at IS NULL AND lease_token=$2 AND locked_until>clock_timestamp()`, job.EventID, job.ClaimToken, now)
		if err != nil {
			return err
		}
		if command.RowsAffected() != 1 {
			return fmt.Errorf("%w: outbox lease lost or history already advanced", domain.ErrVersionConflict)
		}
		command, err = tx.Exec(ctx, `UPDATE outbox_events SET published_at=$3,locked_by=NULL,locked_until=NULL,lease_token=NULL,last_error=NULL WHERE id=$1 AND published_at IS NULL AND lease_token=$2 AND locked_until>clock_timestamp()`, job.EventID, job.ClaimToken, now)
		if err != nil {
			return err
		}
		if command.RowsAffected() != 1 {
			return domain.ErrVersionConflict
		}
		return nil
	})
}

func (s *OutboxStore) Retry(ctx context.Context, job outbox.Job, next time.Time, reason string) error {
	command, err := s.pool.Exec(ctx, `UPDATE outbox_events SET available_at=$3,locked_by=NULL,locked_until=NULL,lease_token=NULL,last_error=$4 WHERE id=$1 AND published_at IS NULL AND lease_token=$2`, job.EventID, job.ClaimToken, next, reason)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return domain.ErrVersionConflict
	}
	return nil
}

var _ outbox.Store = (*OutboxStore)(nil)
