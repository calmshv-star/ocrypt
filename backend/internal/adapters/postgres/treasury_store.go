package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/money"
	"github.com/calmshv-star/ocrypt/backend/internal/treasury"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrFinancialFenceLost = errors.New("financial operation lease fence lost")

type financialFenceKey struct{}

type FinancialFence struct {
	TenantID, AggregateType, AggregateID, OwnerID string
	Token                                         int64
}

func WithFinancialFence(ctx context.Context, fence FinancialFence) context.Context {
	return context.WithValue(ctx, financialFenceKey{}, fence)
}

// AcquireFinancialLease returns a monotonically increasing token. A worker
// must carry the returned fence in its context; store updates then reject stale
// owners even if their network call completes after the lease was reassigned.
func AcquireFinancialLease(ctx context.Context, pool *pgxpool.Pool, tenantID, aggregateType, aggregateID, ownerID string, ttl time.Duration) (FinancialFence, bool, error) {
	if pool == nil || tenantID == "" || aggregateID == "" || ownerID == "" || ttl < 5*time.Second || ttl > 15*time.Minute {
		return FinancialFence{}, false, errors.New("invalid financial lease request")
	}
	db, err := NewDatabase(pool)
	if err != nil {
		return FinancialFence{}, false, err
	}
	var fence FinancialFence
	var acquired bool
	err = db.WithinTenant(ctx, tenantID, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `INSERT INTO financial_work_leases
(tenant_id,aggregate_type,aggregate_id,owner_id,fencing_token,lease_until,updated_at)
VALUES ($1,$2,$3,$4,1,clock_timestamp()+$5::interval,clock_timestamp())
ON CONFLICT (tenant_id,aggregate_type,aggregate_id) DO UPDATE SET
 owner_id=EXCLUDED.owner_id,
 fencing_token=financial_work_leases.fencing_token+1,
 lease_until=EXCLUDED.lease_until,
 updated_at=clock_timestamp()
WHERE financial_work_leases.lease_until < clock_timestamp()
   OR financial_work_leases.owner_id=EXCLUDED.owner_id
RETURNING owner_id,fencing_token`, tenantID, aggregateType, aggregateID, ownerID, intervalLiteral(ttl)).Scan(&fence.OwnerID, &fence.Token)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("acquire financial lease: %w", err)
		}
		fence.TenantID, fence.AggregateType, fence.AggregateID = tenantID, aggregateType, aggregateID
		acquired = true
		return nil
	})
	return fence, acquired, err
}

func intervalLiteral(duration time.Duration) string {
	return fmt.Sprintf("%d milliseconds", duration.Milliseconds())
}

type TreasuryStore struct{ db *Database }

type FinancialProxyNonceStore struct{ pool *pgxpool.Pool }

type FinancialOutboxEvent struct {
	ID, TenantID, AggregateType, AggregateID, EventType string
	Payload                                             json.RawMessage
	OccurredAt                                          time.Time
	LeaseToken                                          int64
	AttemptCount                                        int
}

func NewFinancialProxyNonceStore(pool *pgxpool.Pool) (*FinancialProxyNonceStore, error) {
	if pool == nil {
		return nil, errors.New("PostgreSQL pool is required")
	}
	return &FinancialProxyNonceStore{pool: pool}, nil
}

func (s *FinancialProxyNonceStore) Consume(ctx context.Context, keyID, nonce string, expiresAt time.Time) (bool, error) {
	command, err := s.pool.Exec(ctx, `INSERT INTO financial_proxy_nonces
(key_id,nonce,expires_at,created_at) VALUES ($1,$2,$3,clock_timestamp())
ON CONFLICT (key_id,nonce) DO NOTHING`, keyID, nonce, expiresAt.UTC())
	if err != nil {
		return false, fmt.Errorf("consume financial proxy nonce: %w", err)
	}
	return command.RowsAffected() == 1, nil
}

func (s *FinancialProxyNonceStore) DeleteExpired(ctx context.Context, limit int) (int64, error) {
	if limit < 1 || limit > 100000 {
		return 0, errors.New("nonce cleanup limit must be in [1,100000]")
	}
	command, err := s.pool.Exec(ctx, `DELETE FROM financial_proxy_nonces WHERE (key_id,nonce) IN
(SELECT key_id,nonce FROM financial_proxy_nonces WHERE expires_at<clock_timestamp() ORDER BY expires_at LIMIT $1)`, limit)
	if err != nil {
		return 0, err
	}
	return command.RowsAffected(), nil
}

