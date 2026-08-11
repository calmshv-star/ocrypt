package retention

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrStaleFence = errors.New("retention lease or fence is stale")

type PostgresRepository struct{ pool *pgxpool.Pool }

func NewPostgresRepository(pool *pgxpool.Pool) (*PostgresRepository, error) {
	if pool == nil {
		return nil, errors.New("retention repository requires PostgreSQL")
	}
	return &PostgresRepository{pool: pool}, nil
}

func (r *PostgresRepository) ClaimArchive(ctx context.Context, worker string, now time.Time, lease time.Duration, limit int) (batch Batch, found bool, err error) {
	err = pgx.BeginTxFunc(ctx, r.pool, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx pgx.Tx) error {
		rows, queryErr := tx.Query(ctx, `SELECT batch_id::text,tenant_id::text,data_class,policy_version,cutoff_at,created_at,
object_retention_until,lease_token::text,fence,ordinal,merchant_id::text,source_table,source_record_id::text,
recorded_at,original_digest,canonical_data
FROM retention_claim_archive_batch($1,$2,$3,$4)`, worker, now.UTC(), int(lease/time.Second), limit)
		if queryErr != nil {
			return queryErr
		}
		defer rows.Close()
		for rows.Next() {
			var item Record
			var class string
			var digest []byte
			var ordinal int
			if queryErr = rows.Scan(&batch.ID, &batch.TenantID, &class, &batch.PolicyVersion, &batch.Cutoff, &batch.CreatedAt,
				&batch.ObjectRetentionUntil, &batch.LeaseToken, &batch.Fence, &ordinal, &item.MerchantID, &item.SourceTable,
				&item.RecordID, &item.RecordedAt, &digest, &item.CanonicalData); queryErr != nil {
				return queryErr
			}
			batch.DataClass = DataClass(class)
			item.TenantID = batch.TenantID
			if len(digest) != sha256.Size {
				return errors.New("retention source returned an invalid digest")
			}
			copy(item.OriginalSHA[:], digest)
			batch.Records = append(batch.Records, item)
		}
		if queryErr = rows.Err(); queryErr != nil {
			return queryErr
		}
		found = len(batch.Records) > 0
		if found {
			return batch.validate()
		}
		return nil
	})
	return batch, found, err
}

func (r *PostgresRepository) AcknowledgeArchive(ctx context.Context, batch Batch, object ObjectEvidence, manifest ManifestEvidence, now time.Time) error {
	return pgx.BeginTxFunc(ctx, r.pool, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx pgx.Tx) error {
		var acknowledged bool
		err := tx.QueryRow(ctx, `SELECT retention_acknowledge_archive($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
			batch.ID, batch.LeaseToken, batch.Fence, object.Key, object.VersionID, object.ByteLength, object.SHA256[:],
			manifest.ManifestSHA256[:], manifest.SigningKeyID, manifest.Signature, object.ObjectLockMode,
			object.RetentionUntil.UTC(), object.AttestedAt.UTC(), now.UTC()).Scan(&acknowledged)
		if err != nil {
			return err
		}
		if !acknowledged {
			return ErrStaleFence
		}
		return nil
	})
}

func (r *PostgresRepository) FailArchive(ctx context.Context, batch Batch, reason string, now time.Time) error {
	var failed bool
	err := r.pool.QueryRow(ctx, `SELECT retention_fail_archive($1,$2,$3,$4,$5)`, batch.ID, batch.LeaseToken, batch.Fence, reason, now.UTC()).Scan(&failed)
	if err != nil {
		return err
	}
	if !failed {
		return ErrStaleFence
	}
	return nil
}

func (r *PostgresRepository) ClaimPrune(ctx context.Context, worker string, now time.Time, lease time.Duration) (claim PruneClaim, found bool, err error) {
	err = pgx.BeginTxFunc(ctx, r.pool, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx pgx.Tx) error {
		var class string
		var notBefore *time.Time
		row := tx.QueryRow(ctx, `SELECT batch_id::text,tenant_id::text,data_class,lease_token::text,fence,not_before,first_check
FROM retention_claim_prune($1,$2,$3)`, worker, now.UTC(), int(lease/time.Second))
		if scanErr := row.Scan(&claim.BatchID, &claim.TenantID, &class, &claim.LeaseToken, &claim.Fence, &notBefore, &claim.FirstCheck); scanErr != nil {
			if errors.Is(scanErr, pgx.ErrNoRows) {
				return nil
			}
			return scanErr
		}
		claim.DataClass = DataClass(class)
		if notBefore != nil {
			claim.NotBefore = notBefore.UTC()
		}
		found = true
		return nil
	})
	return claim, found, err
}

func (r *PostgresRepository) AdvancePrune(ctx context.Context, claim PruneClaim, now time.Time) (outcome PruneOutcome, err error) {
	err = pgx.BeginTxFunc(ctx, r.pool, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx pgx.Tx) error {
		var value string
		if queryErr := tx.QueryRow(ctx, `SELECT retention_advance_prune($1,$2,$3,$4)`, claim.BatchID, claim.LeaseToken, claim.Fence, now.UTC()).Scan(&value); queryErr != nil {
			return queryErr
		}
		outcome = PruneOutcome(value)
		return nil
	})
	return outcome, err
}

func (r *PostgresRepository) Health(ctx context.Context, now time.Time, stale time.Duration) (health Health, err error) {
	var last *time.Time
	err = r.pool.QueryRow(ctx, `SELECT last_cycle_at,pending_batches,stale_leases FROM retention_worker_health($1,$2)`,
		now.UTC(), int(stale/time.Second)).Scan(&last, &health.PendingBatches, &health.StaleLeases)
	if err != nil {
		return Health{}, fmt.Errorf("retention database readiness: %w", err)
	}
	health.LastCycleAt = last
	health.Ready = health.StaleLeases == 0
	return health, nil
}

var _ Repository = (*PostgresRepository)(nil)
