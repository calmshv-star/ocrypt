package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/ids"
	"github.com/calmshv-star/ocrypt/backend/internal/money"
	"github.com/calmshv-star/ocrypt/backend/internal/webhook"
	"github.com/jackc/pgx/v5"
)

type paymentObservation struct {
	ID, TenantID, MerchantID, IntentID, RouteID, TransferEventID string
	ChainID, AssetID, TransactionID, EventIndex                  string
	FromAddress, ToAddress, BlockHash, EvidenceHash              string
	Amount                                                       money.Amount
	AssetDecimals                                                uint8
	BlockHeight, Confirmations, RequiredConfirmations            uint64
	BlockTime                                                    time.Time
	Finality                                                     string
	Generation, Version                                          int64
}

// Ready prevents an API or worker from advertising readiness while the
// lifecycle projection is absent or its atomic resolution trigger is disabled.
func (s *Store) Ready(ctx context.Context) error {
	var ready bool
	err := s.db.pool.QueryRow(ctx, `SELECT
  to_regclass('public.payment_observations') IS NOT NULL
  AND to_regclass('public.payment_observation_events') IS NOT NULL
  AND to_regclass('public.manual_resolution_events') IS NOT NULL
  AND EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid='public.manual_resolutions'::regclass AND tgname='manual_resolution_webhook' AND tgenabled<>'D')`).Scan(&ready)
	if err != nil {
		return err
	}
	if !ready {
		return fmt.Errorf("payment lifecycle migration 000023 is not ready")
	}
	return nil
}

// Ping lets the API readiness group enforce the lifecycle migration in
// addition to the pool-level database probe. Workers use Ready directly.
func (s *Store) Ping(ctx context.Context) error { return s.Ready(ctx) }