func LeaseFinancialOutbox(ctx context.Context, pool *pgxpool.Pool, tenantID, ownerID string, limit int, ttl time.Duration) ([]FinancialOutboxEvent, error) {
	if pool == nil || tenantID == "" || ownerID == "" || limit < 1 || limit > 100 || ttl < 5*time.Second || ttl > 5*time.Minute {
		return nil, errors.New("invalid financial outbox lease")
	}
	db, err := NewDatabase(pool)
	if err != nil {
		return nil, err
	}
	items := make([]FinancialOutboxEvent, 0, limit)
	err = db.WithinTenant(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `WITH candidates AS (
 SELECT id FROM financial_outbox
 WHERE tenant_id=$1 AND published_at IS NULL AND dead_lettered_at IS NULL
   AND available_at<=clock_timestamp() AND (lease_until IS NULL OR lease_until<clock_timestamp())
 ORDER BY available_at,id FOR UPDATE SKIP LOCKED LIMIT $3
)
UPDATE financial_outbox o SET lease_owner=$2,lease_token=o.lease_token+1,
 lease_until=clock_timestamp()+$4::interval,attempt_count=o.attempt_count+1
FROM candidates c WHERE o.id=c.id
RETURNING o.id::text,o.tenant_id::text,o.aggregate_type,o.aggregate_id::text,o.event_type,
 o.payload,o.occurred_at,o.lease_token,o.attempt_count`, tenantID, ownerID, limit, intervalLiteral(ttl))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item FinancialOutboxEvent
			if err := rows.Scan(&item.ID, &item.TenantID, &item.AggregateType, &item.AggregateID, &item.EventType, &item.Payload, &item.OccurredAt, &item.LeaseToken, &item.AttemptCount); err != nil {
				return err
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	return items, err
}

func CompleteFinancialOutbox(ctx context.Context, pool *pgxpool.Pool, event FinancialOutboxEvent, ownerID string) error {
	db, err := NewDatabase(pool)
	if err != nil {
		return err
	}
	return db.WithinTenant(ctx, event.TenantID, func(tx pgx.Tx) error {
		command, err := tx.Exec(ctx, `UPDATE financial_outbox SET published_at=clock_timestamp(),lease_until=NULL,last_error=NULL
WHERE tenant_id=$1 AND id=$2 AND lease_owner=$3 AND lease_token=$4 AND lease_until>clock_timestamp()
 AND published_at IS NULL AND dead_lettered_at IS NULL`, event.TenantID, event.ID, ownerID, event.LeaseToken)
		if err != nil {
			return err
		}
		if command.RowsAffected() != 1 {
			return ErrFinancialFenceLost
		}
		return nil
	})
}

func RetryFinancialOutbox(ctx context.Context, pool *pgxpool.Pool, event FinancialOutboxEvent, ownerID, failure string, delay time.Duration) error {
	if delay < time.Second || delay > time.Hour {
		return errors.New("invalid financial outbox retry delay")
	}
	if len(failure) > 2048 {
		failure = failure[:2048]
	}
	db, err := NewDatabase(pool)
	if err != nil {
		return err
	}
	return db.WithinTenant(ctx, event.TenantID, func(tx pgx.Tx) error {
		command, err := tx.Exec(ctx, `UPDATE financial_outbox SET
 available_at=clock_timestamp()+$5::interval,lease_until=NULL,last_error=$6,
 dead_lettered_at=CASE WHEN attempt_count>=20 THEN clock_timestamp() ELSE NULL END
WHERE tenant_id=$1 AND id=$2 AND lease_owner=$3 AND lease_token=$4 AND published_at IS NULL`, event.TenantID, event.ID, ownerID, event.LeaseToken, intervalLiteral(delay), failure)
		if err != nil {
			return err
		}
		if command.RowsAffected() != 1 {
			return ErrFinancialFenceLost
		}
		return nil
	})
}

func NewTreasuryStore(pool *pgxpool.Pool) (*TreasuryStore, error) {
	db, err := NewDatabase(pool)
	if err != nil {
		return nil, err
	}
	return &TreasuryStore{db: db}, nil
}

func (s *TreasuryStore) ActivePolicy(ctx context.Context, tenantID treasury.TenantID, assetID treasury.AssetID) (treasury.Policy, error) {
	var policy treasury.Policy
	err := s.db.WithinTenant(ctx, string(tenantID), func(tx pgx.Tx) error {
		var encoded []byte
		err := tx.QueryRow(ctx, `SELECT policy FROM financial_treasury_policies
WHERE tenant_id=$1 AND asset_id=$2 AND enabled AND NOT emergency_paused
  AND active_from<=clock_timestamp() AND (active_until IS NULL OR active_until>clock_timestamp())
ORDER BY version DESC LIMIT 1`, tenantID, assetID).Scan(&encoded)
		if err != nil {
			return err
		}
		return json.Unmarshal(encoded, &policy)
	})
	return policy, err
}

func (s *TreasuryStore) Create(ctx context.Context, mutation treasury.CreateMutation) (result treasury.SweepRequest, created bool, err error) {
	r := mutation.Request
	if r.TenantID == "" || r.ID == "" || r.AssetID == "" || r.Version != 1 || r.RequestHash == "" || mutation.Audit.TenantID != r.TenantID {
		return result, false, treasury.ErrValidation
	}
	err = s.db.WithinTenant(ctx, string(r.TenantID), func(tx pgx.Tx) error {
		if err := lockFinancialIdempotency(ctx, tx, string(r.TenantID), "sweep", r.IdempotencyKey); err != nil {
			return err
		}
		var existingHash string
		var existingJSON []byte
		err := tx.QueryRow(ctx, `SELECT request_hash,aggregate FROM financial_sweep_requests
WHERE tenant_id=$1 AND idempotency_key=$2`, r.TenantID, r.IdempotencyKey).Scan(&existingHash, &existingJSON)
		if err == nil {
			if existingHash != r.RequestHash {
				return treasury.ErrIdempotencyConflict
			}
			if err := json.Unmarshal(existingJSON, &result); err != nil {
				return err
			}
			created = false
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err := reserveFinancialUsage(ctx, tx, string(r.TenantID), string(r.AssetID), "sweep", mutation.Limits.WindowStart, mutation.Limits.WindowEnd, r.Amount, mutation.Limits.Maximum); err != nil {
			return fmt.Errorf("%w: %v", treasury.ErrPolicyLimit, err)
		}
		encoded, err := json.Marshal(r)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO financial_sweep_requests
(id,tenant_id,asset_id,chain_id,policy_id,policy_version,creator_id,idempotency_key,request_hash,status,amount_atomic,aggregate,version,created_at,updated_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11::uint256,$12::jsonb,$13,$14,$15)`,
			r.ID, r.TenantID, r.AssetID, r.ChainID, r.PolicyID, r.PolicyVersion, r.CreatorID, r.IdempotencyKey, r.RequestHash, r.Status, r.Amount.String(), encoded, r.Version, r.CreatedAt, r.UpdatedAt)
		if err != nil {
			return classifyTreasury(err)
		}
		for _, source := range mutation.Sources {
			if source.Address.Chain != r.ChainID || strings.TrimSpace(source.NonceRef) == "" {
				return treasury.ErrValidation
			}
			_, err = tx.Exec(ctx, `INSERT INTO financial_sweep_source_reservations
(tenant_id,sweep_id,chain_id,source_address,nonce_ref,reserved_at) VALUES ($1,$2,$3,$4,$5,$6)`,
				r.TenantID, r.ID, source.Address.Chain, source.Address.Value, source.NonceRef, r.CreatedAt)
			if err != nil {
				return classifyTreasury(err)
			}
		}
		if err := insertTreasuryArtifacts(ctx, tx, "sweep", mutation.Audit, mutation.Ledger, mutation.Outbox); err != nil {
			return err
		}
		result, created = r, true
		return nil
	})
	return result, created, err
}

func (s *TreasuryStore) Get(ctx context.Context, tenantID treasury.TenantID, requestID treasury.RequestID) (treasury.SweepRequest, error) {
	var result treasury.SweepRequest
	err := s.db.WithinTenant(ctx, string(tenantID), func(tx pgx.Tx) error {
		var encoded []byte
		if err := tx.QueryRow(ctx, `SELECT aggregate FROM financial_sweep_requests WHERE tenant_id=$1 AND id=$2`, tenantID, requestID).Scan(&encoded); err != nil {
			return err
		}
		return json.Unmarshal(encoded, &result)
	})
	return result, err
}

func (s *TreasuryStore) List(ctx context.Context, tenantID treasury.TenantID, after string, limit int) ([]treasury.SweepRequest, error) {
	if limit < 1 || limit > 200 {
		return nil, treasury.ErrValidation
	}
	items := make([]treasury.SweepRequest, 0, limit)
	err := s.db.WithinTenant(ctx, string(tenantID), func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT aggregate FROM financial_sweep_requests
WHERE tenant_id=$1 AND ($2='' OR id<$2::uuid) ORDER BY id DESC LIMIT $3`, tenantID, after, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var encoded []byte
			var item treasury.SweepRequest
			if err := rows.Scan(&encoded); err != nil {
				return err
			}
			if err := json.Unmarshal(encoded, &item); err != nil {
				return err
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	return items, err
}

func (s *TreasuryStore) ListExecutable(ctx context.Context, tenantID treasury.TenantID, limit int) ([]treasury.SweepRequest, error) {
	if limit < 1 || limit > 200 {
		return nil, treasury.ErrValidation
	}
	items := make([]treasury.SweepRequest, 0, limit)
	err := s.db.WithinTenant(ctx, string(tenantID), func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT aggregate FROM financial_sweep_requests
WHERE tenant_id=$1 AND status IN ('approved','awaiting_signature','signed') ORDER BY id LIMIT $2`, tenantID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var encoded []byte
			var item treasury.SweepRequest
			if err := rows.Scan(&encoded); err != nil {
				return err
			}
			if err := json.Unmarshal(encoded, &item); err != nil {
				return err
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	return items, err
}

func (s *TreasuryStore) ListAwaitingFinality(ctx context.Context, tenantID treasury.TenantID, limit int) ([]treasury.SweepRequest, error) {
	if limit < 1 || limit > 200 {
		return nil, treasury.ErrValidation
	}
	items := make([]treasury.SweepRequest, 0, limit)
	err := s.db.WithinTenant(ctx, string(tenantID), func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT aggregate FROM financial_sweep_requests WHERE tenant_id=$1 AND status IN ('broadcast','confirmed','finalized') ORDER BY updated_at,id LIMIT $2`, tenantID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var b []byte
			var item treasury.SweepRequest
			if err := rows.Scan(&b); err != nil {
				return err
			}
			if err := json.Unmarshal(b, &item); err != nil {
				return err
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	return items, err
}

func (s *TreasuryStore) Update(ctx context.Context, mutation treasury.UpdateMutation) (result treasury.SweepRequest, err error) {
	if mutation.TenantID == "" || mutation.RequestID == "" || mutation.ExpectedVersion < 1 || mutation.Next.Version != mutation.ExpectedVersion+1 || mutation.Next.TenantID != mutation.TenantID || mutation.Next.ID != mutation.RequestID {
		return result, treasury.ErrValidation
	}
	err = s.db.WithinTenant(ctx, string(mutation.TenantID), func(tx pgx.Tx) error {
		if mutation.DecisionOperation != "" {
			replayed, err := financialDecisionReplay(ctx, tx, string(mutation.TenantID), string(mutation.DecisionActor), mutation.DecisionOperation, mutation.DecisionKey, mutation.DecisionFingerprint, &result, treasury.ErrIdempotencyConflict)
			if err != nil || replayed {
				return err
			}
		}
		if err := checkFinancialFence(ctx, tx, string(mutation.TenantID), "sweep", string(mutation.RequestID)); err != nil {
			return err
		}
		var currentJSON []byte
		var currentVersion int64
		if err := tx.QueryRow(ctx, `SELECT aggregate,version FROM financial_sweep_requests
WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, mutation.TenantID, mutation.RequestID).Scan(&currentJSON, &currentVersion); err != nil {
			return err
		}
		if currentVersion != mutation.ExpectedVersion {
			return treasury.ErrVersionConflict
		}
		encoded, err := json.Marshal(mutation.Next)
		if err != nil {
			return err
		}
		command, err := tx.Exec(ctx, `UPDATE financial_sweep_requests SET
status=$3,aggregate=$4::jsonb,version=$5,updated_at=$6
WHERE tenant_id=$1 AND id=$2 AND version=$7`, mutation.TenantID, mutation.RequestID, mutation.Next.Status, encoded, mutation.Next.Version, mutation.Next.UpdatedAt, mutation.ExpectedVersion)
		if err != nil {
			return classifyTreasury(err)
		}
		if command.RowsAffected() != 1 {
			return treasury.ErrVersionConflict
		}
		for _, source := range mutation.ReleaseSources {
			command, err = tx.Exec(ctx, `UPDATE financial_sweep_source_reservations SET released_at=$6
WHERE tenant_id=$1 AND sweep_id=$2 AND chain_id=$3 AND source_address=$4 AND nonce_ref=$5 AND released_at IS NULL`,
				mutation.TenantID, mutation.RequestID, source.Address.Chain, source.Address.Value, source.NonceRef, mutation.Next.UpdatedAt)
			if err != nil || command.RowsAffected() != 1 {
				if err != nil {
					return err
				}
				return treasury.ErrStateConflict
			}
		}
		if err := insertTreasuryArtifacts(ctx, tx, "sweep", mutation.Audit, mutation.Ledger, mutation.Outbox); err != nil {
			return err
		}
		result = mutation.Next
		if mutation.DecisionOperation != "" {
			if err := storeFinancialDecision(ctx, tx, string(mutation.TenantID), string(mutation.DecisionActor), mutation.DecisionOperation, mutation.DecisionKey, mutation.DecisionFingerprint, result, mutation.Next.UpdatedAt); err != nil {
				return err
			}
		}
		return nil
	})
	return result, err
}

func (s *TreasuryStore) ReplayDecision(ctx context.Context, tenantID treasury.TenantID, actorID treasury.ActorID, operation, key string, fingerprint [32]byte) (result treasury.SweepRequest, replayed bool, err error) {
	err = s.db.WithinTenant(ctx, string(tenantID), func(tx pgx.Tx) error {
		var inner error
		replayed, inner = financialDecisionReplay(ctx, tx, string(tenantID), string(actorID), operation, key, fingerprint, &result, treasury.ErrIdempotencyConflict)
		return inner
	})
	return result, replayed, err
}

func lockFinancialIdempotency(ctx context.Context, tx pgx.Tx, tenantID, kind, key string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, tenantID+"\x1f"+kind+"\x1f"+key)
	return err
}

func reserveFinancialUsage(ctx context.Context, tx pgx.Tx, tenantID, assetID, operation string, start, end time.Time, amount, maximum money.Amount) error {
	if start.IsZero() || !end.After(start) || amount.IsZero() || maximum.IsZero() {
		return errors.New("invalid usage window")
	}
	_, err := tx.Exec(ctx, `INSERT INTO financial_usage_buckets
(tenant_id,asset_id,operation,window_start,window_end,amount_atomic,updated_at)
VALUES ($1,$2,$3,$4,$5,0,clock_timestamp()) ON CONFLICT DO NOTHING`, tenantID, assetID, operation, start.UTC(), end.UTC())
	if err != nil {
		return err
	}
	var currentText string
	if err := tx.QueryRow(ctx, `SELECT amount_atomic::text FROM financial_usage_buckets
WHERE tenant_id=$1 AND asset_id=$2 AND operation=$3 AND window_start=$4 FOR UPDATE`, tenantID, assetID, operation, start.UTC()).Scan(&currentText); err != nil {
		return err
	}
	current, err := money.Parse(currentText)
	if err != nil {
		return err
	}
	next, err := current.Add(amount)
	if err != nil || next.Cmp(maximum) > 0 {
		return errors.New("usage limit exceeded")
	}
	_, err = tx.Exec(ctx, `UPDATE financial_usage_buckets SET amount_atomic=$5::uint256,updated_at=clock_timestamp(),window_end=$6
WHERE tenant_id=$1 AND asset_id=$2 AND operation=$3 AND window_start=$4`, tenantID, assetID, operation, start.UTC(), next.String(), end.UTC())
	return err
}

func checkFinancialFence(ctx context.Context, tx pgx.Tx, tenantID, aggregateType, aggregateID string) error {
	fence, ok := ctx.Value(financialFenceKey{}).(FinancialFence)
	if !ok {
		return nil
	}
	if fence.TenantID != tenantID || fence.AggregateType != aggregateType || fence.AggregateID != aggregateID {
		return ErrFinancialFenceLost
	}
	var owner string
	var token int64
	var active bool
	if err := tx.QueryRow(ctx, `SELECT owner_id,fencing_token,lease_until>clock_timestamp() FROM financial_work_leases
WHERE tenant_id=$1 AND aggregate_type=$2 AND aggregate_id=$3 FOR UPDATE`, tenantID, aggregateType, aggregateID).Scan(&owner, &token, &active); err != nil {
		return ErrFinancialFenceLost
	}
	if owner != fence.OwnerID || token != fence.Token || !active {
		return ErrFinancialFenceLost
	}
	return nil
}

func insertTreasuryArtifacts(ctx context.Context, tx pgx.Tx, aggregateType string, audit treasury.AuditCommand, ledger []treasury.LedgerCommand, outbox []treasury.OutboxCommand) error {
	if err := insertFinancialAudit(ctx, tx, audit.ID, string(audit.TenantID), aggregateType, audit.AggregateID, audit.ActorID, audit.Action, audit.Reason, audit.OccurredAt); err != nil {
		return err
	}
	for _, command := range ledger {
		if command.TenantID != audit.TenantID {
			return treasury.ErrValidation
		}
		if err := insertFinancialLedger(ctx, tx, command.ID, string(command.TenantID), string(command.AssetID), aggregateType, command.AggregateID, command.EntryType, command.DebitAccountID, command.CreditAccountID, command.Amount, command.OccurredAt); err != nil {
			return err
		}
	}
	for _, event := range outbox {
		if event.TenantID != audit.TenantID {
			return treasury.ErrValidation
		}
		if err := insertFinancialOutbox(ctx, tx, event.ID, string(event.TenantID), aggregateType, event.AggregateID, event.EventType, event.Payload, event.OccurredAt); err != nil {
			return err
		}
	}
	return nil
}

func insertFinancialAudit(ctx context.Context, tx pgx.Tx, id, tenantID, aggregateType, aggregateID, actorID, action, reason string, at time.Time) error {
	if id == "" || tenantID == "" || aggregateID == "" || actorID == "" || action == "" || strings.TrimSpace(reason) == "" {
		return errors.New("invalid financial audit command")
	}
	_, err := tx.Exec(ctx, `SELECT append_financial_audit($1,$2,$3,$4,$5,$6,$7,$8)`, id, tenantID, aggregateType, aggregateID, actorID, action, reason, at.UTC())
	return err
}

func insertFinancialLedger(ctx context.Context, tx pgx.Tx, id, tenantID, assetID, aggregateType, aggregateID, entryType, debit, credit string, amount money.Amount, at time.Time) error {
	if id == "" || tenantID == "" || assetID == "" || aggregateID == "" || entryType == "" || debit == "" || credit == "" || debit == credit || amount.IsZero() {
		return errors.New("invalid balanced ledger command")
	}
	_, err := tx.Exec(ctx, `INSERT INTO financial_ledger_transactions
(id,tenant_id,asset_id,entry_type,aggregate_type,aggregate_id,occurred_at,created_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,clock_timestamp())`, id, tenantID, assetID, entryType, aggregateType, aggregateID, at.UTC())
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO financial_ledger_legs
(transaction_id,tenant_id,asset_id,sequence,direction,account_code,amount_atomic,created_at)
VALUES ($1,$2,$3,1,'debit',$4,$6::uint256,clock_timestamp()),
       ($1,$2,$3,2,'credit',$5,$6::uint256,clock_timestamp())`, id, tenantID, assetID, debit, credit, amount.String())
	return err
}

func insertFinancialOutbox(ctx context.Context, tx pgx.Tx, id, tenantID, aggregateType, aggregateID, eventType string, payload []byte, at time.Time) error {
	if id == "" || tenantID == "" || aggregateID == "" || eventType == "" || !json.Valid(payload) {
		return errors.New("invalid financial outbox command")
	}
	_, err := tx.Exec(ctx, `INSERT INTO financial_outbox
(id,tenant_id,aggregate_type,aggregate_id,event_type,payload,occurred_at,available_at)
VALUES ($1,$2,$3,$4,$5,$6::jsonb,$7,$7)`, id, tenantID, aggregateType, aggregateID, eventType, payload, at.UTC())
	return err
}

func classifyTreasury(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return treasury.ErrStateConflict
	}
	return err
}
