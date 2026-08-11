package postgres

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/application"
	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/ids"
	"github.com/calmshv-star/ocrypt/backend/internal/money"
	"github.com/jackc/pgx/v5"
)

func (s *Store) ClaimResolutions(ctx context.Context, workerID string, now time.Time, lease time.Duration, limit int) (jobs []application.ResolutionJob, err error) {
	if workerID == "" || lease < time.Second || limit < 1 || limit > 100 {
		return nil, errors.New("invalid manual resolution claim")
	}
	err = pgx.BeginTxFunc(ctx, s.db.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text FROM manual_resolutions WHERE status IN ('verification_requested','verification_retry') AND next_attempt_at<=$1 AND (locked_until IS NULL OR locked_until<$1) ORDER BY next_attempt_at,id LIMIT $2 FOR UPDATE SKIP LOCKED`, now, limit)
		if err != nil {
			return err
		}
		var selected []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			selected = append(selected, id)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		for _, id := range selected {
			claimToken, err := ids.New()
			if err != nil {
				return err
			}
			command, err := tx.Exec(ctx, `UPDATE manual_resolutions SET locked_by=$1,locked_until=$2,lease_token=$3,attempt_count=attempt_count+1,updated_at=$4,version=version+1 WHERE id=$5 AND status IN ('verification_requested','verification_retry')`, workerID, now.Add(lease), claimToken, now, id)
			if err != nil {
				return err
			}
			if command.RowsAffected() != 1 {
				return domain.ErrVersionConflict
			}
			job, err := loadResolutionJob(ctx, tx, id, claimToken)
			if err != nil {
				return err
			}
			jobs = append(jobs, job)
		}
		return nil
	})
	return jobs, err
}

func loadResolutionJob(ctx context.Context, tx pgx.Tx, resolutionID, claimToken string) (application.ResolutionJob, error) {
	var job application.ResolutionJob
	var status, amount, height, transferStatus string
	var evidence []byte
	err := tx.QueryRow(ctx, `SELECT mr.id::text,mr.unmatched_id::text,mr.event_id::text,mr.target_route_id::text,mr.candidate_set_version,mr.idempotency_key,mr.requested_by::text,COALESCE(mr.approved_by::text,''),mr.accept_shortfall,mr.accept_late_payment,mr.accept_cross_asset,mr.human_reason,mr.status::text,COALESCE(encode(mr.verifier_evidence_hash,'hex'),''),mr.created_at,mr.completed_at,mr.version,mr.attempt_count,
te.id::text,te.chain_id,te.transaction_id,te.event_identity,te.asset_id,te.to_address,te.event_kind,te.from_address,te.amount_atomic::text,te.asset_decimals,te.block_height::text,te.block_hash,te.on_chain_time,te.confirmations,te.status::text,te.parser_version,te.evidence_hash
FROM manual_resolutions mr JOIN transfer_events te ON te.id=mr.event_id WHERE mr.id=$1 AND mr.lease_token=$2 AND mr.locked_until>clock_timestamp()`, resolutionID, claimToken).Scan(
		&job.Resolution.ID, &job.Resolution.UnmatchedPaymentID, &job.Resolution.TransferEventID, &job.Resolution.TargetRouteID, &job.Resolution.CandidateSetVersion, &job.Resolution.IdempotencyKey, &job.Resolution.RequestedBy, &job.Resolution.ApprovedBy, &job.Resolution.AcceptShortfall, &job.Resolution.AcceptLatePayment, &job.Resolution.AcceptCrossAsset, &job.Resolution.Reason, &status, &job.Resolution.VerifierEvidenceHash, &job.Resolution.CreatedAt, &job.Resolution.CompletedAt, &job.Resolution.Version, &job.Resolution.Attempt,
		&job.Expected.ID, &job.Expected.Identity.ChainID, &job.Expected.Identity.TransactionID, &job.Expected.Identity.EventIndex, &job.Expected.Identity.AssetID, &job.Expected.Identity.ToAddress, &job.Expected.Kind, &job.Expected.FromAddress, &amount, &job.Expected.AssetDecimals, &height, &job.Expected.BlockHash, &job.Expected.OnChainTime, &job.Expected.Confirmations, &transferStatus, &job.Expected.ParserVersion, &evidence)
	if err != nil {
		return job, err
	}
	job.Resolution.Status = domain.UnmatchedStatus(status)
	job.Resolution.ClaimToken = claimToken
	job.Expected.Status = domain.TransferStatus(transferStatus)
	job.Expected.EvidenceHash = fmt.Sprintf("%x", evidence)
	job.Expected.Amount, err = money.Parse(amount)
	if err == nil {
		job.Expected.BlockHeight, err = strconv.ParseUint(height, 10, 64)
	}
	return job, err
}

func (s *Store) RetryResolution(ctx context.Context, resolution domain.ManualResolution, next time.Time, reason string, dead bool) error {
	status := domain.UnmatchedVerificationRetry
	if dead {
		status = domain.UnmatchedConflict
	}
	return pgx.BeginTxFunc(ctx, s.db.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		command, err := tx.Exec(ctx, `UPDATE manual_resolutions SET status=$1,next_attempt_at=$2,last_error=$3,locked_by=NULL,locked_until=NULL,lease_token=NULL,updated_at=clock_timestamp(),version=version+1 WHERE id=$4 AND lease_token=$5 AND locked_until>clock_timestamp()`, status, next, reason, resolution.ID, resolution.ClaimToken)
		if err != nil {
			return err
		}
		if command.RowsAffected() != 1 {
			return domain.ErrVersionConflict
		}
		_, err = tx.Exec(ctx, `UPDATE unmatched_payments SET status=$1,updated_at=clock_timestamp(),version=version+1 WHERE id=$2`, status, resolution.UnmatchedPaymentID)
		return err
	})
}
