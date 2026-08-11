package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/reconciliation"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type ReconciliationStore struct{ db *Database }

func NewReconciliationStore(pool *pgxpool.Pool) (*ReconciliationStore, error) {
	db, err := NewDatabase(pool)
	if err != nil {
		return nil, err
	}
	return &ReconciliationStore{db: db}, nil
}

func (s *ReconciliationStore) Create(ctx context.Context, mutation reconciliation.CreateMutation) (result reconciliation.Run, created bool, err error) {
	run := mutation.Run
	if run.TenantID == "" || run.ID == "" || run.Version != 1 || mutation.Audit.TenantID != run.TenantID {
		return result, false, reconciliation.ErrValidation
	}
	err = s.db.WithinTenant(ctx, string(run.TenantID), func(tx pgx.Tx) error {
		if err := lockFinancialIdempotency(ctx, tx, string(run.TenantID), "reconciliation", run.IdempotencyKey); err != nil {
			return err
		}
		var existingHash string
		var existingJSON []byte
		err := tx.QueryRow(ctx, `SELECT request_hash,aggregate FROM financial_reconciliation_runs
WHERE tenant_id=$1 AND idempotency_key=$2`, run.TenantID, run.IdempotencyKey).Scan(&existingHash, &existingJSON)
		if err == nil {
			if existingHash != run.RequestHash {
				return reconciliation.ErrIdempotencyConflict
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
		encoded, err := json.Marshal(run)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO financial_reconciliation_runs
(id,tenant_id,idempotency_key,request_hash,status,aggregate,report_digest,version,created_at,updated_at)
VALUES ($1,$2,$3,$4,$5,$6::jsonb,NULL,$7,$8,$9)`, run.ID, run.TenantID, run.IdempotencyKey, run.RequestHash, run.Status, encoded, run.Version, run.CreatedAt, run.UpdatedAt)
		if err != nil {
			return classifyReconciliation(err)
		}
		if err := insertReconciliationArtifacts(ctx, tx, mutation.Audit, mutation.Outbox); err != nil {
			return err
		}
		result, created = run, true
		return nil
	})
	return result, created, err
}

func (s *ReconciliationStore) Get(ctx context.Context, tenantID reconciliation.TenantID, runID reconciliation.RunID) (reconciliation.Run, error) {
	var result reconciliation.Run
	err := s.db.WithinTenant(ctx, string(tenantID), func(tx pgx.Tx) error {
		var encoded []byte
		if err := tx.QueryRow(ctx, `SELECT aggregate FROM financial_reconciliation_runs WHERE tenant_id=$1 AND id=$2`, tenantID, runID).Scan(&encoded); err != nil {
			return err
		}
		return json.Unmarshal(encoded, &result)
	})
	return result, err
}

func (s *ReconciliationStore) List(ctx context.Context, tenantID reconciliation.TenantID, after string, limit int) ([]reconciliation.Run, error) {
	if limit < 1 || limit > 200 {
		return nil, reconciliation.ErrValidation
	}
	items := make([]reconciliation.Run, 0, limit)
	err := s.db.WithinTenant(ctx, string(tenantID), func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT aggregate FROM financial_reconciliation_runs
WHERE tenant_id=$1 AND ($2='' OR id<$2::uuid) ORDER BY id DESC LIMIT $3`, tenantID, after, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var encoded []byte
			var item reconciliation.Run
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

func (s *ReconciliationStore) Update(ctx context.Context, mutation reconciliation.UpdateMutation) (result reconciliation.Run, err error) {
	if mutation.TenantID == "" || mutation.RunID == "" || mutation.ExpectedVersion < 1 || mutation.Next.Version != mutation.ExpectedVersion+1 || mutation.Next.TenantID != mutation.TenantID || mutation.Next.ID != mutation.RunID {
		return result, reconciliation.ErrValidation
	}
	err = s.db.WithinTenant(ctx, string(mutation.TenantID), func(tx pgx.Tx) error {
		if mutation.DecisionOperation != "" {
			replayed, err := financialDecisionReplay(ctx, tx, string(mutation.TenantID), string(mutation.DecisionActor), mutation.DecisionOperation, mutation.DecisionKey, mutation.DecisionFingerprint, &result, reconciliation.ErrIdempotencyConflict)
			if err != nil || replayed {
				return err
			}
		}
		if err := checkFinancialFence(ctx, tx, string(mutation.TenantID), "reconciliation", string(mutation.RunID)); err != nil {
			return err
		}
		var currentVersion int64
		if err := tx.QueryRow(ctx, `SELECT version FROM financial_reconciliation_runs
WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, mutation.TenantID, mutation.RunID).Scan(&currentVersion); err != nil {
			return err
		}
		if currentVersion != mutation.ExpectedVersion {
			return reconciliation.ErrVersionConflict
		}
		encoded, err := json.Marshal(mutation.Next)
		if err != nil {
			return err
		}
		command, err := tx.Exec(ctx, `UPDATE financial_reconciliation_runs SET
status=$3,aggregate=$4::jsonb,report_digest=NULLIF($5,''),version=$6,updated_at=$7
WHERE tenant_id=$1 AND id=$2 AND version=$8`, mutation.TenantID, mutation.RunID, mutation.Next.Status, encoded, mutation.Next.ReportDigest, mutation.Next.Version, mutation.Next.UpdatedAt, mutation.ExpectedVersion)
		if err != nil {
			return classifyReconciliation(err)
		}
		if command.RowsAffected() != 1 {
			return reconciliation.ErrVersionConflict
		}
		if mutation.Next.Status == reconciliation.StatusCompleted {
			for _, item := range mutation.Next.Items {
				if item.TenantID != mutation.TenantID || item.RunID != mutation.RunID {
					return reconciliation.ErrValidation
				}
				payload, err := json.Marshal(item)
				if err != nil {
					return err
				}
				_, err = tx.Exec(ctx, `INSERT INTO financial_reconciliation_items
(tenant_id,run_id,asset_id,item) VALUES ($1,$2,$3,$4::jsonb)`, mutation.TenantID, mutation.RunID, item.AssetID, payload)
				if err != nil {
					return err
				}
			}
			for _, item := range mutation.Next.IntegrityItems {
				if item.TenantID != mutation.TenantID || item.RunID != mutation.RunID {
					return reconciliation.ErrValidation
				}
				payload, err := json.Marshal(item)
				if err != nil {
					return err
				}
				_, err = tx.Exec(ctx, `INSERT INTO financial_reconciliation_integrity_items
(tenant_id,run_id,asset_id,subject_id,item) VALUES ($1,$2,$3,$4,$5::jsonb)`, mutation.TenantID, mutation.RunID, item.AssetID, item.SubjectID, payload)
				if err != nil {
					return err
				}
			}
		}
		if err := insertReconciliationArtifacts(ctx, tx, mutation.Audit, mutation.Outbox); err != nil {
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

func (s *ReconciliationStore) ReplayDecision(ctx context.Context, tenantID reconciliation.TenantID, actorID reconciliation.ActorID, operation, key string, fingerprint [32]byte) (result reconciliation.Run, replayed bool, err error) {
	err = s.db.WithinTenant(ctx, string(tenantID), func(tx pgx.Tx) error {
		var inner error
		replayed, inner = financialDecisionReplay(ctx, tx, string(tenantID), string(actorID), operation, key, fingerprint, &result, reconciliation.ErrIdempotencyConflict)
		return inner
	})
	return result, replayed, err
}

func (s *ReconciliationStore) Snapshot(ctx context.Context, tenantID reconciliation.TenantID, assetID reconciliation.AssetID, cutoffAt time.Time) (reconciliation.BalanceSnapshot, error) {
	var result reconciliation.BalanceSnapshot
	err := s.db.WithinTenant(ctx, string(tenantID), func(tx pgx.Tx) error {
		var encoded []byte
		var digest string
		if err := tx.QueryRow(ctx, `SELECT snapshot,evidence_digest FROM financial_balance_snapshots
WHERE tenant_id=$1 AND asset_id=$2 AND cutoff_at=$3`, tenantID, assetID, cutoffAt.UTC()).Scan(&encoded, &digest); err != nil {
			return err
		}
		if err := json.Unmarshal(encoded, &result); err != nil {
			return err
		}
		if result.EvidenceDigest != digest {
			return reconciliation.ErrValidation
		}
		return nil
	})
	return result, err
}

func (s *ReconciliationStore) IntegritySnapshots(ctx context.Context, tenantID reconciliation.TenantID, assetID reconciliation.AssetID, cutoffAt time.Time) ([]reconciliation.IntegritySnapshot, error) {
	items := make([]reconciliation.IntegritySnapshot, 0)
	err := s.db.WithinTenant(ctx, string(tenantID), func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT snapshot,evidence_digest FROM financial_integrity_snapshots
WHERE tenant_id=$1 AND asset_id=$2 AND cutoff_at=$3 ORDER BY subject_id`, tenantID, assetID, cutoffAt.UTC())
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var encoded []byte
			var digest string
			var item reconciliation.IntegritySnapshot
			if err := rows.Scan(&encoded, &digest); err != nil {
				return err
			}
			if err := json.Unmarshal(encoded, &item); err != nil {
				return err
			}
			if item.EvidenceDigest != digest {
				return reconciliation.ErrValidation
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	return items, err
}

func insertReconciliationArtifacts(ctx context.Context, tx pgx.Tx, audit reconciliation.AuditCommand, outbox []reconciliation.OutboxCommand) error {
	if err := insertFinancialAudit(ctx, tx, audit.ID, string(audit.TenantID), "reconciliation", audit.AggregateID, string(audit.ActorID), audit.Action, audit.Reason, audit.OccurredAt); err != nil {
		return err
	}
	for _, event := range outbox {
		if event.TenantID != audit.TenantID {
			return reconciliation.ErrValidation
		}
		if err := insertFinancialOutbox(ctx, tx, event.ID, string(event.TenantID), "reconciliation", event.AggregateID, event.EventType, event.Payload, event.OccurredAt); err != nil {
			return err
		}
	}
	return nil
}

func classifyReconciliation(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return reconciliation.ErrIdempotencyConflict
	}
	return err
}
