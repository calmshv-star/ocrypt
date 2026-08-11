package management

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/application"
	"github.com/calmshv-star/ocrypt/backend/internal/ids"
	"github.com/jackc/pgx/v5"
)

func (s *PostgresRepository) FindReceiptTransferCandidate(ctx context.Context, target ReceiptTarget, amountAtomic string, occurredAt time.Time, window time.Duration) (candidate ReceiptTransferCandidate, err error) {
	if amountAtomic == "" || occurredAt.IsZero() || window <= 0 || window > 10*time.Minute {
		return candidate, ErrInvalid
	}
	err = s.withinTenant(ctx, target.TenantID, func(tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `SELECT set_config('app.merchant_id',$1,true)`, target.MerchantID); e != nil {
			return e
		}
		rows, e := tx.Query(ctx, `SELECT te.id::text,te.transaction_id
			FROM transfer_events te
			WHERE te.chain_id=$1 AND te.asset_id=$2 AND te.to_address=$3
			  AND te.amount_atomic=$4::numeric AND te.event_kind<>'gasfree_fee'
			  AND te.status<>'reorged' AND te.on_chain_time BETWEEN $5 AND $6
			  AND NOT EXISTS(SELECT 1 FROM payment_matches pm WHERE pm.event_id=te.id AND pm.state<>'reversed' AND pm.intent_id<>$7)
			ORDER BY te.on_chain_time,te.id LIMIT 2`, target.ChainID, target.AssetID, target.Address, amountAtomic, occurredAt.Add(-window), occurredAt.Add(window), target.IntentID)
		if e != nil {
			return e
		}
		defer rows.Close()
		var found []ReceiptTransferCandidate
		for rows.Next() {
			var item ReceiptTransferCandidate
			if e = rows.Scan(&item.TransferEventID, &item.TransactionID); e != nil {
				return e
			}
			found = append(found, item)
		}
		if e = rows.Err(); e != nil {
			return e
		}
		// Zero and multiple candidates both fail closed. The latter is never
		// guessed from AI confidence or amount proximity.
		if len(found) == 1 {
			candidate = found[0]
		}
		return nil
	})
	return candidate, err
}

func (s *PostgresRepository) ResolveReceiptTarget(ctx context.Context, hash [32]byte, origin string) (target ReceiptTarget, err error) {
	err = s.pool.QueryRow(ctx, `SELECT tenant_id::text,merchant_id::text,intent_id::text FROM lookup_checkout_session($1)`, hash[:]).Scan(&target.TenantID, &target.MerchantID, &target.IntentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return target, ErrNotFound
	}
	if err != nil {
		return target, err
	}
	err = s.withinTenant(ctx, target.TenantID, func(tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `SELECT set_config('app.merchant_id',$1,true)`, target.MerchantID); e != nil {
			return e
		}
		var selected, audience, allowedOrigin string
		var actions []string
		e := tx.QueryRow(ctx, `SELECT COALESCE(cs.selected_route_id::text,''),cs.audience,COALESCE(cs.allowed_origin,''),cs.allowed_actions
			FROM checkout_sessions cs
			JOIN payment_intents i ON i.id=cs.intent_id AND i.tenant_id=cs.tenant_id AND i.merchant_id=cs.merchant_id
			WHERE cs.token_hash=$1 AND cs.tenant_id=$2 AND cs.merchant_id=$3 AND cs.intent_id=$4
			  AND cs.revoked_at IS NULL AND cs.expires_at>clock_timestamp()
			  AND i.status NOT IN('settled','overpaid','cancelled','reversed')`, hash[:], target.TenantID, target.MerchantID, target.IntentID).Scan(&selected, &audience, &allowedOrigin, &actions)
		if errors.Is(e, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if e != nil {
			return e
		}
		if !checkoutOriginAllowed(audience, allowedOrigin, origin) || !contains(actions, "read") {
			return ErrNotFound
		}
		var decimals int16
		e = tx.QueryRow(ctx, `SELECT r.id::text,r.chain_id,r.asset_id,r.receiving_address,r.expected_amount_atomic::text,r.asset_decimals
			FROM payment_routes r
			WHERE r.intent_id=$1 AND r.tenant_id=$2 AND r.merchant_id=$3 AND r.provider='on_chain'
			  AND r.status IN('active','expired') AND ($4='' OR r.id=NULLIF($4,'')::uuid)
			  AND ($4<>'' OR (SELECT count(*) FROM payment_routes candidate WHERE candidate.intent_id=$1 AND candidate.tenant_id=$2 AND candidate.merchant_id=$3 AND candidate.provider='on_chain' AND candidate.status IN('active','expired'))=1)
			LIMIT 1`, target.IntentID, target.TenantID, target.MerchantID, selected).Scan(&target.RouteID, &target.ChainID, &target.AssetID, &target.Address, &target.ExpectedAmount, &decimals)
		if e != nil {
			return e
		}
		if decimals < 0 || decimals > 255 {
			return ErrDependency
		}
		target.AssetDecimals = uint8(decimals)
		return nil
	})
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrNotFound
	}
	return target, err
}

