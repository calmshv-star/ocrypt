package management

import (
	"bytes"
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

type actionScanner interface{ Scan(...any) error }

const managementActionColumns = `id::text,operation,resource_type,resource_id::text,resource_version,request_body,request_hash,mutation_idempotency_key,requested_by::text,requested_session,requested_step_up_at,request_reason,COALESCE(approved_by::text,''),COALESCE(approval_reason,''),COALESCE(approval_hash,''::bytea),status,COALESCE(failure_code,''),COALESCE(lease_token::text,''),lease_until,created_at,expires_at,approved_at,completed_at,updated_at,version`

func scanManagementAction(row actionScanner) (value ManagementActionRequest, err error) {
	var requestHash, approvalHash []byte
	err = row.Scan(&value.ID, &value.Operation, &value.ResourceType, &value.ResourceID, &value.ResourceVersion,
		&value.RequestBody, &requestHash, &value.MutationIdempotencyKey, &value.RequestedBy, &value.RequestedSession,
		&value.RequestedStepUpAt, &value.RequestReason, &value.ApprovedBy, &value.ApprovalReason, &approvalHash,
		&value.Status, &value.FailureCode, &value.LeaseToken, &value.LeaseUntil, &value.CreatedAt, &value.ExpiresAt,
		&value.ApprovedAt, &value.CompletedAt, &value.UpdatedAt, &value.Version)
	copy(value.RequestHash[:], requestHash)
	copy(value.ApprovalHash[:], approvalHash)
	return value, err
}

func (s *PostgresRepository) CreateManagementAction(ctx context.Context, p Principal, value ManagementActionRequest) (result ManagementActionRequest, replay bool, err error) {
	err = s.withinTenant(ctx, p.TenantID, func(tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, p.MerchantID+"\x1f"+value.Operation+"\x1f"+value.MutationIdempotencyKey); e != nil {
			return e
		}
		existing, e := scanManagementAction(tx.QueryRow(ctx, `SELECT `+managementActionColumns+` FROM management_action_requests WHERE merchant_id=$1 AND operation=$2 AND mutation_idempotency_key=$3 FOR UPDATE`, p.MerchantID, value.Operation, value.MutationIdempotencyKey))
		if e == nil {
			if !bytes.Equal(existing.RequestHash[:], value.RequestHash[:]) {
				return ErrIdempotencyConflict
			}
			result, replay = existing, true
			return nil
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		_, e = tx.Exec(ctx, `INSERT INTO management_action_requests(id,tenant_id,merchant_id,operation,resource_type,resource_id,resource_version,request_body,request_hash,mutation_idempotency_key,requested_by,requested_session,requested_step_up_at,request_reason,status,created_at,expires_at,updated_at,version) VALUES($1,$2,$3,$4,$5,$6,$7,$8::jsonb,$9,$10,$11,$12,$13,$14,'pending_approval',$15,$16,$15,1)`, value.ID, p.TenantID, p.MerchantID, value.Operation, value.ResourceType, value.ResourceID, value.ResourceVersion, value.RequestBody, value.RequestHash[:], value.MutationIdempotencyKey, p.ActorID, p.SessionID, value.RequestedStepUpAt, value.RequestReason, value.CreatedAt, value.ExpiresAt)
		if e != nil {
			return classifyManagement(e)
		}
		action := "webhook.disable_requested"
		if value.Operation == actionRevokeClient {
			action = "api_client.revoke_requested"
		}
		if e = s.auditOutbox(ctx, tx, p, action, value.ResourceType, value.ID, value.RequestReason, 1, map[string]any{"target_resource_id": value.ResourceID, "target_version": value.ResourceVersion, "request_hash": value.RequestHash}); e != nil {
			return e
		}
		result = value
		return nil
	})
	return result, replay, classifyManagement(err)
}

func (s *PostgresRepository) GetManagementAction(ctx context.Context, p Principal, operation, id string) (result ManagementActionRequest, err error) {
	err = s.withinTenant(ctx, p.TenantID, func(tx pgx.Tx) error {
		var e error
		result, e = scanManagementAction(tx.QueryRow(ctx, `SELECT `+managementActionColumns+` FROM management_action_requests WHERE id=$1 AND merchant_id=$2 AND operation=$3`, id, p.MerchantID, operation))
		if errors.Is(e, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return e
	})
	return result, classifyManagement(err)
}

func (s *PostgresRepository) ListManagementActions(ctx context.Context, p Principal, operation, cursor string, limit int) (result Page[ManagementActionRequest], err error) {
	err = s.withinTenant(ctx, p.TenantID, func(tx pgx.Tx) error {
		rows, e := tx.Query(ctx, `SELECT `+managementActionColumns+` FROM management_action_requests WHERE merchant_id=$1 AND operation=$2 AND ($3='' OR id<$3::uuid) ORDER BY id DESC LIMIT $4`, p.MerchantID, operation, cursor, limit+1)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			value, scanErr := scanManagementAction(rows)
			if scanErr != nil {
				return scanErr
			}
			result.Data = append(result.Data, value)
		}
		if e = rows.Err(); e != nil {
			return e
		}
		if len(result.Data) > limit {
			result.NextCursor = result.Data[limit-1].ID
			result.Data = result.Data[:limit]
		}
		return nil
	})
	return result, classifyManagement(err)
}

func (s *PostgresRepository) ClaimManagementAction(ctx context.Context, p Principal, operation, id, lease, reason string, decisionHash [32]byte, now time.Time) (result ManagementActionRequest, replay bool, err error) {
	err = s.withinTenant(ctx, p.TenantID, func(tx pgx.Tx) error {
		current, e := scanManagementAction(tx.QueryRow(ctx, `SELECT `+managementActionColumns+` FROM management_action_requests WHERE id=$1 AND merchant_id=$2 AND operation=$3 FOR UPDATE`, id, p.MerchantID, operation))
		if errors.Is(e, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if e != nil {
			return e
		}
		if current.RequestedBy == p.ActorID {
			return ErrForbidden
		}
		if current.Status == "completed" {
			if current.ApprovedBy != p.ActorID || current.ApprovalReason != reason || !bytes.Equal(current.ApprovalHash[:], decisionHash[:]) {
				return ErrConflict
			}
			result, replay = current, true
			return nil
		}
		if current.Status == "rejected" || current.Status == "failed" || !current.ExpiresAt.After(now) {
			return ErrConflict
		}
		if current.Status == "executing" && current.LeaseUntil != nil && current.LeaseUntil.After(now) {
			return ErrConflict
		}
		if current.ApprovedBy != "" && (current.ApprovedBy != p.ActorID || current.ApprovalReason != reason || !bytes.Equal(current.ApprovalHash[:], decisionHash[:])) {
			return ErrConflict
		}
		approvedAt := current.ApprovedAt
		if approvedAt == nil {
			approvedAt = &now
		}
		row := tx.QueryRow(ctx, `UPDATE management_action_requests SET approved_by=$4,approval_reason=$5,approval_hash=$6,status='executing',lease_token=$7,lease_until=$8,approved_at=$9,updated_at=$9,version=version+1 WHERE id=$1 AND merchant_id=$2 AND operation=$3 RETURNING `+managementActionColumns, id, p.MerchantID, operation, p.ActorID, reason, decisionHash[:], lease, now.Add(30*time.Second), approvedAt)
		result, e = scanManagementAction(row)
		return e
	})
	return result, replay, classifyManagement(err)
}

func (s *PostgresRepository) RejectManagementAction(ctx context.Context, p Principal, operation, id, reason string, decisionHash [32]byte, now time.Time) (result ManagementActionRequest, replay bool, err error) {
	err = s.withinTenant(ctx, p.TenantID, func(tx pgx.Tx) error {
		current, e := scanManagementAction(tx.QueryRow(ctx, `SELECT `+managementActionColumns+` FROM management_action_requests WHERE id=$1 AND merchant_id=$2 AND operation=$3 FOR UPDATE`, id, p.MerchantID, operation))
		if errors.Is(e, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if e != nil {
			return e
		}
		if current.RequestedBy == p.ActorID {
			return ErrForbidden
		}
		if current.Status == "rejected" {
			if current.ApprovedBy != p.ActorID || current.ApprovalReason != reason || !bytes.Equal(current.ApprovalHash[:], decisionHash[:]) {
				return ErrConflict
			}
			result, replay = current, true
			return nil
		}
		if current.Status != "pending_approval" || !current.ExpiresAt.After(now) {
			return ErrConflict
		}
		result, e = scanManagementAction(tx.QueryRow(ctx, `UPDATE management_action_requests SET approved_by=$4,approval_reason=$5,approval_hash=$6,status='rejected',approved_at=$7,completed_at=$7,updated_at=$7,version=version+1 WHERE id=$1 AND merchant_id=$2 AND operation=$3 RETURNING `+managementActionColumns, id, p.MerchantID, operation, p.ActorID, reason, decisionHash[:], now))
		if e != nil {
			return e
		}
		action := "webhook.disable_rejected"
		if operation == actionRevokeClient {
			action = "api_client.revoke_rejected"
		}
		return s.auditOutbox(ctx, tx, p, action, current.ResourceType, current.ID, reason, result.Version, map[string]any{"target_resource_id": current.ResourceID, "target_version": current.ResourceVersion, "request_hash": current.RequestHash, "approval_hash": decisionHash})
	})
	return result, replay, classifyManagement(err)
}

func (s *PostgresRepository) CompleteManagementAction(ctx context.Context, p Principal, id, lease string, succeeded bool, failureCode string, now time.Time) (result ManagementActionRequest, err error) {
	err = s.withinTenant(ctx, p.TenantID, func(tx pgx.Tx) error {
		status := "completed"
		if !succeeded {
			status = "failed"
		}
		var e error
		result, e = scanManagementAction(tx.QueryRow(ctx, `UPDATE management_action_requests SET status=$4,failure_code=NULLIF($5,''),lease_token=NULL,lease_until=NULL,completed_at=$6,updated_at=$6,version=version+1 WHERE id=$1 AND merchant_id=$2 AND lease_token=$3::uuid AND status='executing' RETURNING `+managementActionColumns, id, p.MerchantID, lease, status, failureCode, now))
		if errors.Is(e, pgx.ErrNoRows) {
			return ErrConflict
		}
		return e
	})
	return result, classifyManagement(err)
}
