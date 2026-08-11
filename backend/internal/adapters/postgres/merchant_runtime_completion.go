package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/application"
	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/ids"
	"github.com/calmshv-star/ocrypt/backend/internal/webhook"
	"github.com/jackc/pgx/v5"
)

func setCorrelation(ctx context.Context, tx pgx.Tx, correlationID string) error {
	if correlationID == "" {
		return nil
	}
	_, err := tx.Exec(ctx, `SELECT set_config('app.correlation_id',$1,true)`, correlationID)
	return err
}

func (s *Store) ExpireIntent(ctx context.Context, cmd application.ExpireIntent, requestHash string) (intent domain.PaymentIntent, replay bool, err error) {
	err = s.db.WithinTenant(ctx, cmd.Principal.TenantID, func(tx pgx.Tx) error {
		if err := setCorrelation(ctx, tx, cmd.CorrelationID); err != nil {
			return err
		}
		const operation = "expire_intent"
		if err := lockIdempotency(ctx, tx, cmd.Principal.MerchantID, operation, cmd.IdempotencyKey); err != nil {
			return err
		}
		record, found, err := findIdempotency(ctx, tx, cmd.Principal.MerchantID, operation, cmd.IdempotencyKey)
		if err != nil {
			return err
		}
		if found {
			if !hashMatches(record.RequestHash, requestHash) {
				return domain.ErrIdempotencyConflict
			}
			if err = json.Unmarshal(record.ResponseBody, &intent); err != nil {
				return err
			}
			replay = true
			return nil
		}
		intent, err = getIntent(ctx, tx, cmd.Principal, cmd.IntentID, true)
		if err != nil {
			return err
		}
		if intent.Version != cmd.ExpectedVersion {
			return domain.ErrVersionConflict
		}
		now := s.now()
		if err = intent.Transition(domain.IntentExpired, cmd.Reason, now); err != nil {
			return err
		}
		intent.ExpiresAt = now
		command, err := tx.Exec(ctx, `UPDATE payment_intents SET status='expired',status_reason=$1,expires_at=$2,updated_at=$2,version=$3 WHERE tenant_id=$4 AND merchant_id=$5 AND id=$6 AND version=$7`, cmd.Reason, now, intent.Version, cmd.Principal.TenantID, cmd.Principal.MerchantID, intent.ID, cmd.ExpectedVersion)
		if err != nil {
			return err
		}
		if command.RowsAffected() != 1 {
			return domain.ErrVersionConflict
		}
		if _, err = tx.Exec(ctx, `UPDATE payment_routes SET status='expired',expires_at=LEAST(expires_at,$1),updated_at=$1,version=version+1 WHERE tenant_id=$2 AND merchant_id=$3 AND intent_id=$4 AND status='active'`, now, cmd.Principal.TenantID, cmd.Principal.MerchantID, intent.ID); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE checkout_sessions SET revoked_at=COALESCE(revoked_at,$1),version=version+1 WHERE tenant_id=$2 AND merchant_id=$3 AND intent_id=$4 AND revoked_at IS NULL`, now, cmd.Principal.TenantID, cmd.Principal.MerchantID, intent.ID); err != nil {
			return err
		}
		for index := range intent.Routes {
			if intent.Routes[index].Status == domain.RouteActive {
				intent.Routes[index].Status = domain.RouteExpired
				intent.Routes[index].ExpiresAt = now
				intent.Routes[index].Version++
			}
		}
		if err = insertPaymentStateCallback(ctx, tx, cmd.Principal, intent, "payment.expired", cmd.CorrelationID, now); err != nil {
			return err
		}
		body, _ := json.Marshal(intent)
		return insertIdempotency(ctx, tx, cmd.Principal, operation, cmd.IdempotencyKey, requestHash, "payment_intent", intent.ID, httpOK, body, now.Add(30*24*time.Hour))
	})
	return intent, replay, err
}

// ExpireDueIntents atomically fences and expires overdue intents. Exact-amount
// reservations remain active until grace_ends_at so late transfers cannot
// collide with a newly issued route.
func (s *Store) ExpireDueIntents(ctx context.Context, now time.Time, limit int) (int, error) {
	if now.IsZero() || limit < 1 || limit > 500 {
		return 0, fmt.Errorf("%w: invalid intent expiration sweep", domain.ErrValidation)
	}
	expired := 0
	for expired < limit {
		found := false
		err := pgx.BeginTxFunc(ctx, s.db.pool, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx pgx.Tx) error {
			var tenantID, merchantID, intentID string
			err := tx.QueryRow(ctx, `SELECT tenant_id::text,merchant_id::text,id::text FROM payment_intents WHERE expires_at<=$1 AND status IN('created','awaiting_route_selection','pending','partially_paid') ORDER BY expires_at,id FOR UPDATE SKIP LOCKED LIMIT 1`, now).Scan(&tenantID, &merchantID, &intentID)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			if err != nil {
				return err
			}
			found = true
			principal := application.Principal{TenantID: tenantID, MerchantID: merchantID}
			intent, err := getIntent(ctx, tx, principal, intentID, false)
			if err != nil {
				return err
			}
			previousVersion := intent.Version
			if err = intent.Transition(domain.IntentExpired, "payment_window_elapsed", now); err != nil {
				return err
			}
			command, err := tx.Exec(ctx, `UPDATE payment_intents SET status='expired',status_reason='payment_window_elapsed',updated_at=$1,version=$2 WHERE tenant_id=$3 AND merchant_id=$4 AND id=$5 AND version=$6 AND status IN('created','awaiting_route_selection','pending','partially_paid')`, now, intent.Version, tenantID, merchantID, intentID, previousVersion)
			if err != nil {
				return err
			}
			if command.RowsAffected() != 1 {
				return domain.ErrVersionConflict
			}
			if _, err = tx.Exec(ctx, `UPDATE payment_routes SET status='expired',updated_at=$1,version=version+1 WHERE tenant_id=$2 AND merchant_id=$3 AND intent_id=$4 AND status='active'`, now, tenantID, merchantID, intentID); err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, `UPDATE checkout_sessions SET revoked_at=COALESCE(revoked_at,$1),version=version+1 WHERE tenant_id=$2 AND merchant_id=$3 AND intent_id=$4 AND revoked_at IS NULL`, now, tenantID, merchantID, intentID); err != nil {
				return err
			}
			for index := range intent.Routes {
				if intent.Routes[index].Status == domain.RouteActive {
					intent.Routes[index].Status = domain.RouteExpired
					intent.Routes[index].Version++
				}
			}
			return insertPaymentStateCallback(ctx, tx, principal, intent, "payment.expired", "expiry-sweep:"+intentID+":"+strconv.FormatInt(intent.Version, 10), now)
		})
		if err != nil {
			return expired, err
		}
		if !found {
			break
		}
		expired++
	}
	return expired, nil
}

// ReleaseElapsedRouteGrace releases collision reservations only after their
// immutable range and route grace have ended. Assigned addresses are retired,
// not forgotten or reused, so chain observation remains safe.
func (s *Store) ReleaseElapsedRouteGrace(ctx context.Context, now time.Time, limit int) (int, error) {
	if now.IsZero() || limit < 1 || limit > 500 {
		return 0, fmt.Errorf("%w: invalid route grace sweep", domain.ErrValidation)
	}
	released := 0
	err := pgx.BeginTxFunc(ctx, s.db.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT reservation.id::text,reservation.tenant_id::text,route.address_assignment_id::text,assignment.address_id::text
FROM amount_reservations reservation JOIN payment_routes route ON route.id=reservation.route_id AND route.tenant_id=reservation.tenant_id JOIN address_assignments assignment ON assignment.id=route.address_assignment_id AND assignment.tenant_id=route.tenant_id
WHERE reservation.state='active' AND route.grace_ends_at<=$1 AND upper(reservation.active_window)<=$1 AND route.status IN('expired','cancelled','settled')
ORDER BY route.grace_ends_at,reservation.id LIMIT $2 FOR UPDATE OF reservation,route,assignment SKIP LOCKED`, now, limit)
		if err != nil {
			return err
		}
		type item struct{ reservationID, tenantID, assignmentID, addressID string }
		var items []item
		for rows.Next() {
			var value item
			if err = rows.Scan(&value.reservationID, &value.tenantID, &value.assignmentID, &value.addressID); err != nil {
				rows.Close()
				return err
			}
			items = append(items, value)
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		for _, value := range items {
			command, err := tx.Exec(ctx, `UPDATE amount_reservations SET state='released',release_reason='route_grace_elapsed',updated_at=$1,version=version+1 WHERE id=$2 AND tenant_id=$3 AND state='active' AND upper(active_window)<=$1`, now, value.reservationID, value.tenantID)
			if err != nil {
				return err
			}
			if command.RowsAffected() != 1 {
				return domain.ErrVersionConflict
			}
			if _, err = tx.Exec(ctx, `UPDATE address_assignments SET status='retired',updated_at=$1,version=version+1 WHERE id=$2 AND tenant_id=$3 AND status='bound'`, now, value.assignmentID, value.tenantID); err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, `UPDATE addresses a SET status=CASE WHEN w.custody_mode='watch_only' THEN 'available' ELSE 'retired' END,updated_at=$1,version=a.version+1 FROM wallets w WHERE a.id=$2 AND a.tenant_id=$3 AND a.wallet_id=w.id AND w.tenant_id=a.tenant_id AND a.status='assigned' AND NOT EXISTS(SELECT 1 FROM address_assignments active WHERE active.address_id=a.id AND active.tenant_id=a.tenant_id AND active.status IN('leased','bound'))`, now, value.addressID, value.tenantID); err != nil {
				return err
			}
			released++
		}
		return nil
	})
	return released, err
}

