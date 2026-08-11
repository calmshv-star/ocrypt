package retentionadmin

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/calmshv-star/ocrypt/backend/internal/ids"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresRepository struct{ pool *pgxpool.Pool }

func NewPostgresRepository(pool *pgxpool.Pool) (*PostgresRepository, error) {
	if pool == nil {
		return nil, ErrDependency
	}
	return &PostgresRepository{pool: pool}, nil
}

func (r *PostgresRepository) PingControl(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2_000_000_000)
	defer cancel()
	var ready bool
	err := r.pool.QueryRow(ctx, `SELECT to_regclass('retention_policy_change_requests') IS NOT NULL
AND to_regclass('retention_hold_release_requests') IS NOT NULL
AND has_function_privilege(current_user,'request_retention_policy_change(uuid,uuid,text,bigint,bigint,integer,integer,integer,boolean,timestamptz,text,uuid,text,timestamptz,text,bytea)','EXECUTE')
AND has_function_privilege(current_user,'decide_retention_policy_change(uuid,uuid,bigint,boolean,text,uuid,text,timestamptz,text,bytea)','EXECUTE')
AND has_function_privilege(current_user,'create_retention_control_hold(uuid,uuid,text,text,uuid,text,uuid,text,text,timestamptz,uuid,text,timestamptz,text,bytea)','EXECUTE')
AND has_function_privilege(current_user,'request_retention_hold_release(uuid,uuid,uuid,bigint,text,uuid,text,timestamptz,text,bytea)','EXECUTE')
AND has_function_privilege(current_user,'decide_retention_hold_release(uuid,uuid,bigint,boolean,text,uuid,text,timestamptz,text,bytea)','EXECUTE')
AND has_function_privilege(current_user,'retention_control_effective_policies(uuid)','EXECUTE')
AND has_function_privilege(current_user,'retention_control_batches(uuid,uuid,integer)','EXECUTE')`).Scan(&ready)
	if err != nil || !ready {
		if err == nil {
			err = ErrDependency
		}
		return fmt.Errorf("retention control PostgreSQL: %w", err)
	}
	return nil
}

func (r *PostgresRepository) PingScheduler(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2_000_000_000)
	defer cancel()
	var ready bool
	err := r.pool.QueryRow(ctx, `SELECT to_regclass('retention_control_worker_heartbeats') IS NOT NULL
AND has_function_privilege(current_user,'retention_control_advance_due(text,integer)','EXECUTE')
AND has_function_privilege(current_user,'retention_control_worker_health(integer)','EXECUTE')`).Scan(&ready)
	if err != nil || !ready {
		if err == nil {
			err = ErrDependency
		}
		return fmt.Errorf("retention control scheduler PostgreSQL: %w", err)
	}
	return nil
}

func (r *PostgresRepository) SchedulerHealth(ctx context.Context, staleAfterSeconds int) (SchedulerHealth, error) {
	if staleAfterSeconds < 10 || staleAfterSeconds > 3600 {
		return SchedulerHealth{}, ErrInvalid
	}
	var out SchedulerHealth
	err := r.pool.QueryRow(ctx, `SELECT last_cycle_at,due_policy_changes,due_hold_releases,due_hold_expiries,ready FROM retention_control_worker_health($1)`, staleAfterSeconds).
		Scan(&out.LastCycleAt, &out.DuePolicyChanges, &out.DueHoldReleases, &out.DueHoldExpiries, &out.Ready)
	return out, classify(err)
}

func (r *PostgresRepository) within(ctx context.Context, principal Principal, scope Scope, fn func(pgx.Tx) error) error {
	return pgx.BeginTxFunc(ctx, r.pool, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id',$1,true),set_config('app.platform_admin_global','false',true),set_config('app.platform_admin_tenants',$1,true),set_config('app.retention_actor_id',$2,true),set_config('app.retention_session_id',$3,true)`, scope.TenantID, principal.ActorID, principal.SessionID); err != nil {
			return err
		}
		return fn(tx)
	})
}

type rowScanner interface{ Scan(...any) error }

func scanPolicy(row rowScanner) (EffectivePolicy, error) {
	var out EffectivePolicy
	err := row.Scan(&out.ID, &out.TenantID, &out.DataClass, &out.Version, &out.ArchiveAfterDays, &out.PruneGraceDays,
		&out.ObjectLockDays, &out.PruneEnabled, &out.EffectiveAt, &out.PolicyDigest, &out.HeadFence, &out.LastActivatedAt)
	return out, classify(err)
}

