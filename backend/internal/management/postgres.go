package management

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	corepostgres "github.com/calmshv-star/ocrypt/backend/internal/adapters/postgres"
	"github.com/calmshv-star/ocrypt/backend/internal/ids"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresRepository struct {
	pool        *pgxpool.Pool
	responseBox SecretBox
	webhookBox  SecretBox
	core        *corepostgres.Store
	now         func() time.Time
}

func NewPostgresRepository(pool *pgxpool.Pool, responseBox, webhookBox SecretBox, core *corepostgres.Store) (*PostgresRepository, error) {
	if pool == nil || responseBox == nil || webhookBox == nil || core == nil {
		return nil, errors.New("management PostgreSQL, core payment boundary, and purpose-specific secret boxes are required")
	}
	return &PostgresRepository{pool: pool, responseBox: responseBox, webhookBox: webhookBox, core: core, now: func() time.Time { return time.Now().UTC() }}, nil
}

func (s *PostgresRepository) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := s.pool.Ping(ctx); err != nil {
		return fmt.Errorf("management PostgreSQL: %w", err)
	}
	var ready bool
	if err := s.pool.QueryRow(ctx, `SELECT
  to_regclass('public.payment_links') IS NOT NULL
  AND to_regclass('public.management_idempotency_records') IS NOT NULL
  AND to_regclass('public.hosted_payment_link_jobs') IS NOT NULL
  AND to_regclass('public.hosted_payment_link_incidents') IS NOT NULL
  AND to_regclass('public.payment_match_aggregates') IS NOT NULL
  AND to_regclass('public.payment_receipt_evidence') IS NOT NULL
  AND to_regclass('public.payment_route_policy_bindings') IS NOT NULL
  AND to_regprocedure('public.lookup_payment_link(bytea)') IS NOT NULL
  AND to_regprocedure('public.admit_hosted_provider_operation(uuid,uuid,text,text,timestamp with time zone)') IS NOT NULL
  AND has_table_privilege(current_user,'public.hosted_payment_link_jobs','SELECT,INSERT')
  AND has_table_privilege(current_user,'public.hosted_provider_create_attempts','SELECT,INSERT')
  AND has_table_privilege(current_user,'public.payment_match_aggregates','SELECT')
  AND has_table_privilege(current_user,'public.payment_route_policy_bindings','SELECT')
  AND has_table_privilege(current_user,'public.merchants','SELECT')
  AND has_table_privilege(current_user,'public.payment_matches','SELECT')
  AND has_table_privilege(current_user,'public.payment_receipt_evidence','SELECT,INSERT')
  AND has_table_privilege(current_user,'public.transfer_events','SELECT')
  AND has_function_privilege(current_user,'public.admit_hosted_provider_operation(uuid,uuid,text,text,timestamp with time zone)','EXECUTE')`).Scan(&ready); err != nil || !ready {
		return errors.New("management PostgreSQL migrations through 000019 or hosted payment-link grants are not ready")
	}
	return nil
}

func (s *PostgresRepository) withinTenant(ctx context.Context, tenantID string, fn func(pgx.Tx) error) error {
	if tenantID == "" {
		return ErrUnauthenticated
	}
	return pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id',$1,true)`, tenantID); err != nil {
			return err
		}
		return fn(tx)
	})
}

func (s *PostgresRepository) ConsumeManagementAssertion(ctx context.Context, jti, tenantID string, expires time.Time) (bool, error) {
	var consumed bool
	err := s.pool.QueryRow(ctx, `SELECT consume_management_assertion($1,$2,$3)`, jti, tenantID, expires).Scan(&consumed)
	return consumed, err
}

type managementReplay struct {
	Body       []byte
	ResourceID string
}