func insertPaymentStateCallback(ctx context.Context, tx pgx.Tx, principal application.Principal, intent domain.PaymentIntent, eventType, correlationID string, now time.Time) error {
	callbackID, err := ids.New()
	if err != nil {
		return err
	}
	return insertPaymentStateCallbackWithID(ctx, tx, principal, intent, eventType, correlationID, callbackID, now)
}

func insertPaymentStateCallbackWithID(ctx context.Context, tx pgx.Tx, principal application.Principal, intent domain.PaymentIntent, eventType, correlationID, callbackID string, now time.Time) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, principal.MerchantID); err != nil {
		return err
	}
	sequence, err := nextMerchantEventSequence(ctx, tx, principal.TenantID, principal.MerchantID)
	if err != nil {
		return err
	}
	body, err := webhook.CanonicalBody(webhook.Event{EventID: callbackID, EventType: eventType, SchemaVersion: "1", Sequence: sequence, OccurredAt: now, MerchantID: principal.MerchantID, Livemode: true, PaymentIntent: webhook.PaymentIntentSnapshot{ID: intent.ID, MerchantOrderID: intent.MerchantOrderID, Status: string(intent.Status), AmountMinor: intent.AmountMinor, Currency: intent.Currency}})
	if err != nil {
		return err
	}
	bodyHash := sha256.Sum256(body)
	var eventSigningKey string
	if err = tx.QueryRow(ctx, `SELECT COALESCE(min(signing_key_id),'unconfigured') FROM webhook_endpoints WHERE tenant_id=$1 AND merchant_id=$2 AND status='active'`, principal.TenantID, principal.MerchantID).Scan(&eventSigningKey); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO callback_events(id,tenant_id,merchant_id,intent_id,event_type,schema_version,canonical_payload,canonical_body,body_hash,signing_key_id,merchant_sequence,aggregate_sequence,occurred_at,created_at) VALUES($1,$2,$3,$4,$5,'1',$6::jsonb,$7,$8,$9,$10,$11,$12,$12)`, callbackID, principal.TenantID, principal.MerchantID, intent.ID, eventType, string(body), body, bodyHash[:], eventSigningKey, sequence, intent.Version, now); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT id::text,signing_key_id FROM webhook_endpoints WHERE tenant_id=$1 AND merchant_id=$2 AND status='active' AND ($3=ANY(event_types) OR '*'=ANY(event_types)) FOR UPDATE`, principal.TenantID, principal.MerchantID, eventType)
	if err != nil {
		return err
	}
	type endpoint struct{ id, keyID string }
	var endpoints []endpoint
	for rows.Next() {
		var item endpoint
		if err = rows.Scan(&item.id, &item.keyID); err != nil {
			rows.Close()
			return err
		}
		endpoints = append(endpoints, item)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, item := range endpoints {
		deliveryID, err := ids.New()
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO callback_deliveries(id,tenant_id,callback_event_id,endpoint_id,signing_key_id,status,attempt_count,next_attempt_at,created_at,updated_at,version) VALUES($1,$2,$3,$4,$5,'pending',0,$6,$6,$6,1)`, deliveryID, principal.TenantID, callbackID, item.id, item.keyID, now); err != nil {
			return err
		}
	}
	outboxID, err := ids.New()
	if err != nil {
		return err
	}
	return insertOutbox(ctx, tx, outboxID, principal, intent.ID, intent.Version, eventType, correlationID, body, now)
}

func (s *Store) UpdateIntentMetadata(ctx context.Context, cmd application.UpdateIntentMetadata, requestHash string) (intent domain.PaymentIntent, replay bool, err error) {
	err = s.db.WithinTenant(ctx, cmd.Principal.TenantID, func(tx pgx.Tx) error {
		if err := setCorrelation(ctx, tx, cmd.CorrelationID); err != nil {
			return err
		}
		const operation = "update_intent_metadata"
		if err := lockIdempotency(ctx, tx, cmd.Principal.MerchantID, operation, cmd.IdempotencyKey); err != nil {
			return err
		}
		record, found, err := findIdempotency(ctx, tx, cmd.Principal.MerchantID, operation, cmd.IdempotencyKey)
		if err != nil {
			return err
		}
		if found {
			if !hashMatches(record.RequestHash, requestHash) {
				return domain.ErrIdempotencyConflict
			}
			if err = json.Unmarshal(record.ResponseBody, &intent); err != nil {
				return err
			}
			replay = true
			return nil
		}
		intent, err = getIntent(ctx, tx, cmd.Principal, cmd.IntentID, true)
		if err != nil {
			return err
		}
		if intent.Version != cmd.ExpectedVersion || intent.Status == domain.IntentReversed {
			return domain.ErrVersionConflict
		}
		now := s.now()
		command, err := tx.Exec(ctx, `UPDATE payment_intents SET metadata=$1::jsonb,updated_at=$2,version=version+1 WHERE tenant_id=$3 AND merchant_id=$4 AND id=$5 AND version=$6 AND status<>'reversed'`, cmd.Metadata, now, cmd.Principal.TenantID, cmd.Principal.MerchantID, intent.ID, cmd.ExpectedVersion)
		if err != nil {
			return err
		}
		if command.RowsAffected() != 1 {
			return domain.ErrVersionConflict
		}
		intent.Metadata = append(json.RawMessage(nil), cmd.Metadata...)
		intent.Version++
		intent.UpdatedAt = now
		eventID, err := ids.New()
		if err != nil {
			return err
		}
		metadataDigest := sha256.Sum256(cmd.Metadata)
		payload, _ := json.Marshal(map[string]any{"payment_intent_id": intent.ID, "status": intent.Status, "metadata_sha256": hex.EncodeToString(metadataDigest[:])})
		if err = insertOutbox(ctx, tx, eventID, cmd.Principal, intent.ID, intent.Version, "payment.metadata_updated", cmd.CorrelationID, payload, now); err != nil {
			return err
		}
		body, _ := json.Marshal(intent)
		return insertIdempotency(ctx, tx, cmd.Principal, operation, cmd.IdempotencyKey, requestHash, "payment_intent", intent.ID, httpOK, body, now.Add(30*24*time.Hour))
	})
	return intent, replay, err
}

func (s *Store) GetEvent(ctx context.Context, principal application.Principal, id string) (result domain.WebhookEventView, err error) {
	err = s.db.WithinTenant(ctx, principal.TenantID, func(tx pgx.Tx) error {
		var body, digest []byte
		err := tx.QueryRow(ctx, `SELECT id::text,event_type,schema_version,merchant_sequence,COALESCE(intent_id::text,''),canonical_body,body_hash,occurred_at FROM callback_events WHERE tenant_id=$1 AND merchant_id=$2 AND id=$3`, principal.TenantID, principal.MerchantID, id).Scan(&result.EventID, &result.EventType, &result.SchemaVersion, &result.Sequence, &result.PaymentIntentID, &body, &digest, &result.OccurredAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return err
		}
		calculated := sha256.Sum256(body)
		if len(digest) != sha256.Size || !equalBytes(digest, calculated[:]) || !json.Valid(body) {
			return fmt.Errorf("%w: stored webhook event digest is invalid", domain.ErrInvariantViolation)
		}
		result.CanonicalBody = append(json.RawMessage(nil), body...)
		result.CanonicalBodyBase64 = base64.RawURLEncoding.EncodeToString(body)
		result.BodySHA256 = hex.EncodeToString(digest)
		return nil
	})
	return result, err
}

func (s *Store) GetTransfer(ctx context.Context, principal application.Principal, network, transaction string) (items []domain.MerchantTransfer, err error) {
	err = s.db.WithinTenant(ctx, principal.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT DISTINCT ON(te.id) te.id::text,pm.intent_id::text,pm.route_id::text,te.chain_id,te.asset_id,te.transaction_id,te.event_identity,te.from_address,te.to_address,te.amount_atomic::text,te.block_height::text,te.block_hash,te.confirmations,te.status::text,pm.state,te.on_chain_time
FROM transfer_events te JOIN payment_matches pm ON pm.event_id=te.id JOIN payment_intents intent ON intent.id=pm.intent_id AND intent.tenant_id=pm.tenant_id
WHERE pm.tenant_id=$1 AND intent.merchant_id=$2 AND te.chain_id=$3 AND te.transaction_id=$4
ORDER BY te.id,pm.created_at DESC,pm.id DESC`, principal.TenantID, principal.MerchantID, network, transaction)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item domain.MerchantTransfer
			var status string
			if err = rows.Scan(&item.TransferEventID, &item.PaymentIntentID, &item.PaymentRouteID, &item.ChainID, &item.AssetID, &item.TransactionID, &item.EventIndex, &item.FromAddress, &item.ToAddress, &item.AmountAtomic, &item.BlockHeight, &item.BlockHash, &item.Confirmations, &status, &item.MatchState, &item.OnChainTime); err != nil {
				return err
			}
			item.Status = domain.TransferStatus(status)
			items = append(items, item)
		}
		if err = rows.Err(); err != nil {
			return err
		}
		if len(items) == 0 {
			return domain.ErrNotFound
		}
		return nil
	})
	return items, err
}

