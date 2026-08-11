package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/application"
	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/ids"
	"github.com/jackc/pgx/v5"
)

func (s *Store) CreatePaymentProof(ctx context.Context, cmd application.SubmitPaymentProof, requestHash string) (proof domain.PaymentProof, replay bool, err error) {
	err = s.db.WithinTenant(ctx, cmd.Principal.TenantID, func(tx pgx.Tx) error {
		if err := lockIdempotency(ctx, tx, cmd.Principal.MerchantID, "create_payment_proof", cmd.IdempotencyKey); err != nil {
			return err
		}
		record, found, err := findIdempotency(ctx, tx, cmd.Principal.MerchantID, "create_payment_proof", cmd.IdempotencyKey)
		if err != nil {
			return err
		}
		if found {
			if !hashMatches(record.RequestHash, requestHash) {
				return domain.ErrIdempotencyConflict
			}
			if err := json.Unmarshal(record.ResponseBody, &proof); err != nil {
				return err
			}
			replay = true
			return nil
		}
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM payment_intents WHERE id=$1 AND tenant_id=$2 AND merchant_id=$3)`, cmd.PaymentIntentID, cmd.Principal.TenantID, cmd.Principal.MerchantID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return domain.ErrNotFound
		}
		id, err := ids.New()
		if err != nil {
			return err
		}
		eventID, err := ids.New()
		if err != nil {
			return err
		}
		now := s.now()
		proof = domain.PaymentProof{ID: id, TenantID: cmd.Principal.TenantID, MerchantID: cmd.Principal.MerchantID, PaymentIntentID: cmd.PaymentIntentID, ChainID: cmd.ChainID, TransactionID: cmd.TransactionID, Status: domain.ProofQueued, TransferEventIDs: []string{}, CreatedAt: now, UpdatedAt: now, Version: 1}
		_, err = tx.Exec(ctx, `INSERT INTO payment_proofs (id,tenant_id,merchant_id,intent_id,chain_id,transaction_id,status,next_attempt_at,created_at,updated_at,version) VALUES ($1,$2,$3,$4,$5,$6,'queued',$7,$7,$7,1)`, id, cmd.Principal.TenantID, cmd.Principal.MerchantID, cmd.PaymentIntentID, cmd.ChainID, cmd.TransactionID, now)
		if err != nil {
			return classify(err)
		}
		payload, _ := json.Marshal(map[string]any{"payment_proof_id": id, "payment_intent_id": cmd.PaymentIntentID, "chain_id": cmd.ChainID, "transaction_id": cmd.TransactionID})
		_, err = tx.Exec(ctx, `INSERT INTO outbox_events (id,tenant_id,merchant_id,aggregate_type,aggregate_id,aggregate_version,aggregate_sequence,event_type,schema_version,payload,correlation_id,occurred_at,recorded_at,available_at) VALUES ($1,$2,$3,'payment_proof',$4,1,1,'payment.proof.submitted','1',$5::jsonb,NULLIF($6,''),$7,$7,$7)`, eventID, cmd.Principal.TenantID, cmd.Principal.MerchantID, id, payload, cmd.CorrelationID, now)
		if err != nil {
			return err
		}
		body, _ := json.Marshal(proof)
		return insertIdempotency(ctx, tx, cmd.Principal, "create_payment_proof", cmd.IdempotencyKey, requestHash, "payment_proof", id, httpCreated, body, now.Add(30*24*time.Hour))
	})
	return proof, replay, err
}
func (s *Store) GetPaymentProof(ctx context.Context, p application.Principal, id string) (proof domain.PaymentProof, err error) {
	err = s.db.WithinTenant(ctx, p.TenantID, func(tx pgx.Tx) error {
		var status string
		err := tx.QueryRow(ctx, `SELECT id::text,tenant_id::text,merchant_id::text,intent_id::text,chain_id,transaction_id,status,transfer_event_ids,created_at,updated_at,version FROM payment_proofs WHERE id=$1 AND tenant_id=$2 AND merchant_id=$3`, id, p.TenantID, p.MerchantID).Scan(&proof.ID, &proof.TenantID, &proof.MerchantID, &proof.PaymentIntentID, &proof.ChainID, &proof.TransactionID, &status, &proof.TransferEventIDs, &proof.CreatedAt, &proof.UpdatedAt, &proof.Version)
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		proof.Status = domain.ProofStatus(status)
		return err
	})
	return proof, err
}
