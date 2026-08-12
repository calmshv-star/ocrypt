package platformadmin

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/ids"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresRepository struct {
	pool *pgxpool.Pool
	now  func() time.Time
}

func NewPostgresRepository(pool *pgxpool.Pool) (*PostgresRepository, error) {
	if pool == nil {
		return nil, ErrDependency
	}
	return &PostgresRepository{pool: pool, now: func() time.Time { return time.Now().UTC() }}, nil
}
func (r *PostgresRepository) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := r.pool.Ping(ctx); err != nil {
		return fmt.Errorf("platform admin PostgreSQL: %w", err)
	}
	return nil
}

func (r *PostgresRepository) ScannerWatchAddresses(ctx context.Context, chainID string, observedAt time.Time) ([]string, error) {
	rows, err := r.pool.Query(ctx, `SELECT address FROM scanner_active_watch_addresses($1,$2)`, chainID, observedAt)
	if err != nil {
		return nil, classify(err)
	}
	defer rows.Close()
	addresses := make([]string, 0)
	for rows.Next() {
		var address string
		if err = rows.Scan(&address); err != nil {
			return nil, classify(err)
		}
		addresses = append(addresses, address)
	}
	return addresses, classify(rows.Err())
}
func (r *PostgresRepository) ConsumePlatformAdminAssertion(ctx context.Context, audience, nonce string, expires time.Time) (bool, error) {
	var ok bool
	err := r.pool.QueryRow(ctx, `SELECT consume_platform_admin_assertion($1,NULLIF($2,'')::uuid,$3)`, audience, nonce, expires).Scan(&ok)
	return ok, err
}
func (r *PostgresRepository) ServiceIdentityEnabled(ctx context.Context, id, purpose string) (bool, error) {
	var enabled bool
	err := r.within(ctx, Scope{}, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT enabled FROM platform_admin_service_identities WHERE id=$1 AND purpose=$2`, id, purpose).Scan(&enabled)
	})
	return enabled, classify(err)
}
func (r *PostgresRepository) AuthorizedPlatformGrants(ctx context.Context, actorID, tenantID string) ([]Grant, error) {
	grants := []Grant{}
	err := pgx.BeginTxFunc(ctx, r.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT set_config('app.admin_user_id',$1,true)`, actorID); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT DISTINCT rp.permission_key,COALESCE(b.tenant_id::text,'') FROM admin_role_bindings b JOIN admin_users u ON u.id=b.user_id JOIN admin_role_permissions rp ON rp.role_key=b.role_key WHERE b.user_id=$1 AND u.status='active' AND b.revoked_at IS NULL AND (b.expires_at IS NULL OR b.expires_at>clock_timestamp()) AND (rp.permission_key LIKE 'platform_config:%' OR rp.permission_key LIKE 'provider_ops:%' OR rp.permission_key LIKE 'provider_config:%' OR rp.permission_key LIKE 'migration:%' OR rp.permission_key LIKE 'retention:%') AND (($2='' AND b.tenant_id IS NULL) OR ($2<>'' AND (b.tenant_id IS NULL OR b.tenant_id=$2::uuid))) ORDER BY 1,2`, actorID, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var grant Grant
			if err = rows.Scan(&grant.Permission, &grant.TenantID); err != nil {
				return err
			}
			grants = append(grants, grant)
		}
		return rows.Err()
	})
	return grants, classify(err)
}

func (r *PostgresRepository) within(ctx context.Context, scope Scope, fn func(pgx.Tx) error) error {
	return pgx.BeginTxFunc(ctx, r.pool, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx pgx.Tx) error {
		global := "false"
		tenants := ""
		if scope.TenantID == "" {
			global = "true"
		} else {
			tenants = scope.TenantID
		}
		if _, err := tx.Exec(ctx, `SELECT set_config('app.platform_admin_global',$1,true),set_config('app.platform_admin_tenants',$2,true)`, global, tenants); err != nil {
			return err
		}
		return fn(tx)
	})
}

func (r *PostgresRepository) mutate(ctx context.Context, p Principal, scope Scope, operation string, idem Idempotency, out any, fn func(pgx.Tx) error) error {
	return r.within(ctx, scope, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, scope.TenantID+"\x1f"+p.ActorID+"\x1f"+operation+"\x1f"+idem.Key); err != nil {
			return err
		}
		var storedHash, body []byte
		err := tx.QueryRow(ctx, `SELECT request_hash,response_body FROM platform_admin_idempotency WHERE scope_id=platform_scope_uuid(NULLIF($1,'')::uuid) AND actor_id=$2 AND operation=$3 AND idempotency_key=$4 AND expires_at>clock_timestamp() FOR UPDATE`, scope.TenantID, p.ActorID, operation, idem.Key).Scan(&storedHash, &body)
		if err == nil {
			if !bytes.Equal(storedHash, idem.Fingerprint[:]) {
				return ErrIdempotencyConflict
			}
			if json.Unmarshal(body, out) != nil {
				return ErrDependency
			}
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err = fn(tx); err != nil {
			return err
		}
		response, err := json.Marshal(out)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO platform_admin_idempotency(scope_id,tenant_id,actor_id,operation,idempotency_key,request_hash,response_status,response_body,created_at,expires_at) VALUES(platform_scope_uuid(NULLIF($1,'')::uuid),NULLIF($1,'')::uuid,$2,$3,$4,$5,200,$6::jsonb,$7,$8)`, scope.TenantID, p.ActorID, operation, idem.Key, idem.Fingerprint[:], response, r.now(), r.now().Add(24*time.Hour))
		return classify(err)
	})
}