func (s *Store) GetQuote(ctx context.Context, principal application.Principal, id string) (result domain.QuoteDetail, err error) {
	err = s.db.WithinTenant(ctx, principal.TenantID, func(tx pgx.Tx) error {
		var sourceIDs []string
		var provenance []byte
		err := tx.QueryRow(ctx, `SELECT q.id::text,q.payment_intent_id::text,q.fiat_amount_minor::text,q.fiat_currency,q.fiat_scale,q.asset_id,q.crypto_amount_atomic::text,q.reference_price,q.spread_bps,q.policy_version,q.issued_at,q.expires_at,q.source_tick_ids,q.raw_provenance_hash
FROM rate_quotes q WHERE q.id=$1 AND q.tenant_id=$2 AND q.merchant_id=$3 AND EXISTS(SELECT 1 FROM payment_routes route WHERE route.tenant_id=q.tenant_id AND route.merchant_id=q.merchant_id AND route.quote_id=q.id)`, id, principal.TenantID, principal.MerchantID).Scan(&result.ID, &result.PaymentIntentID, &result.FiatAmountMinor, &result.FiatCurrency, &result.FiatScale, &result.AssetID, &result.CryptoAmountAtomic, &result.ReferencePrice, &result.SpreadBPS, &result.PolicyVersion, &result.IssuedAt, &result.ExpiresAt, &sourceIDs, &provenance)
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return err
		}
		if len(provenance) != sha256.Size {
			return fmt.Errorf("%w: quote provenance digest is invalid", domain.ErrInvariantViolation)
		}
		result.SourceTickIDs = append([]string(nil), sourceIDs...)
		result.RawProvenanceSHA256 = hex.EncodeToString(provenance)
		if len(sourceIDs) == 0 {
			result.Sources = []domain.QuoteSourceView{}
			return nil
		}
		rows, err := tx.Query(ctx, `SELECT id::text,source,numerator::text,denominator::text,spread_bps,policy_version,observed_at,max_age_seconds,provenance_hash FROM asset_rate_ticks WHERE id=ANY($1::uuid[]) ORDER BY observed_at,id`, sourceIDs)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var source domain.QuoteSourceView
			var digest []byte
			if err = rows.Scan(&source.ID, &source.Source, &source.PriceNumerator, &source.PriceDenominator, &source.SpreadBPS, &source.PolicyVersion, &source.ObservedAt, &source.MaxAgeSeconds, &digest); err != nil {
				return err
			}
			if len(digest) != sha256.Size {
				return fmt.Errorf("%w: quote source digest is invalid", domain.ErrInvariantViolation)
			}
			source.ProvenanceSHA256 = hex.EncodeToString(digest)
			result.Sources = append(result.Sources, source)
		}
		return rows.Err()
	})
	return result, err
}

