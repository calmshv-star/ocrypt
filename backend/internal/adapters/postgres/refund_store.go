package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/calmshv-star/ocrypt/backend/internal/money"
	"github.com/calmshv-star/ocrypt/backend/internal/refunds"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type RefundStore struct{ db *Database }

func NewRefundStore(pool *pgxpool.Pool) (*RefundStore, error) {
	db, err := NewDatabase(pool)
	if err != nil {
		return nil, err
	}
	return &RefundStore{db: db}, nil
}

// SyncSettlements is an idempotent bridge from finalized core matches. A
// refund is never made refundable before both the match and chain event are
// finalized. Reversed/reorged evidence is placed on hold. Existing monetary
// evidence is never rewritten; any mismatch also creates a risk hold.
func (s *RefundStore) SyncSettlements(ctx context.Context, tenantID refunds.TenantID, limit int) (int64, error) {
	if tenantID == "" || limit < 1 || limit > 1000 {
		return 0, refunds.ErrValidation
	}
	var changed int64
	err := s.db.WithinTenant(ctx, string(tenantID), func(tx pgx.Tx) error {
		command, err := tx.Exec(ctx, `INSERT INTO financial_refund_settlements
(id,tenant_id,payment_match_id,asset_id,chain_id,intent_id,chain_event_id,observed_sender,
 received_amount_atomic,refunded_amount_atomic,finalized,risk_hold,evidence_digest,created_at,updated_at)
SELECT pm.id,pm.tenant_id,pm.id,te.asset_id,te.chain_id,pm.intent_id,te.id,te.from_address,
 pm.received_atomic,0,true,false,encode(te.evidence_hash,'hex'),clock_timestamp(),clock_timestamp()
FROM payment_matches pm JOIN transfer_events te ON te.id=pm.event_id
WHERE pm.tenant_id=$1 AND pm.state='finalized' AND te.status='finalized'
  AND pm.allocation_role='payment'
ORDER BY pm.created_at,pm.id LIMIT $2
ON CONFLICT (id) DO UPDATE SET
 finalized=financial_refund_settlements.finalized AND
   financial_refund_settlements.asset_id=EXCLUDED.asset_id AND financial_refund_settlements.chain_id=EXCLUDED.chain_id AND
   financial_refund_settlements.intent_id=EXCLUDED.intent_id AND financial_refund_settlements.chain_event_id=EXCLUDED.chain_event_id AND
   financial_refund_settlements.observed_sender=EXCLUDED.observed_sender AND
   financial_refund_settlements.received_amount_atomic=EXCLUDED.received_amount_atomic AND
   financial_refund_settlements.evidence_digest=EXCLUDED.evidence_digest,
 risk_hold=financial_refund_settlements.risk_hold OR NOT (
   financial_refund_settlements.asset_id=EXCLUDED.asset_id AND financial_refund_settlements.chain_id=EXCLUDED.chain_id AND
   financial_refund_settlements.intent_id=EXCLUDED.intent_id AND financial_refund_settlements.chain_event_id=EXCLUDED.chain_event_id AND
   financial_refund_settlements.observed_sender=EXCLUDED.observed_sender AND
   financial_refund_settlements.received_amount_atomic=EXCLUDED.received_amount_atomic AND
   financial_refund_settlements.evidence_digest=EXCLUDED.evidence_digest),
 updated_at=clock_timestamp()`, tenantID, limit)
		if err != nil {
			return err
		}
		changed += command.RowsAffected()
		command, err = tx.Exec(ctx, `UPDATE financial_refund_settlements s SET finalized=false,risk_hold=true,updated_at=clock_timestamp()
FROM payment_matches pm JOIN transfer_events te ON te.id=pm.event_id
WHERE s.tenant_id=$1 AND s.payment_match_id=pm.id AND s.chain_event_id=te.id
 AND (pm.state='reversed' OR te.status IN ('reorged','invalidated'))
 AND (s.finalized OR NOT s.risk_hold)`, tenantID)
		if err != nil {
			return err
		}
		changed += command.RowsAffected()
		_, err = tx.Exec(ctx, `UPDATE financial_verified_refund_destinations d SET revoked_at=COALESCE(revoked_at,clock_timestamp())
FROM financial_refund_settlements s WHERE d.tenant_id=$1 AND d.settlement_id=s.id AND s.risk_hold AND d.revoked_at IS NULL`, tenantID)
		if err != nil {
			return err
		}
		return nil
	})
	return changed, err
}