const policyChangeColumns = `id::text,tenant_id::text,data_class,expected_policy_version,expected_head_fence,archive_after_days,prune_grace_days,object_lock_days,prune_enabled,status,reason,requested_by::text,COALESCE(approved_by::text,''),COALESCE(rejected_by::text,''),COALESCE(decision_reason,''),scheduled_for,expires_at,approved_at,decided_at,activated_at,created_at,updated_at,row_version`

func scanPolicyChange(row rowScanner) (PolicyChange, error) {
	var out PolicyChange
	err := row.Scan(&out.ID, &out.TenantID, &out.DataClass, &out.ExpectedEffectiveVersion, &out.ExpectedHeadFence,
		&out.Proposal.ArchiveAfterDays, &out.Proposal.PruneGraceDays, &out.Proposal.ObjectLockDays, &out.Proposal.PruneEnabled,
		&out.Status, &out.Reason, &out.RequestedBy, &out.ApprovedBy, &out.RejectedBy, &out.DecisionReason, &out.ScheduledFor,
		&out.ExpiresAt, &out.ApprovedAt, &out.DecidedAt, &out.ActivatedAt, &out.CreatedAt, &out.UpdatedAt, &out.RowVersion)
	return out, classify(err)
}

const holdColumns = `id::text,tenant_id::text,data_class,scope_type,COALESCE(merchant_id::text,''),COALESCE(source_table,''),COALESCE(source_record_id::text,''),COALESCE(case_reference,''),reason,actor_id,created_at,expires_at,released_at,COALESCE(released_by,''),expired_at,version`

func scanHold(row rowScanner) (LegalHold, error) {
	var out LegalHold
	err := row.Scan(&out.ID, &out.TenantID, &out.DataClass, &out.ScopeType, &out.MerchantID, &out.SourceTable,
		&out.SourceRecordID, &out.CaseReference, &out.Reason, &out.CreatedBy, &out.CreatedAt, &out.ExpiresAt,
		&out.ReleasedAt, &out.ReleasedBy, &out.ExpiredAt, &out.Version)
	return out, classify(err)
}

const releaseColumns = `id::text,tenant_id::text,hold_id::text,expected_hold_version,status,reason,requested_by::text,COALESCE(approved_by::text,''),COALESCE(rejected_by::text,''),COALESCE(decision_reason,''),created_at,expires_at,decided_at,row_version`

func scanRelease(row rowScanner) (HoldReleaseRequest, error) {
	var out HoldReleaseRequest
	err := row.Scan(&out.ID, &out.TenantID, &out.HoldID, &out.ExpectedVersion, &out.Status, &out.Reason,
		&out.RequestedBy, &out.ApprovedBy, &out.RejectedBy, &out.DecisionReason, &out.CreatedAt, &out.ExpiresAt, &out.DecidedAt, &out.RowVersion)
	return out, classify(err)
}

