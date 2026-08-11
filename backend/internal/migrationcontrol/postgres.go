package migrationcontrol

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

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
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	functions := []string{
		"create_migration_run(uuid,text,text,text,uuid,text,timestamptz,text,bytea)",
		"attach_migration_manifest(uuid,bigint,uuid,text,bytea,bytea,text[],uuid,text,timestamptz,text,text,bytea)",
		"request_migration_transition(uuid,uuid,text,bigint,bigint,uuid,text,uuid,text,timestamptz,text,bytea)",
		"decide_migration_transition(uuid,uuid,bigint,boolean,text,uuid,text,timestamptz,text,bytea)",
		"execute_migration_transition(uuid,uuid,bigint,bigint,bigint,text,uuid,text,timestamptz,text,bytea)",
	}
	checks := "to_regclass('public.migration_runs') IS NOT NULL"
	args := make([]any, len(functions))
	for i, function := range functions {
		checks += fmt.Sprintf(" AND has_function_privilege(current_user,$%d,'EXECUTE')", i+1)
		args[i] = function
	}
	var ready bool
	if err := r.pool.QueryRow(ctx, "SELECT "+checks, args...).Scan(&ready); err != nil || !ready {
		if err == nil {
			err = ErrDependency
		}
		return fmt.Errorf("migration control PostgreSQL: %w", err)
	}
	return nil
}

func (r *PostgresRepository) PingActuator(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var ready bool
	err := r.pool.QueryRow(ctx, `SELECT pg_has_role(current_user,'migration_traffic_actuator','member')
		AND has_function_privilege(current_user,'migration_pending_actuator_action(uuid)','EXECUTE')
		AND has_function_privilege(current_user,'acknowledge_migration_actuator(uuid,bigint,bigint,text,text,text,text)','EXECUTE')
		AND NOT has_table_privilege(current_user,'migration_runs','INSERT,UPDATE,DELETE')`).Scan(&ready)
	if err != nil || !ready {
		if err == nil {
			err = ErrDependency
		}
		return fmt.Errorf("migration actuator PostgreSQL: %w", err)
	}
	return nil
}

func (r *PostgresRepository) PendingActuatorAction(ctx context.Context, migrationID string) (DesiredAction, error) {
	var value DesiredAction
	err := r.pool.QueryRow(ctx, `SELECT migration_id::text,action_version,fence_token,action,target_state FROM migration_pending_actuator_action($1)`, migrationID).Scan(&value.MigrationID, &value.ActionVersion, &value.FenceToken, &value.Action, &value.TargetState)
	return value, classify(err)
}

func (r *PostgresRepository) PingWorker(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	functions := []string{
		"claim_migration_workload(uuid,text,integer)",
		"migration_record_payment_verification(uuid,uuid,text,uuid,bigint,text,bytea,bytea,text[],bigint)",
		"migration_post_verified_opening(uuid,text,uuid,bigint,text,uuid)",
	}
	var ready bool
	err := r.pool.QueryRow(ctx, `SELECT pg_has_role(current_user,'migration_control_worker','member')
		AND has_function_privilege(current_user,$1,'EXECUTE')
		AND has_function_privilege(current_user,$2,'EXECUTE')
		AND has_function_privilege(current_user,$3,'EXECUTE')
		AND NOT has_table_privilege(current_user,'migration_runs','INSERT,UPDATE,DELETE')`, functions[0], functions[1], functions[2]).Scan(&ready)
	if err != nil || !ready {
		if err == nil {
			err = ErrDependency
		}
		return fmt.Errorf("migration worker PostgreSQL: %w", err)
	}
	return nil
}