func (s *RefundStore) ActivePolicy(ctx context.Context, tenantID refunds.TenantID, assetID refunds.AssetID) (refunds.Policy, error) {
	var policy refunds.Policy
	err := s.db.WithinTenant(ctx, string(tenantID), func(tx pgx.Tx) error {
		var encoded []byte
		err := tx.QueryRow(ctx, `SELECT policy FROM financial_refund_policies
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

func (s *RefundStore) Settlement(ctx context.Context, tenantID refunds.TenantID, settlementID refunds.SettlementID) (refunds.Settlement, error) {
	var result refunds.Settlement
	var received, refunded string
	err := s.db.WithinTenant(ctx, string(tenantID), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT id::text,tenant_id::text,asset_id,chain_id,intent_id::text,chain_event_id::text,
observed_sender,received_amount_atomic::text,refunded_amount_atomic::text,finalized,risk_hold
FROM financial_refund_settlements WHERE tenant_id=$1 AND id=$2`, tenantID, settlementID).Scan(
			&result.ID, &result.TenantID, &result.AssetID, &result.ChainID, &result.IntentID, &result.ChainEventID,
			&result.ObservedSender.Value, &received, &refunded, &result.Finalized, &result.RiskHold)
	})
	if err != nil {
		return result, err
	}
	result.ObservedSender.Chain = result.ChainID
	result.ReceivedAmount, err = money.Parse(received)
	if err != nil {
		return result, err
	}
	result.AlreadyRefunded, err = money.Parse(refunded)
	return result, err
}