func (r *PostgresRepository) ListPolicies(ctx context.Context, principal Principal, scope Scope) ([]EffectivePolicy, error) {
	var out []EffectivePolicy
	err := r.within(ctx, principal, scope, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT * FROM retention_control_effective_policies($1)`, scope.TenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			value, scanErr := scanPolicy(rows)
			if scanErr != nil {
				return scanErr
			}
			out = append(out, value)
		}
		return rows.Err()
	})
	return out, classify(err)
}

func pageCursor(cursor string) any {
	if cursor == "" {
		return nil
	}
	return cursor
}

func (r *PostgresRepository) ListPolicyChanges(ctx context.Context, principal Principal, scope Scope, cursor string, limit int) (Page[PolicyChange], error) {
	var out Page[PolicyChange]
	err := r.within(ctx, principal, scope, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+policyChangeColumns+` FROM retention_control_policy_changes($1,$2,$3)`, scope.TenantID, pageCursor(cursor), limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			value, scanErr := scanPolicyChange(rows)
			if scanErr != nil {
				return scanErr
			}
			out.Items = append(out.Items, value)
		}
		return rows.Err()
	})
	trimPage(&out.Items, &out.NextCursor, limit, func(v PolicyChange) string { return v.ID })
	return out, classify(err)
}

func (r *PostgresRepository) ListHolds(ctx context.Context, principal Principal, scope Scope, cursor string, limit int) (Page[LegalHold], error) {
	var out Page[LegalHold]
	err := r.within(ctx, principal, scope, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+holdColumns+` FROM retention_control_holds($1,$2,$3)`, scope.TenantID, pageCursor(cursor), limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			value, scanErr := scanHold(rows)
			if scanErr != nil {
				return scanErr
			}
			out.Items = append(out.Items, value)
		}
		return rows.Err()
	})
	trimPage(&out.Items, &out.NextCursor, limit, func(v LegalHold) string { return v.ID })
	return out, classify(err)
}

func (r *PostgresRepository) ListReleaseRequests(ctx context.Context, principal Principal, scope Scope, cursor string, limit int) (Page[HoldReleaseRequest], error) {
	var out Page[HoldReleaseRequest]
	err := r.within(ctx, principal, scope, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+releaseColumns+` FROM retention_control_hold_releases($1,$2,$3)`, scope.TenantID, pageCursor(cursor), limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			value, scanErr := scanRelease(rows)
			if scanErr != nil {
				return scanErr
			}
			out.Items = append(out.Items, value)
		}
		return rows.Err()
	})
	trimPage(&out.Items, &out.NextCursor, limit, func(v HoldReleaseRequest) string { return v.ID })
	return out, classify(err)
}

func (r *PostgresRepository) ListBatches(ctx context.Context, principal Principal, scope Scope, cursor string, limit int) (Page[ArchiveBatchEvidence], error) {
	var out Page[ArchiveBatchEvidence]
	err := r.within(ctx, principal, scope, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text,data_class,policy_version,status,item_count,COALESCE(object_sha256,''),COALESCE(manifest_sha256,''),COALESCE(signing_key_id,''),object_retention_until,verified_at,pruned_at,created_at FROM retention_control_batches($1,$2,$3)`, scope.TenantID, pageCursor(cursor), limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var value ArchiveBatchEvidence
			if err = rows.Scan(&value.ID, &value.DataClass, &value.PolicyVersion, &value.Status, &value.ItemCount, &value.ObjectSHA256,
				&value.ManifestSHA256, &value.SigningKeyID, &value.ObjectRetentionUntil, &value.VerifiedAt, &value.PrunedAt, &value.CreatedAt); err != nil {
				return err
			}
			out.Items = append(out.Items, value)
		}
		return rows.Err()
	})
	trimPage(&out.Items, &out.NextCursor, limit, func(v ArchiveBatchEvidence) string { return v.ID })
	return out, classify(err)
}

func (r *PostgresRepository) ListTombstones(ctx context.Context, principal Principal, scope Scope, cursor string, limit int) (Page[TombstoneEvidence], error) {
	var out Page[TombstoneEvidence]
	err := r.within(ctx, principal, scope, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT data_class,source_table,source_record_id::text,merchant_id::text,original_sha256,batch_id::text,archived_at FROM retention_control_tombstones($1,$2,$3)`, scope.TenantID, pageCursor(cursor), limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var value TombstoneEvidence
			if err = rows.Scan(&value.DataClass, &value.SourceTable, &value.SourceRecordID, &value.MerchantID, &value.OriginalSHA256, &value.BatchID, &value.ArchivedAt); err != nil {
				return err
			}
			out.Items = append(out.Items, value)
		}
		return rows.Err()
	})
	trimPage(&out.Items, &out.NextCursor, limit, func(v TombstoneEvidence) string { return v.SourceRecordID })
	return out, classify(err)
}

func trimPage[T any](items *[]T, cursor *string, limit int, id func(T) string) {
	if len(*items) > limit {
		*cursor = id((*items)[limit-1])
		*items = (*items)[:limit]
	}
}

func (r *PostgresRepository) RequestPolicy(ctx context.Context, principal Principal, input RequestPolicyInput, idem Idempotency) (PolicyChange, error) {
	id, err := ids.New()
	if err != nil {
		return PolicyChange{}, err
	}
	var out PolicyChange
	err = r.within(ctx, principal, Scope{TenantID: input.TenantID}, func(tx pgx.Tx) error {
		var scanErr error
		out, scanErr = scanPolicyChange(tx.QueryRow(ctx, `SELECT `+policyChangeColumns+` FROM request_retention_policy_change($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
			id, input.TenantID, input.DataClass, input.ExpectedEffectiveVersion, input.ExpectedHeadFence, input.Proposal.ArchiveAfterDays,
			input.Proposal.PruneGraceDays, input.Proposal.ObjectLockDays, input.Proposal.PruneEnabled, input.ScheduledFor,
			input.Reason, principal.ActorID, principal.SessionID, principal.StepUpAt, idem.Key, idem.Fingerprint[:]))
		return scanErr
	})
	return out, classify(err)
}

