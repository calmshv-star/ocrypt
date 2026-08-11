package matchingadmin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/ids"
	"github.com/calmshv-star/ocrypt/backend/internal/management"
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
		return nil, ErrInvalid
	}
	return &PostgresRepository{pool: pool, now: func() time.Time { return time.Now().UTC() }}, nil
}

func (s *PostgresRepository) withinTenant(ctx context.Context, p management.Principal, fn func(pgx.Tx) error) error {
	return pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id',$1,true),set_config('app.admin_user_id',$2,true)`, p.TenantID, p.ActorID); err != nil {
			return err
		}
		return fn(tx)
	})
}

func (s *PostgresRepository) Create(ctx context.Context, p management.Principal, input PolicyInput, idem Idempotency) (result PolicyChange, replay bool, err error) {
	err = s.withinTenant(ctx, p, func(tx pgx.Tx) error {
		if found, e := s.replay(ctx, tx, p, "create", idem, &result); e != nil || found {
			replay = found
			return e
		}
		id, e := ids.New()
		if e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, p.TenantID+"\x1f"+p.MerchantID+"\x1fmatching-policy-version"); e != nil {
			return e
		}
		var version int64
		if e = tx.QueryRow(ctx, `SELECT GREATEST(
COALESCE((SELECT max(version) FROM automated_matching_policies WHERE tenant_id=$1 AND merchant_id=$2),0),
COALESCE((SELECT max(proposed_version) FROM automated_matching_policy_changes WHERE tenant_id=$1 AND merchant_id=$2),0))+1`, p.TenantID, p.MerchantID).Scan(&version); e != nil {
			return e
		}
		now := s.now()
		_, e = tx.Exec(ctx, `INSERT INTO automated_matching_policy_changes
(id,tenant_id,merchant_id,proposed_version,accumulate_partials,underpayment_tolerance_bps,overpayment_mode,accept_late_within_grace,require_same_sender,gasfree_enabled,gasfree_fee_collectors,status,created_by,created_at,updated_at,version)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,'draft',$12,$13,$13,1)`, id, p.TenantID, p.MerchantID, version, input.AccumulatePartials, input.UnderpaymentToleranceBPS, input.OverpaymentMode, input.AcceptLateWithinGrace, input.RequireSameSender, input.GasFreeEnabled, input.GasFreeFeeCollectors, p.ActorID, now)
		if e != nil {
			return classify(e)
		}
		if result, e = loadPolicyChange(ctx, tx, p, id); e != nil {
			return e
		}
		if e = appendPolicyAudit(ctx, tx, p, result, "draft_created", ""); e != nil {
			return e
		}
		return s.remember(ctx, tx, p, "create", idem, result)
	})
	return
}

func (s *PostgresRepository) Get(ctx context.Context, p management.Principal, id string) (result PolicyChange, err error) {
	err = s.withinTenant(ctx, p, func(tx pgx.Tx) error {
		var e error
		result, e = loadPolicyChange(ctx, tx, p, id)
		return e
	})
	return
}

func (s *PostgresRepository) List(ctx context.Context, p management.Principal, cursor string, limit int) (page Page, err error) {
	err = s.withinTenant(ctx, p, func(tx pgx.Tx) error {
		rows, e := tx.Query(ctx, `SELECT id::text FROM automated_matching_policy_changes WHERE tenant_id=$1 AND merchant_id=$2 AND ($3='' OR id<$3::uuid) ORDER BY id DESC LIMIT $4`, p.TenantID, p.MerchantID, cursor, limit+1)
		if e != nil {
			return e
		}
		defer rows.Close()
		var list []string
		for rows.Next() {
			var id string
			if e = rows.Scan(&id); e != nil {
				return e
			}
			list = append(list, id)
		}
		if e = rows.Err(); e != nil {
			return e
		}
		if len(list) > limit {
			page.NextCursor = list[limit-1]
			list = list[:limit]
		}
		for _, id := range list {
			item, e := loadPolicyChange(ctx, tx, p, id)
			if e != nil {
				return e
			}
			page.Data = append(page.Data, item)
		}
		return nil
	})
	return
}

