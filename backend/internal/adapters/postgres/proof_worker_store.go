package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/application"
	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/ids"
	"github.com/jackc/pgx/v5"
)

func (s *Store) ClaimProofs(ctx context.Context, workerID, chainID string, now time.Time, lease time.Duration, limit int) (jobs []application.ProofJob, err error) {
	err = pgx.BeginTxFunc(ctx, s.db.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text,tenant_id::text,merchant_id::text,intent_id::text,chain_id,transaction_id,status,transfer_event_ids,created_at,updated_at,version,attempt_count+1
FROM payment_proofs WHERE chain_id=$1 AND status IN ('queued','verifying') AND next_attempt_at<=$2 AND (locked_until IS NULL OR locked_until<$2)
ORDER BY next_attempt_at,id LIMIT $3 FOR UPDATE SKIP LOCKED`, chainID, now, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var job application.ProofJob
			var status string
			if err := rows.Scan(&job.Proof.ID, &job.Proof.TenantID, &job.Proof.MerchantID, &job.Proof.PaymentIntentID, &job.Proof.ChainID, &job.Proof.TransactionID, &status, &job.Proof.TransferEventIDs, &job.Proof.CreatedAt, &job.Proof.UpdatedAt, &job.Proof.Version, &job.Attempt); err != nil {
				return err
			}
			job.Proof.Status = domain.ProofStatus(status)
			jobs = append(jobs, job)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		for index := range jobs {
			token, err := ids.New()
			if err != nil {
				return err
			}
			jobs[index].ClaimToken = token
			command, err := tx.Exec(ctx, `UPDATE payment_proofs SET status='verifying',locked_by=$1,locked_until=$2,lease_token=$3,attempt_count=attempt_count+1,updated_at=$4,version=version+1 WHERE id=$5 AND (locked_until IS NULL OR locked_until<$4) AND status IN ('queued','verifying')`, workerID, now.Add(lease), token, now, jobs[index].Proof.ID)
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

func (s *Store) CompleteProof(ctx context.Context, job application.ProofJob, eventIDs []string, now time.Time) error {
	if !ids.Valid(job.Proof.ID) || !ids.Valid(job.ClaimToken) || len(eventIDs) == 0 {
		return errors.New("invalid proof completion")
	}
	for _, id := range eventIDs {
		if !ids.Valid(id) {
			return errors.New("invalid proof transfer event identity")
		}
	}
	// A transaction can contain transfers for several recipients. The hint may
	// trigger ingestion of every independently verified event, but this proof
	// only exposes event IDs that match a route belonging to its own intent.
	command, err := s.db.pool.Exec(ctx, `WITH relevant AS (
    SELECT COALESCE(array_agg(DISTINCT te.id),'{}'::uuid[]) AS ids
      FROM payment_proofs proof
      JOIN transfer_events te ON te.id=ANY($3::uuid[]) AND te.chain_id=proof.chain_id AND te.transaction_id=proof.transaction_id
      JOIN payment_routes route ON route.intent_id=proof.intent_id AND route.tenant_id=proof.tenant_id AND route.merchant_id=proof.merchant_id
                               AND route.chain_id=te.chain_id AND route.asset_id=te.asset_id AND route.receiving_address=te.to_address
     WHERE proof.id=$1 AND proof.lease_token=$2 AND proof.locked_until>clock_timestamp() AND proof.status='verifying'
)
UPDATE payment_proofs proof
   SET status=CASE WHEN cardinality(relevant.ids)>0 THEN 'linked' ELSE 'not_found' END,
       transfer_event_ids=relevant.ids,locked_by=NULL,locked_until=NULL,lease_token=NULL,last_error=NULL,updated_at=$4,version=version+1
  FROM relevant WHERE proof.id=$1 AND proof.lease_token=$2 AND proof.locked_until>clock_timestamp() AND proof.status='verifying'`, job.Proof.ID, job.ClaimToken, eventIDs, now)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return domain.ErrVersionConflict
	}
	return nil
}

func (s *Store) RetryProof(ctx context.Context, job application.ProofJob, next time.Time, reason string, status domain.ProofStatus) error {
	if status != domain.ProofQueued && status != domain.ProofInvalid && status != domain.ProofNotFound {
		return errors.New("invalid proof retry status")
	}
	command, err := s.db.pool.Exec(ctx, `UPDATE payment_proofs SET status=$3,next_attempt_at=$4,locked_by=NULL,locked_until=NULL,lease_token=NULL,last_error=$5,updated_at=clock_timestamp(),version=version+1 WHERE id=$1 AND lease_token=$2 AND status='verifying'`, job.Proof.ID, job.ClaimToken, status, next, reason)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return domain.ErrVersionConflict
	}
	return nil
}

var _ application.ProofQueueStore = (*Store)(nil)
