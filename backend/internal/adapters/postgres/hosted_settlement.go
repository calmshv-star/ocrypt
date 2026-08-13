package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/application"
	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/ids"
	"github.com/calmshv-star/ocrypt/backend/internal/money"
	"github.com/calmshv-star/ocrypt/backend/internal/webhook"
	"github.com/jackc/pgx/v5"
)

type hostedSettlementCandidate struct {
	ProviderOrderID, RouteID, IntentID, TenantID, MerchantID, MerchantOrderID string
	ProviderReference, ProviderStatus, AssetID, Currency                      string
	Expected, IntentAmount                                                    money.Amount
	AssetDecimals                                                             uint8
	IntentVersion                                                             int64
	RouteStatus                                                               domain.RouteStatus
	IntentStatus                                                              domain.IntentStatus
	LastOccurredAt                                                            *time.Time
	LastEventID                                                               string
	EconomicDrift                                                             bool
}

// IngestVerifiedProviderPayment is the hosted financial transaction boundary:
// immutable evidence, match, double-entry ledger, route arbitration, merchant
// callback, and outbox event commit or roll back together.
func (s *Store) IngestVerifiedProviderPayment(ctx context.Context, payment domain.VerifiedProviderPayment) (result domain.HostedSettlementResult, err error) {
	return s.ingestVerifiedProviderPayment(ctx, payment, "", "")
}