const changeColumns = `id::text,COALESCE(tenant_id::text,''),kind::text,logical_key,version,COALESCE(based_on_version,0),COALESCE(rollback_of_snapshot_id::text,''),payload,payload_hash,status::text,reason,requested_by::text,COALESCE(approved_by::text,''),COALESCE(rejected_by::text,''),scheduled_for,activated_at,created_at,updated_at,row_version`

type scanner interface{ Scan(...any) error }

func scanChange(row scanner) (ChangeRequest, error) {
	var v ChangeRequest
	var hash []byte
	err := row.Scan(&v.ID, &v.TenantID, &v.Kind, &v.LogicalKey, &v.Version, &v.BasedOnVersion, &v.RollbackOfSnapshotID, &v.Payload, &hash, &v.Status, &v.Reason, &v.RequestedBy, &v.ApprovedBy, &v.RejectedBy, &v.ScheduledFor, &v.ActivatedAt, &v.CreatedAt, &v.UpdatedAt, &v.RowVersion)
	v.PayloadHash = hex.EncodeToString(hash)
	return v, classify(err)
}

const snapshotColumns = `s.id::text,COALESCE(s.tenant_id::text,''),s.change_request_id::text,s.kind::text,s.logical_key,s.version,s.payload,s.payload_hash,COALESCE(s.rollback_of_snapshot_id::text,''),s.activated_at,COALESCE(h.fence_token,0)`

func scanSnapshot(row scanner) (Snapshot, error) {
	var v Snapshot
	var hash []byte
	err := row.Scan(&v.ID, &v.TenantID, &v.ChangeRequestID, &v.Kind, &v.LogicalKey, &v.Version, &v.Payload, &hash, &v.RollbackOfSnapshotID, &v.ActivatedAt, &v.FenceToken)
	v.PayloadHash = hex.EncodeToString(hash)
	return v, classify(err)
}

func (r *PostgresRepository) CreateDraft(ctx context.Context, p Principal, input CreateInput, idem Idempotency) (ChangeRequest, error) {
	scope := Scope{input.TenantID}
	var out ChangeRequest
	err := r.mutate(ctx, p, scope, "create-draft", idem, &out, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, input.TenantID+"\x1f"+string(input.Kind)+"\x1f"+input.LogicalKey); err != nil {
			return err
		}
		var current int64
		err := tx.QueryRow(ctx, `SELECT s.version FROM platform_config_heads h JOIN platform_config_snapshots s ON s.id=h.snapshot_id AND s.scope_id=h.scope_id WHERE h.scope_id=platform_scope_uuid(NULLIF($1,'')::uuid) AND h.kind=$2 AND h.logical_key=$3 FOR UPDATE`, input.TenantID, input.Kind, input.LogicalKey).Scan(&current)
		if errors.Is(err, pgx.ErrNoRows) {
			current = 0
		} else if err != nil {
			return err
		}
		if input.BasedOnVersion != current {
			return ErrConflict
		}
		var next int64
		if err = tx.QueryRow(ctx, `SELECT COALESCE(max(version),0)+1 FROM platform_config_change_requests WHERE scope_id=platform_scope_uuid(NULLIF($1,'')::uuid) AND kind=$2 AND logical_key=$3`, input.TenantID, input.Kind, input.LogicalKey).Scan(&next); err != nil {
			return err
		}
		id, err := ids.New()
		if err != nil {
			return err
		}
		now := r.now()
		row := tx.QueryRow(ctx, `INSERT INTO platform_config_change_requests(id,tenant_id,scope_id,kind,logical_key,version,based_on_version,payload,payload_hash,status,reason,requested_by,created_at,updated_at) VALUES($1,NULLIF($2,'')::uuid,platform_scope_uuid(NULLIF($2,'')::uuid),$3,$4,$5,$6,$7::jsonb,digest(($7::jsonb)::text,'sha256'),'draft',$8,$9,$10,$10) RETURNING `+changeColumns, id, input.TenantID, input.Kind, input.LogicalKey, next, current, input.Payload, input.Reason, p.ActorID, now)
		out, err = scanChange(row)
		if err != nil {
			return err
		}
		return r.auditOutbox(ctx, tx, p, scope, "change.drafted", "change_request", out.ID, input.Reason, out.Version, map[string]any{"kind": input.Kind, "logical_key": input.LogicalKey, "payload_hash": out.PayloadHash})
	})
	return out, classify(err)
}