func (r *PostgresRepository) within(ctx context.Context, scope Scope, actor, session string, fn func(pgx.Tx) error) error {
	return pgx.BeginTxFunc(ctx, r.pool, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT set_config('app.platform_admin_global','false',true),set_config('app.platform_admin_tenants',$1,true),set_config('app.migration_actor_id',$2,true),set_config('app.migration_session_id',$3,true)`, scope.TenantID, actor, session); err != nil {
			return err
		}
		return fn(tx)
	})
}

const runColumns = `id::text,tenant_id::text,source_system_id,profile,state,create_traffic_owner,callback_owner,desired_action_version,actuator_ack_version,fence_token,row_version,rollback_deadline,COALESCE(pending_action,''),COALESCE(pending_target_state,''),created_at,updated_at`

type rowScanner interface{ Scan(...any) error }

func scanRun(row rowScanner) (Run, error) {
	var value Run
	err := row.Scan(&value.ID, &value.TenantID, &value.SourceSystemID, &value.Profile, &value.State, &value.CreateTrafficOwner, &value.CallbackOwner, &value.DesiredActionVersion, &value.ActuatorAckVersion, &value.FenceToken, &value.RowVersion, &value.RollbackDeadline, &value.PendingAction, &value.PendingTargetState, &value.CreatedAt, &value.UpdatedAt)
	return value, classify(err)
}

func (r *PostgresRepository) CreateRun(ctx context.Context, principal Principal, input CreateRunInput, idem Idempotency) (Run, error) {
	var result Run
	err := r.within(ctx, Scope{TenantID: input.TenantID}, principal.ActorID, principal.SessionID, func(tx pgx.Tx) error {
		var err error
		result, err = scanRun(tx.QueryRow(ctx, `SELECT `+runColumns+` FROM create_migration_run($1,$2,$3,$4,$5,$6,$7,$8,$9)`, input.TenantID, input.SourceSystemID, input.Profile, input.Reason, principal.ActorID, principal.SessionID, principal.StepUpAt, idem.Key, idem.Fingerprint[:]))
		return err
	})
	return result, classify(err)
}

func (r *PostgresRepository) GetRun(ctx context.Context, scope Scope, id string) (Run, error) {
	var result Run
	err := r.within(ctx, scope, "", "", func(tx pgx.Tx) error {
		var err error
		result, err = scanRun(tx.QueryRow(ctx, `SELECT `+runColumns+` FROM migration_runs WHERE id=$1 AND tenant_id=$2`, id, scope.TenantID))
		return err
	})
	return result, classify(err)
}

func (r *PostgresRepository) ListRuns(ctx context.Context, scope Scope, cursor string, limit int) ([]Run, string, error) {
	items := make([]Run, 0, limit)
	next := ""
	err := r.within(ctx, scope, "", "", func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+runColumns+` FROM migration_runs WHERE tenant_id=$1 AND ($2='' OR id>$2::uuid) ORDER BY id LIMIT $3`, scope.TenantID, cursor, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			value, scanErr := scanRun(rows)
			if scanErr != nil {
				return scanErr
			}
			items = append(items, value)
		}
		return rows.Err()
	})
	if len(items) > limit {
		next = items[limit-1].ID
		items = items[:limit]
	}
	return items, next, classify(err)
}

func (r *PostgresRepository) AttachManifest(ctx context.Context, principal Principal, migrationID string, manifest Manifest, canonical []byte, digest string, signers []string, input AttachManifestInput, idem Idempotency) (StoredManifest, error) {
	var result StoredManifest
	digestBytes, err := hex.DecodeString(digest)
	if err != nil {
		return StoredManifest{}, ErrInvalid
	}
	err = r.within(ctx, Scope{TenantID: manifest.TenantID}, principal.ActorID, principal.SessionID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT id::text,migration_id::text,kind,encode(payload_hash,'hex'),signer_key_ids,created_at FROM attach_migration_manifest($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, migrationID, input.ExpectedRowVersion, manifest.ManifestID, manifest.Kind, canonical, digestBytes, signers, principal.ActorID, principal.SessionID, principal.StepUpAt, input.Reason, idem.Key, idem.Fingerprint[:]).Scan(&result.ID, &result.MigrationID, &result.Kind, &result.PayloadHash, &result.SignerKeyIDs, &result.CreatedAt)
	})
	return result, classify(err)
}

const requestColumns = `id::text,migration_id::text,from_state,target_state,status,requested_by::text,COALESCE(approved_by::text,''),expected_row_version,expected_fence_token,created_at,expires_at,decided_at,version`

func scanRequest(row rowScanner) (TransitionRequest, error) {
	var value TransitionRequest
	err := row.Scan(&value.ID, &value.MigrationID, &value.FromState, &value.TargetState, &value.Status, &value.RequestedBy, &value.ApprovedBy, &value.ExpectedRowVersion, &value.ExpectedFenceToken, &value.CreatedAt, &value.ExpiresAt, &value.DecidedAt, &value.Version)
	return value, classify(err)
}

func (r *PostgresRepository) RequestTransition(ctx context.Context, principal Principal, scope Scope, migrationID string, input TransitionInput, idem Idempotency) (TransitionRequest, error) {
	var result TransitionRequest
	err := r.within(ctx, scope, principal.ActorID, principal.SessionID, func(tx pgx.Tx) error {
		var err error
		result, err = scanRequest(tx.QueryRow(ctx, `SELECT `+requestColumns+` FROM request_migration_transition($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, migrationID, scope.TenantID, input.TargetState, input.ExpectedRowVersion, input.ExpectedFenceToken, input.ManifestID, input.Reason, principal.ActorID, principal.SessionID, principal.StepUpAt, idem.Key, idem.Fingerprint[:]))
		return err
	})
	return result, classify(err)
}