func (s *Store) ingestVerifiedProviderPayment(ctx context.Context, payment domain.VerifiedProviderPayment, prebindID, claimToken string) (result domain.HostedSettlementResult, err error) {
	principal := application.Principal{TenantID: payment.TenantID, MerchantID: payment.MerchantID}
	err = s.withinMerchant(ctx, principal, func(tx pgx.Tx) error {
		if prebindID != "" {
			var lockedID string
			lockErr := tx.QueryRow(ctx, `SELECT id::text FROM provider_prebind_inbox WHERE id=$1 AND tenant_id=$2 AND merchant_id=$3 AND state='pending' AND claim_token=$4 AND claim_until>=clock_timestamp() FOR UPDATE`, prebindID, payment.TenantID, payment.MerchantID, claimToken).Scan(&lockedID)
			if errors.Is(lockErr, pgx.ErrNoRows) {
				return domain.ErrVersionConflict
			}
			if lockErr != nil {
				return lockErr
			}
		}
		ingestErr := func() error {
			candidate, err := loadHostedSettlementCandidate(ctx, tx, payment)
			if errors.Is(err, domain.ErrNotFound) {
				result, err = ingestProviderPrebindEvidence(ctx, tx, payment)
				return err
			}
			if err != nil {
				return err
			}
			payment.ProviderOrderID = candidate.ProviderOrderID

			var existingID, existingReference, existingStatus, existingAsset, existingAmount, existingManifestID string
			var existingDigest, existingSignatureDigest []byte
			var existingDecimals uint8
			var existingConfigVersion int64
			err = tx.QueryRow(ctx, `SELECT provider_reference,provider_status,asset_id,amount_atomic::text,asset_decimals,raw_body_digest,signature_digest,config_manifest_id::text,config_version FROM provider_prebind_inbox WHERE provider_id=$1 AND provider_event_id=$2`, payment.ProviderID, payment.ProviderEventID).Scan(&existingReference, &existingStatus, &existingAsset, &existingAmount, &existingDecimals, &existingDigest, &existingSignatureDigest, &existingManifestID, &existingConfigVersion)
			if err == nil && !providerEvidenceMatches(payment, existingReference, existingStatus, existingAsset, existingAmount, existingDecimals, existingDigest, existingSignatureDigest, existingManifestID, existingConfigVersion) {
				return fmt.Errorf("%w: provider event changed between pre-bind and bound delivery", domain.ErrIdempotencyConflict)
			}
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			err = tx.QueryRow(ctx, `SELECT id::text,provider_reference,provider_status,asset_id,amount_atomic::text,asset_decimals,raw_body_digest,signature_digest,config_manifest_id::text,config_version FROM provider_inbox WHERE provider_id=$1 AND provider_event_id=$2`, payment.ProviderID, payment.ProviderEventID).Scan(&existingID, &existingReference, &existingStatus, &existingAsset, &existingAmount, &existingDecimals, &existingDigest, &existingSignatureDigest, &existingManifestID, &existingConfigVersion)
			if err == nil {
				if !providerEvidenceMatches(payment, existingReference, existingStatus, existingAsset, existingAmount, existingDecimals, existingDigest, existingSignatureDigest, existingManifestID, existingConfigVersion) {
					return fmt.Errorf("%w: provider event id was replayed with different canonical facts", domain.ErrIdempotencyConflict)
				}
				result.Duplicate = true
				result.ProviderInboxID = existingID
				result.IntentID = candidate.IntentID
				result.RouteID = candidate.RouteID
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}

			inboxID, err := ids.New()
			if err != nil {
				return err
			}
			_, err = tx.Exec(ctx, `INSERT INTO provider_inbox(id,tenant_id,merchant_id,provider_id,provider_order_id,route_id,provider_event_id,provider_reference,provider_status,asset_id,amount_atomic,asset_decimals,raw_body,raw_body_digest,signature_scheme,signature_key_id,signature_digest,config_manifest_id,config_version,provider_occurred_at,received_at,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11::numeric,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$21)`, inboxID, payment.TenantID, payment.MerchantID, payment.ProviderID, candidate.ProviderOrderID, candidate.RouteID, payment.ProviderEventID, payment.ProviderReference, payment.ProviderStatus, payment.AssetID, payment.Amount.String(), payment.AssetDecimals, payment.RawBody, payment.RawDigest[:], payment.SignatureScheme, payment.SignatureKeyID, payment.SignatureDigest[:], payment.ConfigManifestID, payment.ConfigVersion, payment.OccurredAt.UTC(), payment.ReceivedAt.UTC())
			if err != nil {
				return classify(err)
			}
			result.ProviderInboxID = inboxID
			result.IntentID = candidate.IntentID
			result.RouteID = candidate.RouteID
			if providerCallbackShouldQuarantine(payment) {
				result.Quarantined = true
				return recordPausedProviderCallbackIncident(ctx, tx, candidate, inboxID, payment.RawDigest[:], payment.ReceivedAt.UTC())
			}
			if candidate.EconomicDrift {
				return recordProviderReconciliationIncident(ctx, tx, candidate, inboxID, "economic_fact_mismatch", payment.ReceivedAt.UTC())
			}

			incidentKind, incidentOutOfOrder := providerIncidentDecision(candidate, payment)
			if incidentOutOfOrder {
				result.OutOfOrder = true
				return recordProviderReconciliationIncident(ctx, tx, candidate, inboxID, incidentKind, payment.ReceivedAt.UTC())
			}
			if providerEventIsOutOfOrder(candidate, payment) {
				result.OutOfOrder = true
				return nil
			}
			mappedStatus := payment.ProviderStatus
			_, err = tx.Exec(ctx, `UPDATE provider_orders SET provider_status=$1,last_provider_occurred_at=$2,last_provider_event_id=$3,updated_at=$4,version=version+1 WHERE id=$5 AND tenant_id=$6 AND merchant_id=$7`, mappedStatus, payment.OccurredAt.UTC(), payment.ProviderEventID, payment.ReceivedAt.UTC(), candidate.ProviderOrderID, payment.TenantID, payment.MerchantID)
			if err != nil {
				return err
			}
			if payment.ProviderStatus == "refunded" {
				return recordProviderReconciliationIncident(ctx, tx, candidate, inboxID, incidentKind, payment.ReceivedAt.UTC())
			}
			if payment.ProviderStatus != "paid" {
				return nil
			}
			if payment.AssetID != candidate.AssetID || payment.AssetDecimals != candidate.AssetDecimals || payment.Amount.Cmp(candidate.Expected) != 0 {
				// Verified but economically different evidence remains immutable for
				// operator review and can never reach the settlement ledger.
				return recordProviderReconciliationIncident(ctx, tx, candidate, inboxID, "economic_fact_mismatch", payment.ReceivedAt.UTC())
			}
			if candidate.IntentStatus == domain.IntentSettled || candidate.IntentStatus == domain.IntentOverpaid || candidate.RouteStatus != domain.RouteActive {
				_, _ = tx.Exec(ctx, `UPDATE provider_orders SET provider_status='superseded',updated_at=$1,version=version+1 WHERE id=$2 AND provider_status='paid'`, payment.ReceivedAt.UTC(), candidate.ProviderOrderID)
				return nil
			}
			if candidate.IntentStatus != domain.IntentPending && candidate.IntentStatus != domain.IntentObserved && candidate.IntentStatus != domain.IntentConfirmed && candidate.IntentStatus != domain.IntentReorgReview {
				return nil
			}

			settlementID, err := ids.New()
			if err != nil {
				return err
			}
			matchID, err := ids.New()
			if err != nil {
				return err
			}
			webhookID, err := ids.New()
			if err != nil {
				return err
			}
			now := payment.ReceivedAt.UTC()
			evidence, _ := json.Marshal(map[string]any{"deterministic": true, "rule": "verified_hosted_provider_v1", "provider_id": payment.ProviderID, "provider_reference": payment.ProviderReference, "provider_event_id": payment.ProviderEventID, "raw_body_sha256": hex.EncodeToString(payment.RawDigest[:])})
			_, err = tx.Exec(ctx, `INSERT INTO payment_matches(id,tenant_id,event_id,provider_inbox_id,route_id,intent_id,match_kind,expected_atomic,received_atomic,credited_atomic,state,evidence,policy_version,created_at,finalized_at) VALUES($1,$2,NULL,$3,$4,$5,'exact',$6::numeric,$6::numeric,$6::numeric,'finalized',$7::jsonb,1,$8,$8)`, matchID, candidate.TenantID, inboxID, candidate.RouteID, candidate.IntentID, candidate.Expected.String(), evidence, now)
			if err != nil {
				return classify(err)
			}
			accountCandidate := settlementCandidate{TenantID: candidate.TenantID, MerchantID: candidate.MerchantID, IntentID: candidate.IntentID, RouteID: candidate.RouteID, MerchantOrderID: candidate.MerchantOrderID, Currency: candidate.Currency, AmountMinor: candidate.IntentAmount, IntentVersion: candidate.IntentVersion, Expected: candidate.Expected}
			debitID, creditID, err := ensureSettlementAccounts(ctx, tx, accountCandidate, candidate.AssetID)
			if err != nil {
				return err
			}
			_, err = tx.Exec(ctx, `INSERT INTO ledger_transactions(id,tenant_id,business_type,business_reference,effective_at,booked_at,correlation_id,policy_version) VALUES($1::uuid,$2,'payment_settlement',$1::uuid::text,$3,$4,$5,1)`, settlementID, candidate.TenantID, payment.OccurredAt.UTC(), now, inboxID)
			if err != nil {
				return classify(err)
			}
			_, err = tx.Exec(ctx, `INSERT INTO ledger_entries(transaction_id,tenant_id,sequence,account_id,asset_id,direction,amount_atomic,created_at) VALUES($1,$2,1,$3,$4,'debit',$5::numeric,$6),($1,$2,2,$7,$4,'credit',$5::numeric,$6)`, settlementID, candidate.TenantID, debitID, candidate.AssetID, candidate.Expected.String(), now, creditID)
			if err != nil {
				return err
			}
			command, err := tx.Exec(ctx, `UPDATE payment_intents SET status='settled',status_reason='verified_hosted_provider_payment',settled_at=$1,updated_at=$1,version=version+1 WHERE id=$2 AND tenant_id=$3 AND merchant_id=$4 AND version=$5 AND status IN ('pending','observed','confirmed','reorg_review')`, now, candidate.IntentID, candidate.TenantID, candidate.MerchantID, candidate.IntentVersion)
			if err != nil {
				return err
			}
			if command.RowsAffected() != 1 {
				return domain.ErrVersionConflict
			}
			if _, err = tx.Exec(ctx, `UPDATE payment_routes SET status=CASE WHEN id=$1 THEN 'settled'::route_status ELSE 'superseded'::route_status END,updated_at=$2,version=version+1 WHERE tenant_id=$3 AND merchant_id=$4 AND intent_id=$5 AND status IN ('active','expired')`, candidate.RouteID, now, candidate.TenantID, candidate.MerchantID, candidate.IntentID); err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, `UPDATE amount_reservations ar SET state='released',release_reason='sibling_settled',updated_at=$1,version=ar.version+1 FROM payment_routes r WHERE ar.route_id=r.id AND ar.tenant_id=r.tenant_id AND r.intent_id=$2 AND r.id<>$3 AND r.tenant_id=$4 AND ar.state='active'`, now, candidate.IntentID, candidate.RouteID, candidate.TenantID); err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, `UPDATE provider_orders po SET provider_status='superseded',updated_at=$1,version=po.version+1 FROM payment_routes r WHERE po.route_id=r.id AND po.tenant_id=r.tenant_id AND r.intent_id=$2 AND r.id<>$3 AND r.tenant_id=$4 AND po.provider_status IN ('pending','authorized','cancel_requested')`, now, candidate.IntentID, candidate.RouteID, candidate.TenantID); err != nil {
				return err
			}

			if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, candidate.MerchantID); err != nil {
				return err
			}
			sequence, err := nextMerchantEventSequence(ctx, tx, candidate.TenantID, candidate.MerchantID)
			if err != nil {
				return err
			}
			body, err := webhook.CanonicalBody(webhook.Event{EventID: webhookID, EventType: "payment.settled", SchemaVersion: "1", Sequence: sequence, OccurredAt: now, MerchantID: candidate.MerchantID, Livemode: true, PaymentIntent: webhook.PaymentIntentSnapshot{ID: candidate.IntentID, MerchantOrderID: candidate.MerchantOrderID, Status: "settled", AmountMinor: candidate.IntentAmount, Currency: candidate.Currency}, Settlement: &webhook.Settlement{SettlementID: settlementID, AssetID: candidate.AssetID, ExpectedRaw: candidate.Expected, ReceivedRaw: payment.Amount, CreditedRaw: payment.Amount, ProviderID: payment.ProviderID, ProviderReference: payment.ProviderReference, ProviderEventID: payment.ProviderEventID, ProviderEvidence: hex.EncodeToString(payment.RawDigest[:]), Finality: "provider_verified", ManualResolution: false}})
			if err != nil {
				return err
			}
			bodyHash := sha256.Sum256(body)
			var signingKey string
			if err = tx.QueryRow(ctx, `SELECT COALESCE(min(signing_key_id),'unconfigured') FROM webhook_endpoints WHERE tenant_id=$1 AND merchant_id=$2 AND status='active'`, candidate.TenantID, candidate.MerchantID).Scan(&signingKey); err != nil {
				return err
			}
			_, err = tx.Exec(ctx, `INSERT INTO callback_events(id,tenant_id,merchant_id,intent_id,event_type,schema_version,canonical_payload,canonical_body,body_hash,signing_key_id,merchant_sequence,aggregate_sequence,occurred_at,created_at) VALUES($1,$2,$3,$4,'payment.settled','1',$5::jsonb,$6,$7,$8,$9,$10,$11,$11)`, webhookID, candidate.TenantID, candidate.MerchantID, candidate.IntentID, string(body), body, bodyHash[:], signingKey, sequence, candidate.IntentVersion+1, now)
			if err != nil {
				return err
			}
			rows, err := tx.Query(ctx, `SELECT id::text,signing_key_id FROM webhook_endpoints WHERE tenant_id=$1 AND merchant_id=$2 AND status='active' AND ('payment.settled'=ANY(event_types) OR '*'=ANY(event_types)) FOR UPDATE`, candidate.TenantID, candidate.MerchantID)
			if err != nil {
				return err
			}
			type endpoint struct{ id, key string }
			var endpoints []endpoint
			for rows.Next() {
				var endpoint endpoint
				if err := rows.Scan(&endpoint.id, &endpoint.key); err != nil {
					rows.Close()
					return err
				}
				endpoints = append(endpoints, endpoint)
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				return err
			}
			rows.Close()
			for _, endpoint := range endpoints {
				deliveryID, err := ids.New()
				if err != nil {
					return err
				}
				if _, err = tx.Exec(ctx, `INSERT INTO callback_deliveries(id,tenant_id,callback_event_id,endpoint_id,signing_key_id,status,attempt_count,next_attempt_at,created_at,updated_at,version) VALUES($1,$2,$3,$4,$5,'pending',0,$6,$6,$6,1)`, deliveryID, candidate.TenantID, webhookID, endpoint.id, endpoint.key, now); err != nil {
					return err
				}
			}
			outboxID, err := ids.New()
			if err != nil {
				return err
			}
			if err = insertOutbox(ctx, tx, outboxID, principal, candidate.IntentID, candidate.IntentVersion+1, "payment.settled", inboxID, body, now); err != nil {
				return err
			}
			result.Settled = true
			result.SettlementID = settlementID
			result.WebhookEventID = webhookID
			return nil
		}()
		if ingestErr != nil {
			return ingestErr
		}
		if prebindID != "" {
			if result.ProviderInboxID == "" {
				return domain.ErrStateConflict
			}
			command, err := tx.Exec(ctx, `UPDATE provider_prebind_inbox SET state='attached',attached_provider_inbox_id=$1,claim_token=NULL,claim_until=NULL,last_error_code=NULL,updated_at=clock_timestamp(),version=version+1 WHERE id=$2 AND tenant_id=$3 AND merchant_id=$4 AND state='pending' AND claim_token=$5 AND claim_until>=clock_timestamp()`, result.ProviderInboxID, prebindID, payment.TenantID, payment.MerchantID, claimToken)
			if err != nil {
				return err
			}
			if command.RowsAffected() != 1 {
				return domain.ErrVersionConflict
			}
		}
		return nil
	})
	return result, err
}