func (s *PostgresRepository) replay(ctx context.Context, tx pgx.Tx, p Principal, operation string, idem Idempotency, output any) (bool, error) {
	if len(idem.Key) < 8 {
		return false, ErrInvalid
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, p.MerchantID+"\x1f"+operation+"\x1f"+idem.Key); err != nil {
		return false, err
	}
	var storedHash, encrypted, responseHash []byte
	err := tx.QueryRow(ctx, `SELECT request_hash,encrypted_response,response_hash FROM management_idempotency_records WHERE merchant_id=$1 AND operation=$2 AND idempotency_key=$3 AND expires_at>clock_timestamp() FOR UPDATE`, p.MerchantID, operation, idem.Key).Scan(&storedHash, &encrypted, &responseHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !bytes.Equal(storedHash, idem.Fingerprint[:]) {
		return false, ErrIdempotencyConflict
	}
	body, err := s.responseBox.Open(ctx, encrypted)
	if err != nil {
		return false, ErrDependency
	}
	digest := sha256.Sum256(body)
	if !bytes.Equal(digest[:], responseHash) {
		return false, ErrDependency
	}
	if err := json.Unmarshal(body, output); err != nil {
		return false, ErrDependency
	}
	return true, nil
}

func (s *PostgresRepository) remember(ctx context.Context, tx pgx.Tx, p Principal, operation string, idem Idempotency, resourceType, resourceID string, status int, output any) error {
	body, err := json.Marshal(output)
	if err != nil {
		return err
	}
	encrypted, err := s.responseBox.Seal(ctx, body)
	if err != nil {
		return ErrDependency
	}
	digest := sha256.Sum256(body)
	_, err = tx.Exec(ctx, `INSERT INTO management_idempotency_records(tenant_id,merchant_id,operation,idempotency_key,request_hash,resource_type,resource_id,response_status,encrypted_response,response_hash,created_at,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, p.TenantID, p.MerchantID, operation, idem.Key, idem.Fingerprint[:], resourceType, resourceID, status, encrypted, digest[:], s.now(), s.now().Add(24*time.Hour))
	return classifyManagement(err)
}

func (s *PostgresRepository) auditOutbox(ctx context.Context, tx pgx.Tx, p Principal, action, resourceType, resourceID, reason string, version int64, details any) error {
	encoded, err := json.Marshal(details)
	if err != nil {
		return err
	}
	if len(encoded) > 16_384 {
		return ErrInvalid
	}
	auditID, err := ids.New()
	if err != nil {
		return err
	}
	var approval any
	if p.ApprovalActor != "" {
		approval = p.ApprovalActor
	}
	now := s.now()
	if _, err = tx.Exec(ctx, `SELECT append_management_audit($1,$2,$3,$4,$5,NULLIF($6,'')::uuid,$7,$8,$9,NULLIF($10,''),$11::jsonb,$12)`, auditID, p.TenantID, p.MerchantID, p.ActorID, p.SessionID, p.ApprovalActor, action, resourceType, resourceID, reason, encoded, now); err != nil {
		return err
	}
	outboxID, err := ids.New()
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]any{"event_id": outboxID, "event_type": "management." + action, "resource_type": resourceType, "resource_id": resourceID, "version": version, "occurred_at": now, "details": json.RawMessage(encoded)})
	_, err = tx.Exec(ctx, `INSERT INTO outbox_events(id,tenant_id,merchant_id,aggregate_type,aggregate_id,aggregate_version,aggregate_sequence,event_type,schema_version,payload,correlation_id,occurred_at,recorded_at,available_at) VALUES($1,$2,$3,$4,$5,$6,$6,$7,'1',$8::jsonb,$9,$10,$10,$10)`, outboxID, p.TenantID, p.MerchantID, "management."+resourceType, resourceID, version, "management."+action, payload, auditID, now)
	_ = approval
	return classifyManagement(err)
}

func classifyManagement(err error) error {
	if err == nil {
		return nil
	}
	var pgerr *pgconn.PgError
	if errors.As(err, &pgerr) {
		switch pgerr.Code {
		case "23505", "23P01":
			return fmt.Errorf("%w: %s", ErrConflict, pgerr.ConstraintName)
		case "23503", "23514", "22P02", "22001", "22023":
			return fmt.Errorf("%w: database constraint", ErrInvalid)
		case "40001", "40P01":
			return fmt.Errorf("%w: retry transaction", ErrConflict)
		}
	}
	return err
}

var _ AssertionReplayStore = (*PostgresRepository)(nil)