func (r *PostgresRepository) GetChange(ctx context.Context, p Principal, scope Scope, id string) (ChangeRequest, error) {
	var out ChangeRequest
	err := r.within(ctx, scope, func(tx pgx.Tx) error {
		var err error
		out, err = scanChange(tx.QueryRow(ctx, `SELECT `+changeColumns+` FROM platform_config_change_requests WHERE id=$1 AND scope_id=platform_scope_uuid(NULLIF($2,'')::uuid)`, id, scope.TenantID))
		return err
	})
	return out, classify(err)
}
func (r *PostgresRepository) ListChanges(ctx context.Context, p Principal, scope Scope, kind Kind, status Status, cursor string, limit int) (Page[ChangeRequest], error) {
	out := Page[ChangeRequest]{Items: []ChangeRequest{}}
	err := r.within(ctx, scope, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+changeColumns+` FROM platform_config_change_requests WHERE scope_id=platform_scope_uuid(NULLIF($1,'')::uuid) AND ($2='' OR kind::text=$2) AND ($3='' OR status::text=$3) AND ($4='' OR id<$4::uuid) ORDER BY id DESC LIMIT $5`, scope.TenantID, kind, status, cursor, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			v, e := scanChange(rows)
			if e != nil {
				return e
			}
			out.Items = append(out.Items, v)
		}
		if len(out.Items) > limit {
			out.NextCursor = out.Items[limit-1].ID
			out.Items = out.Items[:limit]
		}
		return rows.Err()
	})
	return out, classify(err)
}

func (r *PostgresRepository) transition(ctx context.Context, p Principal, scope Scope, id, operation string, expected int64, from, to Status, reason string, idem Idempotency, extra func(pgx.Tx, *ChangeRequest) error) (ChangeRequest, error) {
	var out ChangeRequest
	err := r.mutate(ctx, p, scope, operation, idem, &out, func(tx pgx.Tx) error {
		current, err := scanChange(tx.QueryRow(ctx, `SELECT `+changeColumns+` FROM platform_config_change_requests WHERE id=$1 AND scope_id=platform_scope_uuid(NULLIF($2,'')::uuid) FOR UPDATE`, id, scope.TenantID))
		if err != nil {
			return err
		}
		if current.RowVersion != expected || current.Status != from {
			return ErrConflict
		}
		if extra != nil {
			if err = extra(tx, &current); err != nil {
				return err
			}
		}
		now := r.now()
		query := `UPDATE platform_config_change_requests SET status=$3,updated_at=$4,row_version=row_version+1`
		args := []any{id, scope.TenantID, to, now, p.ActorID}
		switch to {
		case StatusApprovalRequested:
			query += `,requested_at=$4`
		case StatusApproved:
			query += `,approved_by=$5,decided_at=$4`
		case StatusRejected:
			query += `,rejected_by=$5,decided_at=$4`
		}
		query += ` WHERE id=$1 AND scope_id=platform_scope_uuid(NULLIF($2,'')::uuid) RETURNING ` + changeColumns
		out, err = scanChange(tx.QueryRow(ctx, query, args...))
		if err != nil {
			return err
		}
		return r.auditOutbox(ctx, tx, p, scope, "change."+string(to), "change_request", out.ID, reason, out.Version, map[string]any{"row_version": out.RowVersion})
	})
	return out, classify(err)
}
func (r *PostgresRepository) RequestApproval(ctx context.Context, p Principal, scope Scope, id string, input DecisionInput, idem Idempotency) (ChangeRequest, error) {
	return r.transition(ctx, p, scope, id, "request-approval", input.ExpectedRowVersion, StatusDraft, StatusApprovalRequested, input.Reason, idem, nil)
}
func (r *PostgresRepository) Decide(ctx context.Context, p Principal, scope Scope, id string, approve bool, input DecisionInput, idem Idempotency) (ChangeRequest, error) {
	to := StatusRejected
	op := "reject"
	if approve {
		to = StatusApproved
		op = "approve"
	}
	return r.transition(ctx, p, scope, id, op, input.ExpectedRowVersion, StatusApprovalRequested, to, input.Reason, idem, func(_ pgx.Tx, current *ChangeRequest) error {
		if current.RequestedBy == p.ActorID {
			return ErrForbidden
		}
		return nil
	})
}