func ingestProviderPrebindEvidence(ctx context.Context, tx pgx.Tx, payment domain.VerifiedProviderPayment) (result domain.HostedSettlementResult, err error) {
	var id, reference, status, asset, amount, manifestID string
	var decimals uint8
	var configVersion int64
	var rawDigest, signatureDigest []byte
	err = tx.QueryRow(ctx, `SELECT id::text,provider_reference,provider_status,asset_id,amount_atomic::text,asset_decimals,raw_body_digest,signature_digest,config_manifest_id::text,config_version FROM provider_prebind_inbox WHERE provider_id=$1 AND provider_event_id=$2`, payment.ProviderID, payment.ProviderEventID).Scan(&id, &reference, &status, &asset, &amount, &decimals, &rawDigest, &signatureDigest, &manifestID, &configVersion)
	if err == nil {
		if !providerEvidenceMatches(payment, reference, status, asset, amount, decimals, rawDigest, signatureDigest, manifestID, configVersion) {
			return result, fmt.Errorf("%w: pre-bind provider event id was replayed with different canonical facts", domain.ErrIdempotencyConflict)
		}
		result.Duplicate = true
		result.Quarantined = true
		result.PrebindEvidenceID = id
		return result, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return result, err
	}
	id, err = ids.New()
	if err != nil {
		return result, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO provider_prebind_inbox(id,tenant_id,merchant_id,provider_id,provider_event_id,provider_reference,provider_status,asset_id,amount_atomic,asset_decimals,raw_body,raw_body_digest,signature_scheme,signature_key_id,signature_digest,config_manifest_id,config_version,provider_paused_at_receipt,provider_occurred_at,received_at,expires_at,state,next_attempt_at,created_at,updated_at,version) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9::numeric,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,'pending',$20,$20,$20,1)`, id, payment.TenantID, payment.MerchantID, payment.ProviderID, payment.ProviderEventID, payment.ProviderReference, payment.ProviderStatus, payment.AssetID, payment.Amount.String(), payment.AssetDecimals, payment.RawBody, payment.RawDigest[:], payment.SignatureScheme, payment.SignatureKeyID, payment.SignatureDigest[:], payment.ConfigManifestID, payment.ConfigVersion, payment.ProviderPaused, payment.OccurredAt.UTC(), payment.ReceivedAt.UTC(), payment.ReceivedAt.UTC().Add(7*24*time.Hour))
	if err != nil {
		return result, classify(err)
	}
	result.Quarantined = true
	result.PrebindEvidenceID = id
	return result, nil
}

func providerEvidenceMatches(payment domain.VerifiedProviderPayment, reference, status, asset, amount string, decimals uint8, rawDigest, signatureDigest []byte, manifestID string, configVersion int64) bool {
	return reference == payment.ProviderReference && status == payment.ProviderStatus && asset == payment.AssetID && amount == payment.Amount.String() && decimals == payment.AssetDecimals && bytes.Equal(rawDigest, payment.RawDigest[:]) && bytes.Equal(signatureDigest, payment.SignatureDigest[:]) && manifestID == payment.ConfigManifestID && configVersion == payment.ConfigVersion
}

func providerCallbackShouldQuarantine(payment domain.VerifiedProviderPayment) bool {
	return payment.ProviderPaused
}

func recordPausedProviderCallbackIncident(ctx context.Context, tx pgx.Tx, candidate hostedSettlementCandidate, inboxID string, evidence []byte, now time.Time) error {
	incidentID, err := ids.New()
	if err != nil {
		return err
	}
	command, err := tx.Exec(ctx, `INSERT INTO hosted_provider_runtime_incidents(id,tenant_id,merchant_id,provider_id,create_attempt_id,provider_order_id,provider_reference,incident_kind,evidence_digest,status,created_at,version)
SELECT $1,po.tenant_id,po.merchant_id,po.provider_id,NULL,po.id,po.provider_reference,'provider_callback_quarantined',$2,'open',$3,1 FROM provider_orders po WHERE po.id=$4 AND po.tenant_id=$5 AND po.merchant_id=$6 ON CONFLICT(provider_order_id,incident_kind,evidence_digest) DO NOTHING`, incidentID, evidence, now, candidate.ProviderOrderID, candidate.TenantID, candidate.MerchantID)
	if err != nil || command.RowsAffected() == 0 {
		return err
	}
	payload, _ := json.Marshal(map[string]string{"incident_id": incidentID, "provider_order_id": candidate.ProviderOrderID, "provider_inbox_id": inboxID, "payment_intent_id": candidate.IntentID, "incident_kind": "provider_callback_quarantined", "status": "open"})
	outboxID, err := ids.New()
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO outbox_events(id,tenant_id,merchant_id,aggregate_type,aggregate_id,aggregate_version,aggregate_sequence,event_type,schema_version,payload,correlation_id,causation_id,occurred_at,recorded_at,available_at) VALUES($1,$2,$3,'hosted_provider_runtime_incident',$4,1,1,'provider.callback_quarantined','1',$5::jsonb,$6,$6,$7,$7,$7)`, outboxID, candidate.TenantID, candidate.MerchantID, incidentID, payload, inboxID, now)
	return err
}