func advancePaymentObservation(ctx context.Context, tx pgx.Tx, candidate settlementCandidate, event domain.TransferEvent, now time.Time) (paymentObservation, error) {
	if candidate.RequiredFinality < 1 {
		return paymentObservation{}, fmt.Errorf("%w: on-chain route requires positive finality", domain.ErrInvariantViolation)
	}
	evidence, err := hex.DecodeString(event.EvidenceHash)
	if err != nil || len(evidence) != sha256.Size {
		return paymentObservation{}, fmt.Errorf("%w: invalid observation evidence", domain.ErrValidation)
	}
	desiredFinality := "observed"
	if event.Status == domain.TransferFinalized {
		desiredFinality = "finalized"
	} else if event.Status == domain.TransferConfirmed || event.Confirmations >= candidate.RequiredFinality {
		desiredFinality = "confirmed"
	}

	observation, found, err := loadPaymentObservation(ctx, tx, event.ID)
	if err != nil {
		return observation, err
	}
	created, reincluded := false, false
	if !found {
		observationID, idErr := ids.New()
		if idErr != nil {
			return observation, idErr
		}
		observation = paymentObservation{
			ID: observationID, TenantID: candidate.TenantID, MerchantID: candidate.MerchantID,
			IntentID: candidate.IntentID, RouteID: candidate.RouteID, TransferEventID: event.ID,
			ChainID: event.Identity.ChainID, AssetID: event.Identity.AssetID, TransactionID: event.Identity.TransactionID,
			EventIndex: event.Identity.EventIndex, FromAddress: event.FromAddress, ToAddress: event.Identity.ToAddress,
			Amount: event.Amount, AssetDecimals: event.AssetDecimals, BlockHash: event.BlockHash,
			BlockHeight: event.BlockHeight, BlockTime: event.OnChainTime, Confirmations: event.Confirmations,
			RequiredConfirmations: candidate.RequiredFinality, Finality: desiredFinality,
			EvidenceHash: event.EvidenceHash, Generation: 1, Version: 1,
		}
		_, err = tx.Exec(ctx, `INSERT INTO payment_observations(id,tenant_id,merchant_id,intent_id,route_id,transfer_event_id,chain_id,asset_id,transaction_id,event_identity,from_address,to_address,amount_atomic,asset_decimals,block_hash,block_height,block_time,confirmations,required_confirmations,finality,evidence_hash,generation,created_at,updated_at,version) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13::numeric,$14,$15,$16::numeric,$17,$18,$19,$20,$21,1,$22,$22,1)`, observation.ID, observation.TenantID, observation.MerchantID, observation.IntentID, observation.RouteID, observation.TransferEventID, observation.ChainID, observation.AssetID, observation.TransactionID, observation.EventIndex, observation.FromAddress, observation.ToAddress, observation.Amount.String(), observation.AssetDecimals, observation.BlockHash, strconv.FormatUint(observation.BlockHeight, 10), observation.BlockTime, observation.Confirmations, observation.RequiredConfirmations, observation.Finality, evidence, now)
		if err != nil {
			return paymentObservation{}, classify(err)
		}
		created = true
	} else {
		if observation.TenantID != candidate.TenantID || observation.MerchantID != candidate.MerchantID || observation.IntentID != candidate.IntentID || observation.RouteID != candidate.RouteID || observation.ChainID != event.Identity.ChainID || observation.AssetID != event.Identity.AssetID || observation.TransactionID != event.Identity.TransactionID || observation.EventIndex != event.Identity.EventIndex || observation.FromAddress != event.FromAddress || observation.ToAddress != event.Identity.ToAddress || observation.Amount.Cmp(event.Amount) != 0 || observation.AssetDecimals != event.AssetDecimals || observation.RequiredConfirmations != candidate.RequiredFinality {
			return paymentObservation{}, fmt.Errorf("%w: payment observation binding changed", domain.ErrInvariantViolation)
		}
		if observation.Finality == "reorged" {
			observation.Generation++
			observation.BlockHash, observation.BlockHeight, observation.BlockTime = event.BlockHash, event.BlockHeight, event.OnChainTime
			observation.Confirmations, observation.Finality, observation.EvidenceHash = event.Confirmations, desiredFinality, event.EvidenceHash
			observation.Version++
			command, updateErr := tx.Exec(ctx, `UPDATE payment_observations SET block_hash=$1,block_height=$2::numeric,block_time=$3,confirmations=$4,finality=$5,evidence_hash=$6,generation=generation+1,updated_at=$7,version=version+1 WHERE id=$8 AND tenant_id=$9 AND finality='reorged' AND version=$10`, observation.BlockHash, strconv.FormatUint(observation.BlockHeight, 10), observation.BlockTime, observation.Confirmations, observation.Finality, evidence, now, observation.ID, observation.TenantID, observation.Version-1)
			if updateErr != nil {
				return paymentObservation{}, updateErr
			}
			if command.RowsAffected() != 1 {
				return paymentObservation{}, domain.ErrVersionConflict
			}
			reincluded = true
		} else if observation.Finality != desiredFinality || event.Confirmations > observation.Confirmations {
			observation.Confirmations = max(observation.Confirmations, event.Confirmations)
			observation.Finality = laterObservationFinality(observation.Finality, desiredFinality)
			observation.Version++
			command, updateErr := tx.Exec(ctx, `UPDATE payment_observations SET confirmations=$1,finality=$2,updated_at=$3,version=version+1 WHERE id=$4 AND tenant_id=$5 AND version=$6 AND finality<>'reorged'`, observation.Confirmations, observation.Finality, now, observation.ID, observation.TenantID, observation.Version-1)
			if updateErr != nil {
				return paymentObservation{}, updateErr
			}
			if command.RowsAffected() != 1 {
				return paymentObservation{}, domain.ErrVersionConflict
			}
		}
	}

	// Finalized-first catch-up is represented by payment.settled only. The row
	// still preserves the canonical observation and is linked to that event.
	if event.Status == domain.TransferFinalized {
		return observation, nil
	}
	confirmingEmitted := false
	if !created && !reincluded && observation.Finality == "confirmed" {
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM payment_observation_events WHERE observation_id=$1 AND generation=$2 AND event_type='payment.confirming')`, observation.ID, observation.Generation).Scan(&confirmingEmitted); err != nil {
			return paymentObservation{}, err
		}
	}
	eventType := observationLifecycleEvent(created, reincluded, observation.Finality, confirmingEmitted, event.Status)
	if eventType == "" {
		return observation, nil
	}

	intentStatus, intentVersion, err := advanceObservedIntent(ctx, tx, candidate, eventType, now)
	if err != nil {
		return paymentObservation{}, err
	}
	if _, err = emitObservationCallback(ctx, tx, candidate, observation, eventType, intentStatus, intentVersion, now); err != nil {
		return paymentObservation{}, err
	}
	return observation, nil
}

func observationLifecycleEvent(created, reincluded bool, finality string, confirmingEmitted bool, transferStatus domain.TransferStatus) string {
	if transferStatus == domain.TransferFinalized {
		return ""
	}
	if created || reincluded {
		if finality == "confirmed" {
			return "payment.confirming"
		}
		return "payment.observed"
	}
	if finality == "confirmed" && !confirmingEmitted {
		return "payment.confirming"
	}
	return ""
}

func loadPaymentObservation(ctx context.Context, tx pgx.Tx, transferEventID string) (paymentObservation, bool, error) {
	var observation paymentObservation
	var amount, height string
	var evidence []byte
	err := tx.QueryRow(ctx, `SELECT id::text,tenant_id::text,merchant_id::text,intent_id::text,route_id::text,transfer_event_id::text,chain_id,asset_id,transaction_id,event_identity,from_address,to_address,amount_atomic::text,asset_decimals,block_hash,block_height::text,block_time,confirmations,required_confirmations,finality,evidence_hash,generation,version FROM payment_observations WHERE transfer_event_id=$1 FOR UPDATE`, transferEventID).Scan(&observation.ID, &observation.TenantID, &observation.MerchantID, &observation.IntentID, &observation.RouteID, &observation.TransferEventID, &observation.ChainID, &observation.AssetID, &observation.TransactionID, &observation.EventIndex, &observation.FromAddress, &observation.ToAddress, &amount, &observation.AssetDecimals, &observation.BlockHash, &height, &observation.BlockTime, &observation.Confirmations, &observation.RequiredConfirmations, &observation.Finality, &evidence, &observation.Generation, &observation.Version)
	if err == pgx.ErrNoRows {
		return observation, false, nil
	}
	if err != nil {
		return observation, false, err
	}
	observation.Amount, err = money.Parse(amount)
	if err == nil {
		observation.BlockHeight, err = strconv.ParseUint(height, 10, 64)
	}
	observation.EvidenceHash = hex.EncodeToString(evidence)
	return observation, err == nil, err
}

func laterObservationFinality(current, desired string) string {
	rank := map[string]int{"observed": 1, "confirmed": 2, "finalized": 3, "reorged": 4}
	if rank[desired] > rank[current] {
		return desired
	}
	return current
}

func advanceObservedIntent(ctx context.Context, tx pgx.Tx, candidate settlementCandidate, eventType string, now time.Time) (string, int64, error) {
	target, reason := "observed", "canonical_transfer_observed"
	if eventType == "payment.confirming" {
		target, reason = "confirmed", "canonical_transfer_confirming"
	}
	var status string
	var version int64
	err := tx.QueryRow(ctx, `UPDATE payment_intents SET status=$1,status_reason=$2,updated_at=$3,version=version+1 WHERE id=$4 AND tenant_id=$5 AND status IN ('pending','observed','confirmed','reorg_review') AND status<>$1 RETURNING status::text,version`, target, reason, now, candidate.IntentID, candidate.TenantID).Scan(&status, &version)
	if err == pgx.ErrNoRows {
		err = tx.QueryRow(ctx, `SELECT status::text,version FROM payment_intents WHERE id=$1 AND tenant_id=$2 FOR UPDATE`, candidate.IntentID, candidate.TenantID).Scan(&status, &version)
	}
	return status, version, err
}

func emitObservationCallback(ctx context.Context, tx pgx.Tx, candidate settlementCandidate, observation paymentObservation, eventType, intentStatus string, intentVersion int64, now time.Time) (string, error) {
	webhookID, err := ids.New()
	if err != nil {
		return "", err
	}
	outboxID, err := ids.New()
	if err != nil {
		return "", err
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, candidate.MerchantID); err != nil {
		return "", err
	}
	sequence, err := nextMerchantEventSequence(ctx, tx, candidate.TenantID, candidate.MerchantID)
	if err != nil {
		return "", err
	}
	body, err := webhook.CanonicalBody(webhook.Event{
		EventID: webhookID, EventType: eventType, SchemaVersion: "1", Sequence: sequence,
		OccurredAt: now, MerchantID: candidate.MerchantID, Livemode: candidate.Livemode,
		PaymentIntent: webhook.PaymentIntentSnapshot{ID: candidate.IntentID, MerchantOrderID: candidate.MerchantOrderID, Status: intentStatus, AmountMinor: candidate.AmountMinor, Currency: candidate.Currency},
		Observation:   &webhook.Observation{ObservationID: observation.ID, PaymentRouteID: observation.RouteID, Network: observation.ChainID, AssetID: observation.AssetID, TransactionHash: observation.TransactionID, EventIndex: observation.EventIndex, FromAddress: observation.FromAddress, ToAddress: observation.ToAddress, AmountRaw: observation.Amount, AssetDecimals: observation.AssetDecimals, BlockHeight: strconv.FormatUint(observation.BlockHeight, 10), BlockHash: observation.BlockHash, BlockTime: observation.BlockTime, Confirmations: observation.Confirmations, RequiredConfirmations: observation.RequiredConfirmations, Finality: observation.Finality, EvidenceHash: observation.EvidenceHash},
	})
	if err != nil {
		return "", err
	}
	bodyHash := sha256.Sum256(body)
	var eventSigningKey string
	if err = tx.QueryRow(ctx, `SELECT COALESCE(min(signing_key_id),'unconfigured') FROM webhook_endpoints WHERE tenant_id=$1 AND merchant_id=$2 AND status='active'`, candidate.TenantID, candidate.MerchantID).Scan(&eventSigningKey); err != nil {
		return "", err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO callback_events(id,tenant_id,merchant_id,intent_id,event_type,schema_version,canonical_payload,canonical_body,body_hash,signing_key_id,merchant_sequence,aggregate_sequence,occurred_at,created_at) VALUES($1,$2,$3,$4,$5,'1',$6::jsonb,$7,$8,$9,$10,NULL,$11,$11)`, webhookID, candidate.TenantID, candidate.MerchantID, candidate.IntentID, eventType, string(body), body, bodyHash[:], eventSigningKey, sequence, now); err != nil {
		return "", err
	}
	rows, err := tx.Query(ctx, `SELECT id::text,signing_key_id FROM webhook_endpoints WHERE tenant_id=$1 AND merchant_id=$2 AND status='active' AND ($3=ANY(event_types) OR '*'=ANY(event_types)) FOR UPDATE`, candidate.TenantID, candidate.MerchantID, eventType)
	if err != nil {
		return "", err
	}
	type endpointKey struct{ id, key string }
	var endpoints []endpointKey
	for rows.Next() {
		var endpoint endpointKey
		if err = rows.Scan(&endpoint.id, &endpoint.key); err != nil {
			rows.Close()
			return "", err
		}
		endpoints = append(endpoints, endpoint)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return "", err
	}
	rows.Close()
	for _, endpoint := range endpoints {
		deliveryID, idErr := ids.New()
		if idErr != nil {
			return "", idErr
		}
		if _, err = tx.Exec(ctx, `INSERT INTO callback_deliveries(id,tenant_id,callback_event_id,endpoint_id,signing_key_id,status,attempt_count,next_attempt_at,created_at,updated_at,version) VALUES($1,$2,$3,$4,$5,'pending',0,$6,$6,$6,1)`, deliveryID, candidate.TenantID, webhookID, endpoint.id, endpoint.key, now); err != nil {
			return "", err
		}
	}
	if _, err = tx.Exec(ctx, `INSERT INTO outbox_events(id,tenant_id,merchant_id,aggregate_type,aggregate_id,aggregate_version,aggregate_sequence,event_type,schema_version,payload,correlation_id,occurred_at,recorded_at,available_at) VALUES($1,$2,$3,'payment_observation',$4,$5,$5,$6,'1',$7::jsonb,$8,$9,$9,$9)`, outboxID, candidate.TenantID, candidate.MerchantID, observation.ID, observation.Version, eventType, body, observation.TransferEventID, now); err != nil {
		return "", err
	}
	if err = linkObservationEvent(ctx, tx, observation, eventType, webhookID, outboxID, now); err != nil {
		return "", err
	}
	_ = intentVersion // intent version is represented inside payment_intent; the outbox aggregate is the observation.
	return webhookID, nil
}