func (s *Store) CreateReconciliationReport(ctx context.Context, cmd application.CreateReconciliationReport, requestHash string) (result domain.ReconciliationReport, replay bool, err error) {
	err = s.db.WithinTenant(ctx, cmd.Principal.TenantID, func(tx pgx.Tx) error {
		const operation = "reconciliation_report.create"
		if err := lockIdempotency(ctx, tx, cmd.Principal.MerchantID, operation, cmd.IdempotencyKey); err != nil {
			return err
		}
		record, found, err := findIdempotency(ctx, tx, cmd.Principal.MerchantID, operation, cmd.IdempotencyKey)
		if err != nil {
			return err
		}
		if found {
			if !hashMatches(record.RequestHash, requestHash) {
				return domain.ErrIdempotencyConflict
			}
			if err = json.Unmarshal(record.ResponseBody, &result); err != nil {
				return err
			}
			replay = true
			return nil
		}
		id, err := ids.New()
		if err != nil {
			return err
		}
		// This table lock waits for pre-existing ledger writers and prevents new
		// ones until the report row commits. The resulting global sequence fence
		// therefore identifies one durable database snapshot without relying on
		// application wall clocks or transaction commit order.
		if _, err = tx.Exec(ctx, `LOCK TABLE ledger_transactions IN SHARE MODE`); err != nil {
			return err
		}
		var snapshotCutoff time.Time
		var fenceSequence int64
		if err = tx.QueryRow(ctx, `SELECT clock_timestamp(),COALESCE(max(ledger_sequence),0) FROM ledger_transactions`).Scan(&snapshotCutoff, &fenceSequence); err != nil {
			return err
		}
		if cmd.PeriodEnd.After(snapshotCutoff) {
			return fmt.Errorf("%w: period_end must not exceed snapshot_cutoff", domain.ErrValidation)
		}
		_, err = tx.Exec(ctx, `INSERT INTO reconciliation_reports(id,tenant_id,merchant_id,format,period_start,period_end,snapshot_ledger_sequence,snapshot_fence_sequence,snapshot_cutoff,status,attempt_count,next_attempt_at,created_at,updated_at,version) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,'queued',0,$9,$9,$9,1)`, id, cmd.Principal.TenantID, cmd.Principal.MerchantID, cmd.Format, cmd.PeriodStart, cmd.PeriodEnd, fenceSequence, fenceSequence, snapshotCutoff)
		if err != nil {
			return classify(err)
		}
		result, err = loadReconciliationReport(ctx, tx, cmd.Principal, id)
		if err != nil {
			return err
		}
		body, _ := json.Marshal(result)
		return insertIdempotency(ctx, tx, cmd.Principal, operation, cmd.IdempotencyKey, requestHash, "reconciliation_report", id, httpAccepted, body, snapshotCutoff.Add(30*24*time.Hour))
	})
	return result, replay, err
}