func (s *PostgresRepository) RequestApproval(ctx context.Context, p management.Principal, id string, input Mutation, idem Idempotency) (result PolicyChange, replay bool, err error) {
	err = s.transition(ctx, p, id, input.Version, input.Reason, "request", idem, func(ctx context.Context, tx pgx.Tx, now time.Time) error {
		command, e := tx.Exec(ctx, `UPDATE automated_matching_policy_changes SET status='pending_approval',requested_by=$1,request_reason=$2,updated_at=$3,version=version+1 WHERE id=$4 AND tenant_id=$5 AND merchant_id=$6 AND version=$7 AND status='draft'`, p.ActorID, input.Reason, now, id, p.TenantID, p.MerchantID, input.Version)
		if e != nil {
			return e
		}
		if command.RowsAffected() != 1 {
			return ErrConflict
		}
		return nil
	}, &result, &replay)
	return
}

func (s *PostgresRepository) Approve(ctx context.Context, p management.Principal, id string, input Mutation, idem Idempotency) (result PolicyChange, replay bool, err error) {
	err = s.transition(ctx, p, id, input.Version, input.Reason, "approve", idem, func(ctx context.Context, tx pgx.Tx, now time.Time) error {
		command, e := tx.Exec(ctx, `UPDATE automated_matching_policy_changes SET status='approved',approved_by=$1,approval_reason=$2,approved_at=$3,updated_at=$3,version=version+1 WHERE id=$4 AND tenant_id=$5 AND merchant_id=$6 AND version=$7 AND status='pending_approval' AND requested_by<>$1`, p.ActorID, input.Reason, now, id, p.TenantID, p.MerchantID, input.Version)
		if e != nil {
			return e
		}
		if command.RowsAffected() != 1 {
			return ErrConflict
		}
		return nil
	}, &result, &replay)
	return
}

func (s *PostgresRepository) Activate(ctx context.Context, p management.Principal, id string, input Activation, idem Idempotency) (result PolicyChange, replay bool, err error) {
	err = s.withinTenant(ctx, p, func(tx pgx.Tx) error {
		if found, e := s.replay(ctx, tx, p, "activate", idem, &result); e != nil || found {
			replay = found
			return e
		}
		now := s.now()
		var requestedBy, approvedBy string
		var proposedVersion int64
		var config PolicyInput
		e := tx.QueryRow(ctx, `SELECT proposed_version,accumulate_partials,underpayment_tolerance_bps,overpayment_mode,accept_late_within_grace,require_same_sender,gasfree_enabled,gasfree_fee_collectors,requested_by::text,approved_by::text
FROM automated_matching_policy_changes WHERE id=$1 AND tenant_id=$2 AND merchant_id=$3 AND version=$4 AND status='approved' AND requested_by<>$5 FOR UPDATE`, id, p.TenantID, p.MerchantID, input.Version, p.ActorID).Scan(&proposedVersion, &config.AccumulatePartials, &config.UnderpaymentToleranceBPS, &config.OverpaymentMode, &config.AcceptLateWithinGrace, &config.RequireSameSender, &config.GasFreeEnabled, &config.GasFreeFeeCollectors, &requestedBy, &approvedBy)
		if errors.Is(e, pgx.ErrNoRows) {
			return ErrConflict
		}
		if e != nil {
			return e
		}
		policyID, e := ids.New()
		if e != nil {
			return e
		}
		canonical, _ := json.Marshal(config)
		hash := sha256.Sum256(canonical)
		_, e = tx.Exec(ctx, `INSERT INTO automated_matching_policies
(id,tenant_id,merchant_id,version,accumulate_partials,underpayment_tolerance_bps,overpayment_mode,accept_late_within_grace,require_same_sender,gasfree_enabled,gasfree_fee_collectors,effective_at,change_request_id,requested_by,approved_by,activated_by,approval_reference,config_hash,created_at)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19)`, policyID, p.TenantID, p.MerchantID, proposedVersion, config.AccumulatePartials, config.UnderpaymentToleranceBPS, config.OverpaymentMode, config.AcceptLateWithinGrace, config.RequireSameSender, config.GasFreeEnabled, config.GasFreeFeeCollectors, input.EffectiveAt, id, requestedBy, approvedBy, p.ActorID, input.Reason, hash[:], now)
		if e != nil {
			return classify(e)
		}
		command, e := tx.Exec(ctx, `UPDATE automated_matching_policy_changes SET status='activated',activated_by=$1,activation_reason=$2,activated_at=$3,effective_at=$4,updated_at=$3,version=version+1 WHERE id=$5 AND tenant_id=$6 AND merchant_id=$7 AND version=$8 AND status='approved' AND requested_by<>$1`, p.ActorID, input.Reason, now, input.EffectiveAt, id, p.TenantID, p.MerchantID, input.Version)
		if e != nil {
			return e
		}
		if command.RowsAffected() != 1 {
			return ErrConflict
		}
		if result, e = loadPolicyChange(ctx, tx, p, id); e != nil {
			return e
		}
		if e = appendPolicyAudit(ctx, tx, p, result, "activated", input.Reason); e != nil {
			return e
		}
		return s.remember(ctx, tx, p, "activate", idem, result)
	})
	return
}