func (r *PostgresRepository) Schedule(ctx context.Context, p Principal, scope Scope, id string, input ScheduleInput, idem Idempotency) (ChangeRequest, error) {
	var out ChangeRequest
	err := r.mutate(ctx, p, scope, "schedule", idem, &out, func(tx pgx.Tx) error {
		current, err := scanChange(tx.QueryRow(ctx, `SELECT `+changeColumns+` FROM platform_config_change_requests WHERE id=$1 AND scope_id=platform_scope_uuid(NULLIF($2,'')::uuid) FOR UPDATE`, id, scope.TenantID))
		if err != nil {
			return err
		}
		if current.Status != StatusApproved || current.RowVersion != input.ExpectedRowVersion {
			return ErrConflict
		}
		now := r.now()
		out, err = scanChange(tx.QueryRow(ctx, `UPDATE platform_config_change_requests SET status='scheduled',scheduled_by=$3,scheduled_for=$4,updated_at=$5,row_version=row_version+1 WHERE id=$1 AND scope_id=platform_scope_uuid(NULLIF($2,'')::uuid) RETURNING `+changeColumns, id, scope.TenantID, p.ActorID, input.ActivateAt, now))
		if err != nil {
			return err
		}
		return r.auditOutbox(ctx, tx, p, scope, "change.scheduled", "change_request", id, input.Reason, out.Version, map[string]any{"activate_at": input.ActivateAt, "row_version": out.RowVersion})
	})
	return out, classify(err)
}