func (s *Store) GetReconciliationReport(ctx context.Context, principal application.Principal, id string) (result domain.ReconciliationReport, err error) {
	err = s.db.WithinTenant(ctx, principal.TenantID, func(tx pgx.Tx) error {
		var err error
		result, err = loadReconciliationReport(ctx, tx, principal, id)
		return err
	})
	return result, err
}

func loadReconciliationReport(ctx context.Context, tx pgx.Tx, principal application.Principal, id string) (result domain.ReconciliationReport, err error) {
	var status string
	var snapshot int64
	var size *int64
	var digest, signature []byte
	err = tx.QueryRow(ctx, `SELECT id::text,status,format,period_start,period_end,snapshot_ledger_sequence,snapshot_cutoff,attempt_count,COALESCE(last_error_code,''),object_size_bytes,object_sha256,signature,COALESCE(signing_key_id,''),created_at,updated_at,completed_at,version FROM reconciliation_reports WHERE tenant_id=$1 AND merchant_id=$2 AND id=$3`, principal.TenantID, principal.MerchantID, id).Scan(&result.ID, &status, &result.Format, &result.PeriodStart, &result.PeriodEnd, &snapshot, &result.SnapshotCutoff, &result.AttemptCount, &result.LastErrorCode, &size, &digest, &signature, &result.SigningKeyID, &result.CreatedAt, &result.UpdatedAt, &result.CompletedAt, &result.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, domain.ErrNotFound
	}
	if err != nil {
		return result, err
	}
	result.Status = domain.ReconciliationReportStatus(status)
	result.SnapshotLedgerSequence = strconv.FormatInt(snapshot, 10)
	if size != nil {
		result.ObjectSizeBytes = strconv.FormatInt(*size, 10)
	}
	if len(digest) > 0 {
		result.ObjectSHA256 = hex.EncodeToString(digest)
	}
	if len(signature) > 0 {
		result.Signature = base64.RawURLEncoding.EncodeToString(signature)
	}
	if result.Status == domain.ReconciliationReportReady {
		result.DownloadPath = "/v1/reconciliation-reports/" + result.ID + "/download"
	}
	return result, nil
}

func equalBytes(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	var different byte
	for i := range left {
		different |= left[i] ^ right[i]
	}
	return different == 0
}