func (s *PostgresRepository) transition(ctx context.Context, p management.Principal, id string, _ int64, reason, operation string, idem Idempotency, mutate func(context.Context, pgx.Tx, time.Time) error, result *PolicyChange, replay *bool) error {
	return s.withinTenant(ctx, p, func(tx pgx.Tx) error {
		if found, e := s.replay(ctx, tx, p, operation, idem, result); e != nil || found {
			*replay = found
			return e
		}
		if e := mutate(ctx, tx, s.now()); e != nil {
			return e
		}
		var e error
		if *result, e = loadPolicyChange(ctx, tx, p, id); e != nil {
			return e
		}
		action := operation + "d"
		if operation == "request" {
			action = "requested"
		}
		if e = appendPolicyAudit(ctx, tx, p, *result, action, reason); e != nil {
			return e
		}
		return s.remember(ctx, tx, p, operation, idem, *result)
	})
}

func loadPolicyChange(ctx context.Context, tx pgx.Tx, p management.Principal, id string) (result PolicyChange, err error) {
	err = tx.QueryRow(ctx, `SELECT c.id::text,c.proposed_version,c.accumulate_partials,c.underpayment_tolerance_bps,c.overpayment_mode,c.accept_late_within_grace,c.require_same_sender,c.gasfree_enabled,c.gasfree_fee_collectors,c.status,c.created_by::text,
COALESCE(c.requested_by::text,''),COALESCE(c.approved_by::text,''),COALESCE(c.activated_by::text,''),COALESCE(c.request_reason,''),COALESCE(c.approval_reason,''),COALESCE(c.activation_reason,''),c.approved_at,c.activated_at,c.effective_at,COALESCE(p.id::text,''),c.created_at,c.updated_at,c.version
FROM automated_matching_policy_changes c LEFT JOIN automated_matching_policies p ON p.change_request_id=c.id AND p.tenant_id=c.tenant_id
WHERE c.id=$1 AND c.tenant_id=$2 AND c.merchant_id=$3`, id, p.TenantID, p.MerchantID).Scan(&result.ID, &result.ProposedVersion, &result.AccumulatePartials, &result.UnderpaymentToleranceBPS, &result.OverpaymentMode, &result.AcceptLateWithinGrace, &result.RequireSameSender, &result.GasFreeEnabled, &result.GasFreeFeeCollectors, &result.Status, &result.CreatedBy, &result.RequestedBy, &result.ApprovedBy, &result.ActivatedBy, &result.RequestReason, &result.ApprovalReason, &result.ActivationReason, &result.ApprovedAt, &result.ActivatedAt, &result.EffectiveAt, &result.ActivatedPolicyID, &result.CreatedAt, &result.UpdatedAt, &result.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, ErrNotFound
	}
	return result, err
}