func (r *PostgresRepository) Activate(ctx context.Context, p Principal, scope Scope, id string, input ActivateInput, idem Idempotency) (Snapshot, error) {
	var out Snapshot
	err := r.mutate(ctx, p, scope, "activate", idem, &out, func(tx pgx.Tx) error {
		change, err := scanChange(tx.QueryRow(ctx, `SELECT `+changeColumns+` FROM platform_config_change_requests WHERE id=$1 AND scope_id=platform_scope_uuid(NULLIF($2,'')::uuid) FOR UPDATE`, id, scope.TenantID))
		if err != nil {
			return err
		}
		if change.Status != StatusScheduled || change.RowVersion != input.ExpectedRowVersion {
			return ErrConflict
		}
		now := r.now()
		if change.ScheduledFor == nil || change.ScheduledFor.After(now) {
			return ErrScheduledForFuture
		}
		if input.LeaseOwner != "" {
			var owner string
			var token int
			var until time.Time
			if err = tx.QueryRow(ctx, `SELECT COALESCE(activation_lease_owner,''),activation_attempts,activation_lease_until FROM platform_config_change_requests WHERE id=$1`, id).Scan(&owner, &token, &until); err != nil {
				return err
			}
			if owner != input.LeaseOwner || token != input.LeaseToken || !until.After(now) {
				return ErrConflict
			}
		}
		var previousID string
		var currentVersion, fence int64
		err = tx.QueryRow(ctx, `SELECT h.snapshot_id::text,s.version,h.fence_token FROM platform_config_heads h JOIN platform_config_snapshots s ON s.id=h.snapshot_id AND s.scope_id=h.scope_id WHERE h.scope_id=platform_scope_uuid(NULLIF($1,'')::uuid) AND h.kind=$2 AND h.logical_key=$3 FOR UPDATE`, scope.TenantID, change.Kind, change.LogicalKey).Scan(&previousID, &currentVersion, &fence)
		if errors.Is(err, pgx.ErrNoRows) {
			currentVersion = 0
			fence = 0
			previousID = ""
		} else if err != nil {
			return err
		}
		if currentVersion != change.BasedOnVersion || fence != input.ExpectedFenceToken {
			return ErrConflict
		}
		snapshotID, err := ids.New()
		if err != nil {
			return err
		}
		activationID, err := ids.New()
		if err != nil {
			return err
		}
		newFence := fence + 1
		_, err = tx.Exec(ctx, `INSERT INTO platform_config_snapshots(id,scope_id,tenant_id,change_request_id,kind,logical_key,version,payload,payload_hash,rollback_of_snapshot_id,activated_by,activated_at) VALUES($1,platform_scope_uuid(NULLIF($2,'')::uuid),NULLIF($2,'')::uuid,$3,$4,$5,$6,$7::jsonb,decode($8,'hex'),NULLIF($9,'')::uuid,$10,$11)`, snapshotID, scope.TenantID, change.ID, change.Kind, change.LogicalKey, change.Version, change.Payload, change.PayloadHash, change.RollbackOfSnapshotID, p.ActorID, now)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO platform_config_heads(scope_id,tenant_id,kind,logical_key,snapshot_id,fence_token,updated_at) VALUES(platform_scope_uuid(NULLIF($1,'')::uuid),NULLIF($1,'')::uuid,$2,$3,$4,$5,$6) ON CONFLICT(scope_id,kind,logical_key) DO UPDATE SET snapshot_id=EXCLUDED.snapshot_id,fence_token=EXCLUDED.fence_token,updated_at=EXCLUDED.updated_at`, scope.TenantID, change.Kind, change.LogicalKey, snapshotID, newFence, now)
		if err != nil {
			return err
		}
		activationType := "activate"
		if change.RollbackOfSnapshotID != "" {
			activationType = "rollback"
		}
		_, err = tx.Exec(ctx, `INSERT INTO platform_config_activations(id,scope_id,tenant_id,kind,logical_key,snapshot_id,previous_snapshot_id,fence_token,activation_type,actor_id,occurred_at) VALUES($1,platform_scope_uuid(NULLIF($2,'')::uuid),NULLIF($2,'')::uuid,$3,$4,$5,NULLIF($6,'')::uuid,$7,$8,$9,$10)`, activationID, scope.TenantID, change.Kind, change.LogicalKey, snapshotID, previousID, newFence, activationType, p.ActorID, now)
		if err != nil {
			return err
		}
		if previousID != "" {
			_, err = tx.Exec(ctx, `UPDATE platform_config_change_requests SET status='superseded',updated_at=$2,row_version=row_version+1 WHERE id=(SELECT change_request_id FROM platform_config_snapshots WHERE id=$1) AND status='active'`, previousID, now)
			if err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, `UPDATE platform_config_change_requests SET status='active',activated_by=$2,activated_at=$3,updated_at=$3,row_version=row_version+1,activation_lease_owner=NULL,activation_lease_until=NULL WHERE id=$1`, change.ID, p.ActorID, now)
		if err != nil {
			return err
		}
		out = Snapshot{ID: snapshotID, TenantID: scope.TenantID, ChangeRequestID: change.ID, Kind: change.Kind, LogicalKey: change.LogicalKey, Version: change.Version, Payload: change.Payload, PayloadHash: change.PayloadHash, RollbackOfSnapshotID: change.RollbackOfSnapshotID, ActivatedAt: now, FenceToken: newFence}
		return r.auditOutbox(ctx, tx, p, scope, "snapshot."+activationType, "snapshot", snapshotID, input.Reason, newFence, map[string]any{"kind": change.Kind, "logical_key": change.LogicalKey, "version": change.Version, "payload_hash": change.PayloadHash, "fence_token": newFence, "previous_snapshot_id": previousID})
	})
	return out, classify(err)
}

func (r *PostgresRepository) CreateRollback(ctx context.Context, p Principal, input RollbackInput, idem Idempotency) (ChangeRequest, error) {
	scope := Scope{input.TenantID}
	var out ChangeRequest
	err := r.mutate(ctx, p, scope, "rollback-draft", idem, &out, func(tx pgx.Tx) error {
		var historic Snapshot
		historic, err := scanSnapshot(tx.QueryRow(ctx, `SELECT `+snapshotColumns+` FROM platform_config_snapshots s LEFT JOIN platform_config_heads h ON h.snapshot_id=s.id AND h.scope_id=s.scope_id WHERE s.id=$1 AND s.scope_id=platform_scope_uuid(NULLIF($2,'')::uuid)`, input.SnapshotID, input.TenantID))
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, input.TenantID+"\x1f"+string(historic.Kind)+"\x1f"+historic.LogicalKey); err != nil {
			return err
		}
		var current int64
		err = tx.QueryRow(ctx, `SELECT s.version FROM platform_config_heads h JOIN platform_config_snapshots s ON s.id=h.snapshot_id AND s.scope_id=h.scope_id WHERE h.scope_id=platform_scope_uuid(NULLIF($1,'')::uuid) AND h.kind=$2 AND h.logical_key=$3 FOR UPDATE`, input.TenantID, historic.Kind, historic.LogicalKey).Scan(&current)
		if err != nil {
			return classify(err)
		}
		var next int64
		if err = tx.QueryRow(ctx, `SELECT max(version)+1 FROM platform_config_change_requests WHERE scope_id=platform_scope_uuid(NULLIF($1,'')::uuid) AND kind=$2 AND logical_key=$3`, input.TenantID, historic.Kind, historic.LogicalKey).Scan(&next); err != nil {
			return err
		}
		id, err := ids.New()
		if err != nil {
			return err
		}
		now := r.now()
		out, err = scanChange(tx.QueryRow(ctx, `INSERT INTO platform_config_change_requests(id,tenant_id,scope_id,kind,logical_key,version,based_on_version,rollback_of_snapshot_id,payload,payload_hash,status,reason,requested_by,created_at,updated_at) VALUES($1,NULLIF($2,'')::uuid,platform_scope_uuid(NULLIF($2,'')::uuid),$3,$4,$5,$6,$7,$8::jsonb,decode($9,'hex'),'draft',$10,$11,$12,$12) RETURNING `+changeColumns, id, input.TenantID, historic.Kind, historic.LogicalKey, next, current, historic.ID, historic.Payload, historic.PayloadHash, input.Reason, p.ActorID, now))
		if err != nil {
			return err
		}
		return r.auditOutbox(ctx, tx, p, scope, "change.rollback_drafted", "change_request", out.ID, input.Reason, out.Version, map[string]any{"rollback_of_snapshot_id": historic.ID, "payload_hash": historic.PayloadHash})
	})
	return out, classify(err)
}

func (r *PostgresRepository) ListSnapshots(ctx context.Context, p Principal, scope Scope, kind Kind, key, cursor string, limit int) (Page[Snapshot], error) {
	out := Page[Snapshot]{Items: []Snapshot{}}
	err := r.within(ctx, scope, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+snapshotColumns+` FROM platform_config_snapshots s LEFT JOIN platform_config_heads h ON h.snapshot_id=s.id AND h.scope_id=s.scope_id WHERE s.scope_id=platform_scope_uuid(NULLIF($1,'')::uuid) AND ($2='' OR s.kind::text=$2) AND ($3='' OR s.logical_key=$3) AND ($4='' OR s.id<$4::uuid) ORDER BY s.id DESC LIMIT $5`, scope.TenantID, kind, key, cursor, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			v, e := scanSnapshot(rows)
			if e != nil {
				return e
			}
			out.Items = append(out.Items, v)
		}
		if len(out.Items) > limit {
			out.NextCursor = out.Items[limit-1].ID
			out.Items = out.Items[:limit]
		}
		return rows.Err()
	})
	return out, classify(err)
}

func (r *PostgresRepository) ActiveSnapshot(ctx context.Context, scope Scope, kind Kind, key string) (Snapshot, error) {
	if !allKinds[kind] || !logicalKeyPattern.MatchString(key) {
		return Snapshot{}, ErrInvalid
	}
	var out Snapshot
	err := r.within(ctx, scope, func(tx pgx.Tx) error {
		var err error
		out, err = scanSnapshot(tx.QueryRow(ctx, `SELECT `+snapshotColumns+` FROM platform_config_heads h JOIN platform_config_snapshots s ON s.id=h.snapshot_id AND s.scope_id=h.scope_id AND s.kind=h.kind AND s.logical_key=h.logical_key WHERE h.scope_id=platform_scope_uuid(NULLIF($1,'')::uuid) AND h.kind=$2 AND h.logical_key=$3`, scope.TenantID, kind, key))
		return err
	})
	return out, classify(err)
}

func (r *PostgresRepository) ActiveRuntimeState(ctx context.Context, scope Scope, keys []RuntimeSnapshotKey) (RuntimeState, error) {
	if len(keys) < 1 || len(keys) > 256 {
		return RuntimeState{}, ErrInvalid
	}
	seen := make(map[RuntimeSnapshotKey]struct{}, len(keys))
	for _, key := range keys {
		if !allKinds[key.Kind] || !logicalKeyPattern.MatchString(key.LogicalKey) {
			return RuntimeState{}, ErrInvalid
		}
		if _, duplicate := seen[key]; duplicate {
			return RuntimeState{}, ErrInvalid
		}
		seen[key] = struct{}{}
	}
	state := RuntimeState{Snapshots: make([]Snapshot, 0, len(keys)), Paused: make(map[RuntimeSnapshotKey]bool, len(keys))}
	err := r.within(ctx, scope, func(tx pgx.Tx) error {
		for _, key := range keys {
			snapshot, err := scanSnapshot(tx.QueryRow(ctx, `SELECT `+snapshotColumns+` FROM platform_config_heads h JOIN platform_config_snapshots s ON s.id=h.snapshot_id AND s.scope_id=h.scope_id AND s.kind=h.kind AND s.logical_key=h.logical_key WHERE h.scope_id=platform_scope_uuid(NULLIF($1,'')::uuid) AND h.kind=$2 AND h.logical_key=$3`, scope.TenantID, key.Kind, key.LogicalKey))
			if err != nil {
				return err
			}
			if snapshot.FenceToken < 1 || snapshot.Version < 1 || snapshot.Kind != key.Kind || snapshot.LogicalKey != key.LogicalKey {
				return ErrConflict
			}
			state.Snapshots = append(state.Snapshots, snapshot)
			var action string
			err = tx.QueryRow(ctx, `SELECT action FROM platform_emergency_pause_events WHERE scope_id=platform_scope_uuid(NULLIF($1,'')::uuid) AND kind=$2 AND logical_key=$3 ORDER BY occurred_at DESC,id DESC LIMIT 1`, scope.TenantID, key.Kind, key.LogicalKey).Scan(&action)
			switch {
			case errors.Is(err, pgx.ErrNoRows):
				state.Paused[key] = false
			case err != nil:
				return err
			default:
				state.Paused[key] = action == "pause"
			}
		}
		return nil
	})
	return state, classify(err)
}

func (r *PostgresRepository) ClaimDueActivations(ctx context.Context, actorID, workerID string, limit int, lease time.Duration) ([]ActivationJob, error) {
	jobs := []ActivationJob{}
	millis := lease.Milliseconds()
	if millis < 1000 || millis > 300000 {
		return nil, ErrInvalid
	}
	err := r.within(ctx, Scope{}, func(tx pgx.Tx) error {
		var enabled bool
		if err := tx.QueryRow(ctx, `SELECT enabled FROM platform_admin_service_identities WHERE id=$1 AND purpose='scheduled_activation'`, actorID).Scan(&enabled); err != nil || !enabled {
			if err == nil {
				return ErrForbidden
			}
			return classify(err)
		}
		rows, err := tx.Query(ctx, `WITH due AS (SELECT id FROM platform_config_change_requests WHERE status='scheduled' AND scheduled_for<=clock_timestamp() AND (activation_lease_until IS NULL OR activation_lease_until<clock_timestamp()) ORDER BY scheduled_for,id FOR UPDATE SKIP LOCKED LIMIT $1) UPDATE platform_config_change_requests c SET activation_lease_owner=$2,activation_lease_until=clock_timestamp()+$3 * interval '1 millisecond',activation_attempts=activation_attempts+1 FROM due WHERE c.id=due.id RETURNING c.id::text,COALESCE(c.tenant_id::text,''),c.row_version,COALESCE((SELECT h.fence_token FROM platform_config_heads h WHERE h.scope_id=c.scope_id AND h.kind=c.kind AND h.logical_key=c.logical_key),0),c.activation_attempts`, limit, workerID, millis)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var job ActivationJob
			if err = rows.Scan(&job.ChangeID, &job.TenantID, &job.RowVersion, &job.ExpectedFenceToken, &job.ClaimToken); err != nil {
				return err
			}
			jobs = append(jobs, job)
		}
		return rows.Err()
	})
	return jobs, classify(err)
}

func (r *PostgresRepository) ClaimPlatformOutbox(ctx context.Context, workerID string, limit int, lease time.Duration) ([]OutboxEvent, error) {
	events := []OutboxEvent{}
	millis := lease.Milliseconds()
	if millis < 1000 || millis > 300000 || !ids.Valid(workerID) {
		return nil, ErrInvalid
	}
	err := r.within(ctx, Scope{}, func(tx pgx.Tx) error {
		var enabled bool
		if err := tx.QueryRow(ctx, `SELECT enabled FROM platform_admin_service_identities WHERE id=$1 AND purpose='outbox_publisher'`, workerID).Scan(&enabled); err != nil || !enabled {
			if err == nil {
				return ErrForbidden
			}
			return classify(err)
		}
		rows, err := tx.Query(ctx, `WITH due AS (SELECT id FROM platform_admin_outbox WHERE published_at IS NULL AND available_at<=clock_timestamp() AND (lease_until IS NULL OR lease_until<clock_timestamp()) ORDER BY available_at,id FOR UPDATE SKIP LOCKED LIMIT $1) UPDATE platform_admin_outbox o SET lease_owner=$2,lease_until=clock_timestamp()+$3 * interval '1 millisecond',attempts=attempts+1,claim_token=claim_token+1 FROM due WHERE o.id=due.id RETURNING o.id::text,COALESCE(o.tenant_id::text,''),o.event_type,o.aggregate_type,o.aggregate_id,o.aggregate_version,o.payload,o.occurred_at,o.attempts,o.claim_token`, limit, workerID, millis)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var event OutboxEvent
			if err = rows.Scan(&event.ID, &event.TenantID, &event.EventType, &event.AggregateType, &event.AggregateID, &event.AggregateVersion, &event.Payload, &event.OccurredAt, &event.Attempts, &event.ClaimToken); err != nil {
				return err
			}
			events = append(events, event)
		}
		return rows.Err()
	})
	return events, classify(err)
}
func (r *PostgresRepository) MarkPlatformOutboxPublished(ctx context.Context, event OutboxEvent, workerID string, at time.Time) error {
	return r.within(ctx, Scope{}, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE platform_admin_outbox SET published_at=$4,lease_owner=NULL,lease_until=NULL WHERE id=$1 AND lease_owner=$2 AND claim_token=$3 AND lease_until>=clock_timestamp() AND published_at IS NULL`, event.ID, workerID, event.ClaimToken, at)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrConflict
		}
		return nil
	})
}
func (r *PostgresRepository) ReleasePlatformOutbox(ctx context.Context, event OutboxEvent, workerID string, next time.Time) error {
	return r.within(ctx, Scope{}, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE platform_admin_outbox SET available_at=$4,lease_owner=NULL,lease_until=NULL WHERE id=$1 AND lease_owner=$2 AND claim_token=$3 AND lease_until>=clock_timestamp() AND published_at IS NULL`, event.ID, workerID, event.ClaimToken, next)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrConflict
		}
		return nil
	})
}

func (r *PostgresRepository) EmergencyPause(ctx context.Context, p Principal, input PauseInput, idem Idempotency) error {
	scope := Scope{input.TenantID}
	var out map[string]any
	err := r.mutate(ctx, p, scope, "emergency-"+input.Action, idem, &out, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, input.TenantID+"\x1f"+string(input.Kind)+"\x1f"+input.LogicalKey+"\x1fpause"); err != nil {
			return err
		}
		var priorID, priorAction string
		err := tx.QueryRow(ctx, `SELECT id::text,action FROM platform_emergency_pause_events WHERE scope_id=platform_scope_uuid(NULLIF($1,'')::uuid) AND kind=$2 AND logical_key=$3 ORDER BY occurred_at DESC,id DESC LIMIT 1`, input.TenantID, input.Kind, input.LogicalKey).Scan(&priorID, &priorAction)
		if errors.Is(err, pgx.ErrNoRows) {
			priorID = ""
			priorAction = "resume"
		} else if err != nil {
			return err
		}
		if priorAction == input.Action {
			return ErrConflict
		}
		id, err := ids.New()
		if err != nil {
			return err
		}
		now := r.now()
		_, err = tx.Exec(ctx, `INSERT INTO platform_emergency_pause_events(id,scope_id,tenant_id,kind,logical_key,action,reason,actor_id,step_up_at,occurred_at,previous_event_id) VALUES($1,platform_scope_uuid(NULLIF($2,'')::uuid),NULLIF($2,'')::uuid,$3,$4,$5,$6,$7,$8,$9,NULLIF($10,'')::uuid)`, id, input.TenantID, input.Kind, input.LogicalKey, input.Action, input.Reason, p.ActorID, p.StepUpAt, now, priorID)
		if err != nil {
			return err
		}
		out = map[string]any{"id": id, "action": input.Action}
		return r.auditOutbox(ctx, tx, p, scope, "emergency."+input.Action, "emergency_pause", id, input.Reason, 1, map[string]any{"kind": input.Kind, "logical_key": input.LogicalKey, "previous_event_id": priorID})
	})
	return classify(err)
}

func (r *PostgresRepository) auditOutbox(ctx context.Context, tx pgx.Tx, p Principal, scope Scope, action, resourceType, resourceID, reason string, version int64, details any) error {
	encoded, err := json.Marshal(details)
	if err != nil {
		return err
	}
	auditID, err := ids.New()
	if err != nil {
		return err
	}
	now := r.now()
	if _, err = tx.Exec(ctx, `SELECT append_platform_admin_audit($1,NULLIF($2,'')::uuid,$3,$4,$5,$6,$7,$8,$9::jsonb,$10)`, auditID, scope.TenantID, p.ActorID, p.SessionID, action, resourceType, resourceID, reason, encoded, now); err != nil {
		return err
	}
	outboxID, err := ids.New()
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]any{"event_id": outboxID, "event_type": "platform_admin." + action, "resource_type": resourceType, "resource_id": resourceID, "aggregate_version": version, "occurred_at": now, "details": json.RawMessage(encoded)})
	_, err = tx.Exec(ctx, `INSERT INTO platform_admin_outbox(id,scope_id,tenant_id,aggregate_type,aggregate_id,aggregate_version,event_type,payload,occurred_at,available_at) VALUES($1,platform_scope_uuid(NULLIF($2,'')::uuid),NULLIF($2,'')::uuid,$3,$4,$5,$6,$7::jsonb,$8,$8)`, outboxID, scope.TenantID, resourceType, resourceID, version, "platform_admin."+action, payload, now)
	return classify(err)
}

func classify(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pgerr *pgconn.PgError
	if errors.As(err, &pgerr) {
		switch pgerr.Code {
		case "23505", "23P01", "40001", "40P01":
			return fmt.Errorf("%w: concurrent update", ErrConflict)
		case "23503", "23514", "22P02", "22001", "22023":
			return fmt.Errorf("%w: database constraint", ErrInvalid)
		}
	}
	return err
}

var _ Repository = (*PostgresRepository)(nil)
var _ GrantSource = (*PostgresRepository)(nil)
var _ ActiveSnapshotReader = (*PostgresRepository)(nil)
var _ RuntimeStateReader = (*PostgresRepository)(nil)
var _ ActivationSchedulerRepository = (*PostgresRepository)(nil)
var _ OutboxStore = (*PostgresRepository)(nil)