func (s *RefundStore) VerifiedDestination(ctx context.Context, tenantID refunds.TenantID, verificationID string) (refunds.VerifiedDestination, error) {
	var result refunds.VerifiedDestination
	err := s.db.WithinTenant(ctx, string(tenantID), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT id::text,tenant_id::text,settlement_id::text,asset_id,chain_id,address,method,
evidence_digest,verified_at,COALESCE(expires_at,'0001-01-01 UTC'::timestamptz),COALESCE(revoked_at,'0001-01-01 UTC'::timestamptz)
FROM financial_verified_refund_destinations WHERE tenant_id=$1 AND id=$2`, tenantID, verificationID).Scan(
			&result.ID, &result.TenantID, &result.SettlementID, &result.AssetID, &result.Address.Chain, &result.Address.Value,
			&result.Method, &result.EvidenceDigest, &result.VerifiedAt, &result.ExpiresAt, &result.RevokedAt)
	})
	return result, err
}

func (s *RefundStore) Create(ctx context.Context, mutation refunds.CreateMutation) (result refunds.Refund, created bool, err error) {
	r := mutation.Refund
	if r.TenantID == "" || r.ID == "" || r.SettlementID == "" || r.AssetID == "" || r.Version != 1 || mutation.Audit.TenantID != r.TenantID {
		return result, false, refunds.ErrValidation
	}
	err = s.db.WithinTenant(ctx, string(r.TenantID), func(tx pgx.Tx) error {
		if err := lockFinancialIdempotency(ctx, tx, string(r.TenantID), "refund", r.IdempotencyKey); err != nil {
			return err
		}
		var existingHash string
		var existingJSON []byte
		err := tx.QueryRow(ctx, `SELECT request_hash,aggregate FROM financial_refund_requests
WHERE tenant_id=$1 AND idempotency_key=$2`, r.TenantID, r.IdempotencyKey).Scan(&existingHash, &existingJSON)
		if err == nil {
			if existingHash != r.RequestHash {
				return refunds.ErrIdempotencyConflict
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

		// Lock the settlement and independently re-verify origin/ownership. This
		// closes the race between the evidence read and reservation commit.
		var settlementAsset, settlementChain, observedSender, receivedText, refundedText string
		var finalized, riskHold bool
		if err := tx.QueryRow(ctx, `SELECT asset_id,chain_id,observed_sender,received_amount_atomic::text,
refunded_amount_atomic::text,finalized,risk_hold FROM financial_refund_settlements
WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, r.TenantID, r.SettlementID).Scan(
			&settlementAsset, &settlementChain, &observedSender, &receivedText, &refundedText, &finalized, &riskHold); err != nil {
			return err
		}
		if settlementAsset != string(r.AssetID) || settlementChain != string(r.ChainID) || !finalized || riskHold {
			return refunds.ErrStateConflict
		}
		var coreMatchState, coreEventStatus string
		if err := tx.QueryRow(ctx, `SELECT pm.state::text,te.status::text FROM financial_refund_settlements s
JOIN payment_matches pm ON pm.id=s.payment_match_id AND pm.tenant_id=s.tenant_id
JOIN transfer_events te ON te.id=s.chain_event_id
WHERE s.tenant_id=$1 AND s.id=$2`, r.TenantID, r.SettlementID).Scan(&coreMatchState, &coreEventStatus); err != nil {
			return err
		}
		if coreMatchState != "finalized" || coreEventStatus != "finalized" {
			return refunds.ErrStateConflict
		}
		var verifiedAsset, verifiedChain, verifiedAddress, method string
		var usable bool
		if err := tx.QueryRow(ctx, `SELECT asset_id,chain_id,address,method,
(revoked_at IS NULL AND verified_at<=clock_timestamp() AND (expires_at IS NULL OR expires_at>clock_timestamp()))
FROM financial_verified_refund_destinations
WHERE tenant_id=$1 AND id=$2 AND settlement_id=$3`, r.TenantID, r.DestinationVerificationID, r.SettlementID).Scan(
			&verifiedAsset, &verifiedChain, &verifiedAddress, &method, &usable); err != nil {
			return err
		}
		if !usable || verifiedAsset != settlementAsset || verifiedChain != settlementChain || verifiedAddress != r.Destination.Value || method == string(refunds.VerificationObservedSender) {
			return refunds.ErrDestinationUnverified
		}
		var originOnly bool
		if err := tx.QueryRow(ctx, `SELECT refund_to_origin_only FROM financial_refund_policies
WHERE tenant_id=$1 AND id=$2 AND version=$3 AND enabled AND NOT emergency_paused`, r.TenantID, r.PolicyID, r.PolicyVersion).Scan(&originOnly); err != nil {
			return err
		}
		if originOnly && verifiedAddress != observedSender {
			return refunds.ErrDestinationUnverified
		}
		received, err := money.Parse(receivedText)
		if err != nil {
			return err
		}
		refunded, err := money.Parse(refundedText)
		if err != nil {
			return err
		}
		var reservedText string
		if err := tx.QueryRow(ctx, `SELECT COALESCE(sum(amount_atomic),0)::text FROM financial_refund_reservations
WHERE tenant_id=$1 AND settlement_id=$2 AND released_at IS NULL AND finalized_at IS NULL`, r.TenantID, r.SettlementID).Scan(&reservedText); err != nil {
			return err
		}
		reserved, err := money.Parse(reservedText)
		if err != nil {
			return err
		}
		used, err := refunded.Add(reserved)
		if err != nil {
			return refunds.ErrInsufficientRefundable
		}
		available, err := received.Sub(used)
		if err != nil || r.GrossAmount.Cmp(available) > 0 || r.GrossAmount.Cmp(mutation.MaximumRefundable) > 0 {
			return refunds.ErrInsufficientRefundable
		}
		if err := reserveFinancialUsage(ctx, tx, string(r.TenantID), string(r.AssetID), "refund", mutation.LimitWindowStart, mutation.LimitWindowEnd, r.GrossAmount, mutation.DailyLimit); err != nil {
			return refunds.ErrPolicyLimit
		}
		encoded, err := json.Marshal(r)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO financial_refund_requests
(id,tenant_id,settlement_id,asset_id,chain_id,policy_id,policy_version,creator_id,idempotency_key,request_hash,status,gross_amount_atomic,aggregate,version,created_at,updated_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12::uint256,$13::jsonb,$14,$15,$16)`,
			r.ID, r.TenantID, r.SettlementID, r.AssetID, r.ChainID, r.PolicyID, r.PolicyVersion, r.CreatorID, r.IdempotencyKey, r.RequestHash, r.Status, r.GrossAmount.String(), encoded, r.Version, r.CreatedAt, r.UpdatedAt)
		if err != nil {
			return classifyRefund(err)
		}
		_, err = tx.Exec(ctx, `INSERT INTO financial_refund_reservations
(tenant_id,settlement_id,refund_id,amount_atomic,reserved_at) VALUES ($1,$2,$3,$4::uint256,$5)`, r.TenantID, r.SettlementID, r.ID, r.GrossAmount.String(), r.CreatedAt)
		if err != nil {
			return classifyRefund(err)
		}
		if err := insertRefundArtifacts(ctx, tx, mutation.Audit, mutation.Ledger, mutation.Outbox); err != nil {
			return err
		}
		result, created = r, true
		return nil
	})
	return result, created, err
}

func (s *RefundStore) Get(ctx context.Context, tenantID refunds.TenantID, refundID refunds.RefundID) (refunds.Refund, error) {
	var result refunds.Refund
	err := s.db.WithinTenant(ctx, string(tenantID), func(tx pgx.Tx) error {
		var encoded []byte
		if err := tx.QueryRow(ctx, `SELECT aggregate FROM financial_refund_requests WHERE tenant_id=$1 AND id=$2`, tenantID, refundID).Scan(&encoded); err != nil {
			return err
		}
		return json.Unmarshal(encoded, &result)
	})
	return result, err
}

func (s *RefundStore) List(ctx context.Context, tenantID refunds.TenantID, after string, limit int) ([]refunds.Refund, error) {
	if limit < 1 || limit > 200 {
		return nil, refunds.ErrValidation
	}
	items := make([]refunds.Refund, 0, limit)
	err := s.db.WithinTenant(ctx, string(tenantID), func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT aggregate FROM financial_refund_requests
WHERE tenant_id=$1 AND ($2='' OR id<$2::uuid) ORDER BY id DESC LIMIT $3`, tenantID, after, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var encoded []byte
			var item refunds.Refund
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

func (s *RefundStore) ListExecutable(ctx context.Context, tenantID refunds.TenantID, limit int) ([]refunds.Refund, error) {
	if limit < 1 || limit > 200 {
		return nil, refunds.ErrValidation
	}
	items := make([]refunds.Refund, 0, limit)
	err := s.db.WithinTenant(ctx, string(tenantID), func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT aggregate FROM financial_refund_requests
WHERE tenant_id=$1 AND status IN ('approved','awaiting_signature','signed') ORDER BY id LIMIT $2`, tenantID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var encoded []byte
			var item refunds.Refund
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

func (s *RefundStore) ListAwaitingFinality(ctx context.Context, tenantID refunds.TenantID, limit int) ([]refunds.Refund, error) {
	if limit < 1 || limit > 200 {
		return nil, refunds.ErrValidation
	}
	items := make([]refunds.Refund, 0, limit)
	err := s.db.WithinTenant(ctx, string(tenantID), func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT aggregate FROM financial_refund_requests WHERE tenant_id=$1 AND status IN ('broadcast','confirmed','finalized') ORDER BY updated_at,id LIMIT $2`, tenantID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var b []byte
			var item refunds.Refund
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

func (s *RefundStore) Update(ctx context.Context, mutation refunds.UpdateMutation) (result refunds.Refund, err error) {
	if mutation.TenantID == "" || mutation.RefundID == "" || mutation.ExpectedVersion < 1 || mutation.Next.Version != mutation.ExpectedVersion+1 || mutation.Next.TenantID != mutation.TenantID || mutation.Next.ID != mutation.RefundID {
		return result, refunds.ErrValidation
	}
	err = s.db.WithinTenant(ctx, string(mutation.TenantID), func(tx pgx.Tx) error {
		if mutation.DecisionOperation != "" {
			replayed, err := financialDecisionReplay(ctx, tx, string(mutation.TenantID), string(mutation.DecisionActor), mutation.DecisionOperation, mutation.DecisionKey, mutation.DecisionFingerprint, &result, refunds.ErrIdempotencyConflict)
			if err != nil || replayed {
				return err
			}
		}
		if err := checkFinancialFence(ctx, tx, string(mutation.TenantID), "refund", string(mutation.RefundID)); err != nil {
			return err
		}
		var currentJSON []byte
		var currentVersion int64
		if err := tx.QueryRow(ctx, `SELECT aggregate,version FROM financial_refund_requests
WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, mutation.TenantID, mutation.RefundID).Scan(&currentJSON, &currentVersion); err != nil {
			return err
		}
		if currentVersion != mutation.ExpectedVersion {
			return refunds.ErrVersionConflict
		}
		var current refunds.Refund
		if err := json.Unmarshal(currentJSON, &current); err != nil {
			return err
		}
		if mutation.Next.Status == refunds.StatusAwaitingSignature || mutation.Next.Status == refunds.StatusSigned || mutation.Next.Status == refunds.StatusBroadcast {
			var matchState, eventStatus string
			var finalized, risk bool
			if err := tx.QueryRow(ctx, `SELECT pm.state::text,te.status::text,s.finalized,s.risk_hold
FROM financial_refund_settlements s JOIN payment_matches pm ON pm.id=s.payment_match_id AND pm.tenant_id=s.tenant_id
JOIN transfer_events te ON te.id=s.chain_event_id WHERE s.tenant_id=$1 AND s.id=$2`, mutation.TenantID, current.SettlementID).Scan(&matchState, &eventStatus, &finalized, &risk); err != nil {
				return err
			}
			if matchState != "finalized" || eventStatus != "finalized" || !finalized || risk {
				return refunds.ErrStateConflict
			}
		}
		encoded, err := json.Marshal(mutation.Next)
		if err != nil {
			return err
		}
		command, err := tx.Exec(ctx, `UPDATE financial_refund_requests SET
status=$3,aggregate=$4::jsonb,version=$5,updated_at=$6
WHERE tenant_id=$1 AND id=$2 AND version=$7`, mutation.TenantID, mutation.RefundID, mutation.Next.Status, encoded, mutation.Next.Version, mutation.Next.UpdatedAt, mutation.ExpectedVersion)
		if err != nil {
			return classifyRefund(err)
		}
		if command.RowsAffected() != 1 {
			return refunds.ErrVersionConflict
		}
		if !mutation.ReleaseRefundable.IsZero() {
			command, err = tx.Exec(ctx, `UPDATE financial_refund_reservations SET released_at=$3
WHERE tenant_id=$1 AND refund_id=$2 AND released_at IS NULL AND finalized_at IS NULL`, mutation.TenantID, mutation.RefundID, mutation.Next.UpdatedAt)
			if err != nil || command.RowsAffected() != 1 {
				if err != nil {
					return err
				}
				return refunds.ErrStateConflict
			}
		}
		if mutation.Next.Status == refunds.StatusFinalized && current.Status != refunds.StatusFinalized {
			command, err = tx.Exec(ctx, `UPDATE financial_refund_reservations SET finalized_at=$3
WHERE tenant_id=$1 AND refund_id=$2 AND released_at IS NULL AND finalized_at IS NULL`, mutation.TenantID, mutation.RefundID, mutation.Next.UpdatedAt)
			if err != nil || command.RowsAffected() != 1 {
				if err != nil {
					return err
				}
				return refunds.ErrStateConflict
			}
			command, err = tx.Exec(ctx, `UPDATE financial_refund_settlements
SET refunded_amount_atomic=refunded_amount_atomic+$3::uint256,updated_at=$4
WHERE tenant_id=$1 AND id=$2 AND refunded_amount_atomic+$3::uint256<=received_amount_atomic`,
				mutation.TenantID, mutation.Next.SettlementID, mutation.Next.GrossAmount.String(), mutation.Next.UpdatedAt)
			if err != nil || command.RowsAffected() != 1 {
				if err != nil {
					return err
				}
				return refunds.ErrInsufficientRefundable
			}
		}
		if err := insertRefundArtifacts(ctx, tx, mutation.Audit, mutation.Ledger, mutation.Outbox); err != nil {
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

func (s *RefundStore) ReplayDecision(ctx context.Context, tenantID refunds.TenantID, actorID refunds.ActorID, operation, key string, fingerprint [32]byte) (result refunds.Refund, replayed bool, err error) {
	err = s.db.WithinTenant(ctx, string(tenantID), func(tx pgx.Tx) error {
		var inner error
		replayed, inner = financialDecisionReplay(ctx, tx, string(tenantID), string(actorID), operation, key, fingerprint, &result, refunds.ErrIdempotencyConflict)
		return inner
	})
	return result, replayed, err
}

func insertRefundArtifacts(ctx context.Context, tx pgx.Tx, audit refunds.AuditCommand, ledger []refunds.LedgerCommand, outbox []refunds.OutboxCommand) error {
	if err := insertFinancialAudit(ctx, tx, audit.ID, string(audit.TenantID), "refund", audit.AggregateID, string(audit.ActorID), audit.Action, audit.Reason, audit.OccurredAt); err != nil {
		return err
	}
	for _, command := range ledger {
		if command.TenantID != audit.TenantID {
			return refunds.ErrValidation
		}
		if err := insertFinancialLedger(ctx, tx, command.ID, string(command.TenantID), string(command.AssetID), "refund", command.AggregateID, command.EntryType, command.DebitAccountID, command.CreditAccountID, command.Amount, command.OccurredAt); err != nil {
			return err
		}
	}
	for _, event := range outbox {
		if event.TenantID != audit.TenantID {
			return refunds.ErrValidation
		}
		if err := insertFinancialOutbox(ctx, tx, event.ID, string(event.TenantID), "refund", event.AggregateID, event.EventType, event.Payload, event.OccurredAt); err != nil {
			return err
		}
	}
	return nil
}

func classifyRefund(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return refunds.ErrStateConflict
	}
	return err
}

// Keep validation messages useful without exporting database-specific numeric
// details through the domain packages.
func validateRefundAccountCode(value string) error {
	if strings.TrimSpace(value) == "" || len(value) > 255 {
		return fmt.Errorf("invalid refund ledger account code")
	}
	return nil
}