func (r *PostgresRepository) DecideTransition(ctx context.Context, principal Principal, scope Scope, requestID string, approve bool, input DecisionInput, idem Idempotency) (TransitionRequest, error) {
	var result TransitionRequest
	err := r.within(ctx, scope, principal.ActorID, principal.SessionID, func(tx pgx.Tx) error {
		var err error
		result, err = scanRequest(tx.QueryRow(ctx, `SELECT `+requestColumns+` FROM decide_migration_transition($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, requestID, scope.TenantID, input.ExpectedRequestVersion, approve, input.Reason, principal.ActorID, principal.SessionID, principal.StepUpAt, idem.Key, idem.Fingerprint[:]))
		return err
	})
	return result, classify(err)
}

func (r *PostgresRepository) ExecuteTransition(ctx context.Context, principal Principal, scope Scope, requestID string, input ExecuteInput, idem Idempotency) (Run, error) {
	var result Run
	err := r.within(ctx, scope, principal.ActorID, principal.SessionID, func(tx pgx.Tx) error {
		var err error
		result, err = scanRun(tx.QueryRow(ctx, `SELECT `+runColumns+` FROM execute_migration_transition($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, requestID, scope.TenantID, input.ExpectedRequestVersion, input.ExpectedRowVersion, input.ExpectedFenceToken, input.Reason, principal.ActorID, principal.SessionID, principal.StepUpAt, idem.Key, idem.Fingerprint[:]))
		return err
	})
	return result, classify(err)
}

func (r *PostgresRepository) AcknowledgeActuator(ctx context.Context, migrationID string, input ActuatorAckInput) (Run, error) {
	return scanRun(r.pool.QueryRow(ctx, `SELECT `+runColumns+` FROM acknowledge_migration_actuator($1,$2,$3,$4,$5,$6,$7)`, migrationID, input.ActionVersion, input.FenceToken, input.Action, input.EvidenceHash, input.KeyID, input.Signature))
}

func (r *PostgresRepository) ClaimWorkload(ctx context.Context, migrationID, workerID string, leaseSeconds int) (WorkloadLease, error) {
	var lease WorkloadLease
	err := r.pool.QueryRow(ctx, `SELECT worker_id,lease_token::text,fence_token,lease_until FROM claim_migration_workload($1,$2,$3)`, migrationID, workerID, leaseSeconds).Scan(&lease.WorkerID, &lease.LeaseToken, &lease.FenceToken, &lease.LeaseUntil)
	return lease, classify(err)
}

func (r *PostgresRepository) RecordShadowComparison(ctx context.Context, migrationID string, lease WorkloadLease, input ShadowComparisonInput) error {
	var accepted bool
	err := r.pool.QueryRow(ctx, `SELECT record_migration_shadow_comparison($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12::jsonb)`, migrationID, lease.WorkerID, lease.LeaseToken, lease.FenceToken, input.SourceSequence, input.EntityType, input.SourceID, input.SourceDigest, input.PlatformDigest, input.Classification, input.ExplanationRef, input.Observation).Scan(&accepted)
	if err == nil && !accepted {
		err = ErrConflict
	}
	return classify(err)
}

func (r *PostgresRepository) StageImportItem(ctx context.Context, migrationID string, lease WorkloadLease, input ImportItem) error {
	var accepted bool
	err := r.pool.QueryRow(ctx, `SELECT stage_migration_import_item($1,$2,$3,$4,$5,$6,$7,$8::jsonb)`, migrationID, lease.WorkerID, lease.LeaseToken, lease.FenceToken, input.SourceSequence, input.EntityType, input.SourceID, input.Payload).Scan(&accepted)
	if err == nil && !accepted {
		err = ErrConflict
	}
	return classify(err)
}

func (r *PostgresRepository) RecordVerification(ctx context.Context, migrationID string, lease WorkloadLease, sourceID, evidenceID string, verified VerifiedFact) error {
	var accepted bool
	err := r.pool.QueryRow(ctx, `SELECT migration_record_payment_verification($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, evidenceID, migrationID, lease.WorkerID, lease.LeaseToken, lease.FenceToken, sourceID, verified.CanonicalBody, verified.Digest[:], verified.VerifierKeyIDs, verified.VerifierVersion).Scan(&accepted)
	if err == nil && !accepted {
		err = ErrConflict
	}
	return classify(err)
}

func (r *PostgresRepository) PostVerifiedOpening(ctx context.Context, migrationID string, lease WorkloadLease, sourceID, ledgerID string) error {
	var accepted bool
	err := r.pool.QueryRow(ctx, `SELECT migration_post_verified_opening($1,$2,$3,$4,$5,$6)`, migrationID, lease.WorkerID, lease.LeaseToken, lease.FenceToken, sourceID, ledgerID).Scan(&accepted)
	if err == nil && !accepted {
		err = ErrConflict
	}
	return classify(err)
}

func classify(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrInvalid) || errors.Is(err, ErrConflict) || errors.Is(err, ErrForbidden) || errors.Is(err, ErrIdempotencyConflict) || errors.Is(err, ErrNotFound) {
		return err
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return ErrConflict
		case "40001", "40P01":
			return ErrConflict
		case "MP001":
			return ErrInvalid
		case "MP002":
			return ErrConflict
		case "MP003":
			return ErrForbidden
		case "MP004":
			return ErrIdempotencyConflict
		}
	}
	return fmt.Errorf("%w: %v", ErrDependency, err)
}