func (s *PostgresRepository) RecordReceiptAnalysis(ctx context.Context, target ReceiptTarget, analysis ReceiptAnalysis, candidate ReceiptTransferCandidate, model, mediaType string, imageSize int64, imageDigest [32]byte, idem Idempotency) (result ReceiptSubmission, replay bool, err error) {
	if result, replay, err = s.findReceiptReplay(ctx, target, idem); err != nil || replay {
		return result, replay, err
	}

	var proofID string
	transactionID := analysis.TransactionID
	correlationMethod := ""
	matchedTransferEventID := ""
	if transactionID != "" {
		correlationMethod = "receipt_transaction_id"
	} else if candidate.TransactionID != "" && candidate.TransferEventID != "" {
		transactionID = candidate.TransactionID
		matchedTransferEventID = candidate.TransferEventID
		correlationMethod = "unique_amount_time_transfer"
	}
	status := "transaction_not_visible"
	message := "Транзакция не распознана. Пришлите более чёткий чек или укажите хеш транзакции."
	if transactionID != "" {
		keyDigest := sha256.Sum256([]byte("receipt-proof-v1\x00" + target.MerchantID + "\x00" + idem.Key))
		proofKey := "receipt-proof:" + hex.EncodeToString(keyDigest[:16])
		proofFingerprint := sha256.Sum256([]byte("receipt-proof-v1\x00" + target.IntentID + "\x00" + target.ChainID + "\x00" + transactionID))
		proof, _, proofErr := s.core.CreatePaymentProof(ctx, application.SubmitPaymentProof{
			Principal:      application.Principal{TenantID: target.TenantID, MerchantID: target.MerchantID, ActorID: target.MerchantID, KeyID: "receipt-analysis"},
			IdempotencyKey: proofKey, PaymentIntentID: target.IntentID, ChainID: target.ChainID,
			TransactionID: transactionID, CorrelationID: idem.Key,
		}, hex.EncodeToString(proofFingerprint[:]))
		if proofErr != nil {
			return ReceiptSubmission{}, false, classifyReceiptCoreError(proofErr)
		}
		proofID = proof.ID
		status = "proof_queued"
		message = "Транзакция найдена и поставлена в очередь независимой проверки блокчейна."
	}

	analysisJSON, err := json.Marshal(analysis)
	if err != nil || len(analysisJSON) > 8192 {
		return ReceiptSubmission{}, false, ErrDependency
	}
	analysisDigest := sha256.Sum256(analysisJSON)
	err = s.withinTenant(ctx, target.TenantID, func(tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `SELECT set_config('app.merchant_id',$1,true)`, target.MerchantID); e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, target.MerchantID+"\x1freceipt.submit\x1f"+idem.Key); e != nil {
			return e
		}
		var storedHash, response []byte
		// The transaction-scoped advisory lock serializes this idempotency key.
		// Evidence rows are immutable, so a row lock would incorrectly require
		// UPDATE privilege on an append-only audit table.
		e := tx.QueryRow(ctx, `SELECT request_hash,response_body FROM payment_receipt_evidence WHERE merchant_id=$1 AND idempotency_key=$2`, target.MerchantID, idem.Key).Scan(&storedHash, &response)
		if e == nil {
			if !bytes.Equal(storedHash, idem.Fingerprint[:]) || json.Unmarshal(response, &result) != nil {
				return ErrIdempotencyConflict
			}
			replay = true
			return nil
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		id, e := ids.New()
		if e != nil {
			return e
		}
		now := s.now()
		result = ReceiptSubmission{ID: id, PaymentID: target.IntentID, Status: status, ProofID: proofID, ChainID: target.ChainID, TransactionID: transactionID, CorrelationMethod: correlationMethod, MatchedTransferEventID: matchedTransferEventID, Analysis: analysis, Message: message, CreatedAt: now}
		response, e = json.Marshal(result)
		if e != nil {
			return e
		}
		var nullableProof, nullableTransaction any
		if proofID != "" {
			nullableProof, nullableTransaction = proofID, transactionID
		}
		_, e = tx.Exec(ctx, `INSERT INTO payment_receipt_evidence(id,tenant_id,merchant_id,intent_id,route_id,image_sha256,image_media_type,image_size,analyzer_model,analysis,analysis_sha256,status,transaction_id,chain_id,proof_id,idempotency_key,request_hash,response_body,created_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::jsonb,$11,$12,$13,$14,$15,$16,$17,$18::jsonb,$19)`, id, target.TenantID, target.MerchantID, target.IntentID, target.RouteID, imageDigest[:], mediaType, imageSize, model, analysisJSON, analysisDigest[:], status, nullableTransaction, target.ChainID, nullableProof, idem.Key, idem.Fingerprint[:], response, now)
		return classifyManagement(e)
	})
	return result, replay, err
}

func (s *PostgresRepository) findReceiptReplay(ctx context.Context, target ReceiptTarget, idem Idempotency) (result ReceiptSubmission, replay bool, err error) {
	err = s.withinTenant(ctx, target.TenantID, func(tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `SELECT set_config('app.merchant_id',$1,true)`, target.MerchantID); e != nil {
			return e
		}
		var storedHash, body []byte
		e := tx.QueryRow(ctx, `SELECT request_hash,response_body FROM payment_receipt_evidence WHERE merchant_id=$1 AND idempotency_key=$2`, target.MerchantID, idem.Key).Scan(&storedHash, &body)
		if errors.Is(e, pgx.ErrNoRows) {
			return nil
		}
		if e != nil {
			return e
		}
		if !bytes.Equal(storedHash, idem.Fingerprint[:]) || json.Unmarshal(body, &result) != nil {
			return ErrIdempotencyConflict
		}
		replay = true
		return nil
	})
	return result, replay, err
}

func classifyReceiptCoreError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrDependency
	}
	return ErrDependency
}