func loadHostedSettlementCandidate(ctx context.Context, tx pgx.Tx, payment domain.VerifiedProviderPayment) (candidate hostedSettlementCandidate, err error) {
	var expected, routeExpected, routeAsset, intentAmount, routeStatus, intentStatus string
	var routeDecimals uint8
	err = tx.QueryRow(ctx, `SELECT po.id::text,po.route_id::text,r.intent_id::text,po.tenant_id::text,po.merchant_id::text,i.merchant_order_id,po.provider_reference,po.provider_status,po.asset_id,i.currency::text,po.amount_atomic::text,r.expected_amount_atomic::text,r.asset_id,i.amount_minor::text,po.asset_decimals,r.asset_decimals,i.version,r.status::text,i.status::text,po.last_provider_occurred_at,COALESCE(po.last_provider_event_id,'')
FROM provider_orders po JOIN payment_routes r ON r.id=po.route_id AND r.tenant_id=po.tenant_id JOIN payment_intents i ON i.id=r.intent_id AND i.tenant_id=r.tenant_id
WHERE po.provider_id=$1 AND po.provider_reference=$2 AND po.tenant_id=$3 AND po.merchant_id=$4 FOR UPDATE OF po,r,i`, payment.ProviderID, payment.ProviderReference, payment.TenantID, payment.MerchantID).Scan(&candidate.ProviderOrderID, &candidate.RouteID, &candidate.IntentID, &candidate.TenantID, &candidate.MerchantID, &candidate.MerchantOrderID, &candidate.ProviderReference, &candidate.ProviderStatus, &candidate.AssetID, &candidate.Currency, &expected, &routeExpected, &routeAsset, &intentAmount, &candidate.AssetDecimals, &routeDecimals, &candidate.IntentVersion, &routeStatus, &intentStatus, &candidate.LastOccurredAt, &candidate.LastEventID)
	if errors.Is(err, pgx.ErrNoRows) {
		return candidate, domain.ErrNotFound
	}
	if err != nil {
		return candidate, err
	}
	candidate.Expected, err = money.Parse(expected)
	if err != nil {
		return candidate, err
	}
	candidate.IntentAmount, err = money.Parse(intentAmount)
	if err != nil {
		return candidate, err
	}
	candidate.RouteStatus = domain.RouteStatus(routeStatus)
	candidate.IntentStatus = domain.IntentStatus(intentStatus)
	candidate.EconomicDrift = expected != routeExpected || candidate.AssetID != routeAsset || candidate.AssetDecimals != routeDecimals
	return candidate, nil
}