func (r *PostgresRepository) DecidePolicy(ctx context.Context, principal Principal, scope Scope, id string, approve bool, input DecisionInput, idem Idempotency) (PolicyChange, error) {
	var out PolicyChange
	err := r.within(ctx, principal, scope, func(tx pgx.Tx) error {
		var scanErr error
		out, scanErr = scanPolicyChange(tx.QueryRow(ctx, `SELECT `+policyChangeColumns+` FROM decide_retention_policy_change($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
			id, scope.TenantID, input.ExpectedRowVersion, approve, input.Reason, principal.ActorID, principal.SessionID, principal.StepUpAt, idem.Key, idem.Fingerprint[:]))
		return scanErr
	})
	return out, classify(err)
}

func nullableUUID(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func (r *PostgresRepository) CreateHold(ctx context.Context, principal Principal, input CreateHoldInput, idem Idempotency) (LegalHold, error) {
	id, err := ids.New()
	if err != nil {
		return LegalHold{}, err
	}
	var out LegalHold
	err = r.within(ctx, principal, Scope{TenantID: input.TenantID}, func(tx pgx.Tx) error {
		var scanErr error
		out, scanErr = scanHold(tx.QueryRow(ctx, `SELECT `+holdColumns+` FROM create_retention_control_hold($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`,
			id, input.TenantID, input.DataClass, input.ScopeType, nullableUUID(input.MerchantID), nullableText(input.SourceTable),
			nullableUUID(input.SourceRecordID), input.CaseReference, input.Reason, input.ExpiresAt, principal.ActorID,
			principal.SessionID, principal.StepUpAt, idem.Key, idem.Fingerprint[:]))
		return scanErr
	})
	return out, classify(err)
}

func (r *PostgresRepository) RequestHoldRelease(ctx context.Context, principal Principal, input RequestReleaseInput, idem Idempotency) (HoldReleaseRequest, error) {
	id, err := ids.New()
	if err != nil {
		return HoldReleaseRequest{}, err
	}
	var out HoldReleaseRequest
	err = r.within(ctx, principal, Scope{TenantID: input.TenantID}, func(tx pgx.Tx) error {
		var scanErr error
		out, scanErr = scanRelease(tx.QueryRow(ctx, `SELECT `+releaseColumns+` FROM request_retention_hold_release($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
			id, input.TenantID, input.HoldID, input.ExpectedHoldVersion, input.Reason, principal.ActorID, principal.SessionID,
			principal.StepUpAt, idem.Key, idem.Fingerprint[:]))
		return scanErr
	})
	return out, classify(err)
}

func (r *PostgresRepository) DecideHoldRelease(ctx context.Context, principal Principal, scope Scope, id string, approve bool, input DecisionInput, idem Idempotency) (HoldReleaseRequest, error) {
	var out HoldReleaseRequest
	err := r.within(ctx, principal, scope, func(tx pgx.Tx) error {
		var scanErr error
		out, scanErr = scanRelease(tx.QueryRow(ctx, `SELECT `+releaseColumns+` FROM decide_retention_hold_release($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
			id, scope.TenantID, input.ExpectedRowVersion, approve, input.Reason, principal.ActorID, principal.SessionID,
			principal.StepUpAt, idem.Key, idem.Fingerprint[:]))
		return scanErr
	})
	return out, classify(err)
}

func (r *PostgresRepository) AdvanceDue(ctx context.Context, worker string, limit int) (int, error) {
	var processed int
	err := r.pool.QueryRow(ctx, `SELECT retention_control_advance_due($1,$2)`, worker, limit).Scan(&processed)
	return processed, classify(err)
}

func classify(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "P0002":
			return ErrNotFound
		case "RI022":
			return ErrIdempotencyConflict
		case "40001", "23505", "23503":
			return ErrConflict
		case "22023", "22001", "23514", "22P02":
			return ErrInvalid
		case "42501", "RA022":
			return ErrForbidden
		}
		if strings.Contains(pgErr.Message, "retention idempotency conflict") {
			return ErrIdempotencyConflict
		}
	}
	return err
}

var _ Repository = (*PostgresRepository)(nil)