func linkObservationEvent(ctx context.Context, tx pgx.Tx, observation paymentObservation, eventType, callbackID, outboxID string, now time.Time) error {
	_, err := tx.Exec(ctx, `INSERT INTO payment_observation_events(observation_id,tenant_id,generation,observation_version,event_type,callback_event_id,outbox_event_id,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, observation.ID, observation.TenantID, observation.Generation, observation.Version, eventType, callbackID, outboxID, now)
	return classify(err)
}

// reorgPaymentObservation handles an orphaned transfer that has not posted a
// ledger entry. Settled transfers use the existing compensating-ledger path;
// both paths advance the same durable observation to reorged exactly once.
func reorgPaymentObservation(ctx context.Context, tx pgx.Tx, transferEventID string, emit bool, now time.Time) error {
	observation, found, err := loadPaymentObservation(ctx, tx, transferEventID)
	if err != nil || !found || observation.Finality == "reorged" {
		return err
	}
	oldVersion := observation.Version
	observation.Finality = "reorged"
	observation.Version++
	command, err := tx.Exec(ctx, `UPDATE payment_observations SET finality='reorged',updated_at=$1,version=version+1 WHERE id=$2 AND tenant_id=$3 AND version=$4 AND finality<>'reorged'`, now, observation.ID, observation.TenantID, oldVersion)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return domain.ErrVersionConflict
	}
	if !emit {
		return nil
	}
	var previouslyVisible bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM payment_observation_events WHERE observation_id=$1 AND generation=$2 AND event_type IN ('payment.observed','payment.confirming'))`, observation.ID, observation.Generation).Scan(&previouslyVisible); err != nil {
		return err
	}
	if !previouslyVisible {
		return nil
	}

	var candidate settlementCandidate
	candidate.TenantID, candidate.MerchantID, candidate.IntentID, candidate.RouteID = observation.TenantID, observation.MerchantID, observation.IntentID, observation.RouteID
	var amountMinor string
	var status string
	var version int64
	err = tx.QueryRow(ctx, `SELECT i.merchant_order_id,i.amount_minor::text,i.currency,(m.environment='live'),i.status::text,i.version FROM payment_intents i JOIN merchants m ON m.id=i.merchant_id AND m.tenant_id=i.tenant_id WHERE i.id=$1 AND i.tenant_id=$2 FOR UPDATE OF i`, observation.IntentID, observation.TenantID).Scan(&candidate.MerchantOrderID, &amountMinor, &candidate.Currency, &candidate.Livemode, &status, &version)
	if err == pgx.ErrNoRows {
		return domain.ErrNotFound
	}
	if err != nil {
		return err
	}
	if status == "observed" || status == "confirmed" {
		err = tx.QueryRow(ctx, `UPDATE payment_intents SET status='reorg_review',status_reason='canonical_observation_reorged',updated_at=$1,version=version+1 WHERE id=$2 AND tenant_id=$3 AND version=$4 AND status IN ('observed','confirmed') RETURNING status::text,version`, now, observation.IntentID, observation.TenantID, version).Scan(&status, &version)
		if err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE payment_routes SET status=CASE WHEN grace_ends_at>$1 THEN 'active'::route_status ELSE 'expired'::route_status END,updated_at=$1,version=version+1 WHERE id=$2 AND tenant_id=$3 AND status IN ('active','expired')`, now, observation.RouteID, observation.TenantID); err != nil {
		return err
	}
	candidate.AmountMinor, err = money.Parse(amountMinor)
	if err != nil {
		return err
	}
	// The intent transition and webhook are committed together. No payment
	// match or ledger row is fabricated for a pre-settlement observation.
	_, err = emitObservationCallback(ctx, tx, candidate, observation, "payment.reorged", status, version, now)
	return err
}