func providerEventIsOutOfOrder(candidate hostedSettlementCandidate, payment domain.VerifiedProviderPayment) bool {
	if candidate.LastOccurredAt != nil {
		if payment.OccurredAt.Before(*candidate.LastOccurredAt) || (payment.OccurredAt.Equal(*candidate.LastOccurredAt) && payment.ProviderEventID <= candidate.LastEventID) {
			return true
		}
	}
	if (candidate.ProviderStatus == "paid" || candidate.ProviderStatus == "refunded" || candidate.ProviderStatus == "cancelled" || candidate.ProviderStatus == "failed") && (payment.ProviderStatus == "pending" || payment.ProviderStatus == "authorized") {
		return true
	}
	return false
}

func providerIncidentDecision(candidate hostedSettlementCandidate, payment domain.VerifiedProviderPayment) (kind string, outOfOrder bool) {
	if candidate.ProviderStatus == "refunded" && payment.ProviderStatus == "paid" {
		return "paid_after_refund", true
	}
	if payment.ProviderStatus == "refunded" {
		if candidate.IntentStatus == domain.IntentSettled || candidate.IntentStatus == domain.IntentOverpaid {
			return "refund_after_settlement", false
		}
		return "refund_before_settlement", false
	}
	return "", false
}

func recordProviderReconciliationIncident(ctx context.Context, tx pgx.Tx, candidate hostedSettlementCandidate, inboxID, kind string, now time.Time) error {
	incidentID, err := ids.New()
	if err != nil {
		return err
	}
	command, err := tx.Exec(ctx, `INSERT INTO provider_reconciliation_incidents(id,tenant_id,merchant_id,provider_id,provider_order_id,provider_inbox_id,incident_kind,status,created_at,version) SELECT $1,$2,$3,po.provider_id,po.id,$4,$5,'open',$6,1 FROM provider_orders po WHERE po.id=$7 AND po.tenant_id=$2 AND po.merchant_id=$3 ON CONFLICT(provider_inbox_id) DO NOTHING`, incidentID, candidate.TenantID, candidate.MerchantID, inboxID, kind, now, candidate.ProviderOrderID)
	if err != nil {
		return err
	}
	if command.RowsAffected() == 0 {
		return nil
	}
	payload, err := json.Marshal(map[string]any{"incident_id": incidentID, "provider_order_id": candidate.ProviderOrderID, "provider_inbox_id": inboxID, "payment_intent_id": candidate.IntentID, "incident_kind": kind, "status": "open"})
	if err != nil {
		return err
	}
	outboxID, err := ids.New()
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO outbox_events(id,tenant_id,merchant_id,aggregate_type,aggregate_id,aggregate_version,aggregate_sequence,event_type,schema_version,payload,correlation_id,causation_id,occurred_at,recorded_at,available_at) VALUES($1,$2,$3,'provider_reconciliation_incident',$4,1,1,'provider.reconciliation_required','1',$5::jsonb,$6,$6,$7,$7,$7)`, outboxID, candidate.TenantID, candidate.MerchantID, incidentID, payload, inboxID, now)
	return err
}