func (s *PostgresRepository) replay(ctx context.Context, tx pgx.Tx, p management.Principal, operation string, idem Idempotency, output *PolicyChange) (bool, error) {
	if len(idem.Key) < 8 {
		return false, ErrInvalid
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, p.TenantID+"\x1f"+p.MerchantID+"\x1f"+p.ActorID+"\x1f"+operation+"\x1f"+idem.Key); err != nil {
		return false, err
	}
	var hash, body []byte
	err := tx.QueryRow(ctx, `SELECT request_hash,response_body FROM automated_matching_policy_idempotency WHERE tenant_id=$1 AND merchant_id=$2 AND actor_id=$3 AND operation=$4 AND idempotency_key=$5 AND expires_at>clock_timestamp() FOR UPDATE`, p.TenantID, p.MerchantID, p.ActorID, operation, idem.Key).Scan(&hash, &body)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !bytes.Equal(hash, idem.Fingerprint[:]) {
		return false, ErrIdempotency
	}
	if err := json.Unmarshal(body, output); err != nil {
		return false, err
	}
	return true, nil
}

func (s *PostgresRepository) remember(ctx context.Context, tx pgx.Tx, p management.Principal, operation string, idem Idempotency, output PolicyChange) error {
	body, _ := json.Marshal(output)
	_, err := tx.Exec(ctx, `INSERT INTO automated_matching_policy_idempotency(tenant_id,merchant_id,actor_id,operation,idempotency_key,request_hash,resource_id,response_body,created_at,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8::jsonb,$9,$10)`, p.TenantID, p.MerchantID, p.ActorID, operation, idem.Key, idem.Fingerprint[:], output.ID, body, s.now(), s.now().Add(24*time.Hour))
	return classify(err)
}

func appendPolicyAudit(ctx context.Context, tx pgx.Tx, p management.Principal, change PolicyChange, action, reason string) error {
	auditID, err := ids.New()
	if err != nil {
		return err
	}
	details, _ := json.Marshal(map[string]any{"status": change.Status, "proposed_version": change.ProposedVersion, "policy_id": change.ActivatedPolicyID})
	now := time.Now().UTC()
	if _, err = tx.Exec(ctx, `SELECT append_management_audit($1,$2,$3,$4,$5,NULL,$6,'matching_policy_change',$7,NULLIF($8,''),$9::jsonb,$10)`, auditID, p.TenantID, p.MerchantID, p.ActorID, p.SessionID, "matching_policy."+action, change.ID, reason, details, now); err != nil {
		return err
	}
	outboxID, err := ids.New()
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]any{"event_id": outboxID, "event_type": "management.matching_policy." + action, "resource_id": change.ID, "version": change.Version, "occurred_at": now, "details": json.RawMessage(details)})
	_, err = tx.Exec(ctx, `INSERT INTO outbox_events(id,tenant_id,merchant_id,aggregate_type,aggregate_id,aggregate_version,aggregate_sequence,event_type,schema_version,payload,correlation_id,occurred_at,recorded_at,available_at) VALUES($1,$2,$3,'management.matching_policy_change',$4,$5,$5,$6,'1',$7::jsonb,$8,$9,$9,$9)`, outboxID, p.TenantID, p.MerchantID, change.ID, change.Version, "management.matching_policy."+action, payload, auditID, now)
	return err
}

func classify(err error) error {
	if err == nil {
		return nil
	}
	var pgerr *pgconn.PgError
	if errors.As(err, &pgerr) {
		switch pgerr.Code {
		case "23505", "23P01", "40001", "40P01":
			return fmt.Errorf("%w: %s", ErrConflict, pgerr.ConstraintName)
		case "23503", "23514", "22P02", "22001", "22023":
			return ErrInvalid
		}
	}
	return err
}

var _ Repository = (*PostgresRepository)(nil)
