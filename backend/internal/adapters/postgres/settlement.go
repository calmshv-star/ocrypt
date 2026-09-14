package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/application"
	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/ids"
	"github.com/calmshv-star/ocrypt/backend/internal/money"
	"github.com/calmshv-star/ocrypt/backend/internal/webhook"
	"github.com/jackc/pgx/v5"
)

type settlementCandidate struct {
	TenantID, MerchantID, IntentID, RouteID, MerchantOrderID, Currency string
	Livemode                                                           bool
	AmountMinor                                                        money.Amount
	IntentVersion                                                      int64
	Expected                                                           money.Amount
	RequiredFinality                                                   uint64
}

type potentialRoute struct {
	TenantID string
	Route    domain.PaymentRoute
}

// ExactRecoveryTarget confines an operator recovery to one previously missing
// native transfer and one exact route. It is not a manual matching override.
type ExactRecoveryTarget struct {
	IntentID, RouteID string
	Identity          domain.EventIdentity
	Amount            money.Amount
	AssetDecimals     uint8
}

type exactRecoveryStore struct {
	store    *Store
	expected ExactRecoveryTarget
}

func (s *Store) ForExactRecovery(expected ExactRecoveryTarget) (application.TransferSettlementStore, error) {
	if s == nil || !ids.Valid(expected.IntentID) || !ids.Valid(expected.RouteID) || expected.Identity.Validate() != nil || expected.Amount.IsZero() {
		return nil, fmt.Errorf("%w: exact recovery target is incomplete", domain.ErrValidation)
	}
	if !(expected.Identity.ChainID == "ton:mainnet" && expected.AssetDecimals == 9) && !(strings.HasPrefix(expected.Identity.ChainID, "eip155:") && expected.AssetDecimals == 18) {
		return nil, fmt.Errorf("%w: unsupported native recovery asset precision", domain.ErrValidation)
	}
	return exactRecoveryStore{store: s, expected: expected}, nil
}

func (s exactRecoveryStore) IngestAndSettle(ctx context.Context, event domain.TransferEvent) (application.SettlementResult, error) {
	return s.store.ingestAndSettle(ctx, event, &s.expected)
}

func (expected ExactRecoveryTarget) validateEvent(event domain.TransferEvent) error {
	if event.Identity != expected.Identity || event.Amount.Cmp(expected.Amount) != 0 || event.AssetDecimals != expected.AssetDecimals || event.Status != domain.TransferFinalized {
		return fmt.Errorf("%w: recovery transfer differs from the explicit target", domain.ErrStateConflict)
	}
	canonicalNative := event.Kind == "native_top_level" && event.Identity.EventIndex == "native:0"
	if event.Kind == "native_internal" && strings.HasPrefix(event.Identity.EventIndex, "trace:") {
		canonicalNative = true
		for _, part := range strings.Split(strings.TrimPrefix(event.Identity.EventIndex, "trace:"), ",") {
			index, err := strconv.ParseUint(part, 10, 32)
			if err != nil || strconv.FormatUint(index, 10) != part {
				canonicalNative = false
				break
			}
		}
	}
	if (expected.AssetDecimals == 18 && !canonicalNative) || (expected.AssetDecimals == 9 && event.Kind != "native_message") {
		return fmt.Errorf("%w: recovery requires a canonical native transfer", domain.ErrStateConflict)
	}
	return nil
}

func (expected ExactRecoveryTarget) validateCandidates(event domain.TransferEvent, candidates []settlementCandidate) error {
	if len(candidates) != 1 || candidates[0].IntentID != expected.IntentID || candidates[0].RouteID != expected.RouteID || candidates[0].Expected.Cmp(expected.Amount) != 0 {
		return fmt.Errorf("%w: recovery no longer has the intended unique exact route", domain.ErrStateConflict)
	}
	if event.Confirmations < candidates[0].RequiredFinality {
		return fmt.Errorf("%w: recovery does not satisfy route finality", domain.ErrStateConflict)
	}
	return nil
}

func (expected ExactRecoveryTarget) validateResult(result application.SettlementResult) error {
	if result.Outcome != application.SettlementSettled || result.PaymentIntentID != expected.IntentID || result.PaymentRouteID != expected.RouteID {
		return fmt.Errorf("%w: recovery did not settle the explicit target; rolling back", domain.ErrStateConflict)
	}
	return nil
}

func validateRecoveryNativeAsset(ctx context.Context, tx pgx.Tx, expected ExactRecoveryTarget, event domain.TransferEvent) error {
	var valid bool
	// These reads share the SERIALIZABLE snapshot with the locked candidate and
	// every settlement write. A token asset cannot be relabelled as native by CLI.
	err := tx.QueryRow(ctx, `SELECT EXISTS(
SELECT 1 FROM payment_routes r JOIN assets a ON a.id=r.asset_id AND a.chain_id=r.chain_id
WHERE r.id=$1 AND r.intent_id=$2 AND r.chain_id=$3 AND r.asset_id=$4
AND a.kind='native' AND a.decimals=$5 AND r.asset_decimals=$5
AND $6 BETWEEN r.starts_at AND r.expires_at)`, expected.RouteID, expected.IntentID, expected.Identity.ChainID, expected.Identity.AssetID, expected.AssetDecimals, event.OnChainTime).Scan(&valid)
	if err != nil {
		return fmt.Errorf("recovery native asset validation: %w", err)
	}
	if !valid {
		return fmt.Errorf("%w: recovery native asset, route precision or payment window mismatch", domain.ErrStateConflict)
	}
	return nil
}

// IngestAndSettle is the financial transaction boundary used by chain workers.
// The worker database role is intentionally separate from merchant API roles and
// must have the narrowly scoped BYPASSRLS grants required for cross-tenant routing.
func (s *Store) IngestAndSettle(ctx context.Context, event domain.TransferEvent) (result application.SettlementResult, err error) {
	return s.ingestAndSettle(ctx, event, nil)
}

func (s *Store) ingestAndSettle(ctx context.Context, event domain.TransferEvent, expected *ExactRecoveryTarget) (result application.SettlementResult, err error) {
	err = pgx.BeginTxFunc(ctx, s.db.pool, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx pgx.Tx) (txErr error) {
		// This executes before BeginTxFunc can commit, including on every early
		// return below. A failed recovery never commits an unmatched/other order.
		defer func() {
			if expected != nil && txErr == nil {
				txErr = expected.validateResult(result)
			}
		}()
		if expected != nil {
			if err := expected.validateEvent(event); err != nil {
				return err
			}
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM transfer_events WHERE chain_id=$1 AND transaction_id=$2 AND event_identity=$3 AND asset_id=$4 AND to_address=$5)`, event.Identity.ChainID, event.Identity.TransactionID, event.Identity.EventIndex, event.Identity.AssetID, event.Identity.ToAddress).Scan(&exists); err != nil {
				return err
			}
			if exists {
				return fmt.Errorf("%w: recovery transfer already exists", domain.ErrStateConflict)
			}
		}
		actionable, eventID, canonical, err := insertCanonicalTransfer(ctx, tx, event)
		if err != nil {
			return err
		}
		event = canonical
		result.TransferEventID = eventID
		if !actionable {
			if expected != nil {
				return fmt.Errorf("%w: recovery transfer is a duplicate", domain.ErrStateConflict)
			}
			result.Outcome = application.SettlementDuplicate
			return nil
		}
		candidates, err := findSettlementCandidates(ctx, tx, event)
		if err != nil {
			return err
		}
		if expected != nil {
			if err := expected.validateCandidates(event, candidates); err != nil {
				return err
			}
			if err := validateRecoveryNativeAsset(ctx, tx, *expected, event); err != nil {
				return err
			}
		}
		if event.Status != domain.TransferFinalized {
			if len(candidates) == 1 {
				candidate := candidates[0]
				result.PaymentIntentID = candidate.IntentID
				result.PaymentRouteID = candidate.RouteID
				if _, err := advancePaymentObservation(ctx, tx, candidate, event, s.now()); err != nil {
					return err
				}
			}
			result.Outcome = application.SettlementObserved
			return nil
		}
		var alreadyClassified bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM payment_matches WHERE event_id=$1 AND state<>'reversed') OR EXISTS(SELECT 1 FROM unmatched_payments WHERE event_id=$1 AND status<>'reorged')`, eventID).Scan(&alreadyClassified); err != nil {
			return err
		}
		if alreadyClassified {
			if expected != nil {
				return fmt.Errorf("%w: recovery transfer is already classified", domain.ErrStateConflict)
			}
			result.Outcome = application.SettlementDuplicate
			return nil
		}
		if len(candidates) != 1 {
			classification := "no_exact_route"
			result.Outcome = application.SettlementUnmatched
			if len(candidates) > 1 {
				classification = "ambiguous_exact_routes"
				result.Outcome = application.SettlementAmbiguous
			}
			dust := false
			if len(candidates) == 0 {
				dust, err = belowUnmatchedDustThreshold(ctx, tx, event)
				if err != nil {
					return err
				}
			}
			if dust {
				unmatchedID, err := ids.New()
				if err != nil {
					return err
				}
				_, err = tx.Exec(ctx, `INSERT INTO unmatched_payments
(id,tenant_id,event_id,classification,status,workflow_version,severity,created_at,updated_at,version)
VALUES($1,NULL,$2,'dust_below_asset_threshold','ignored',1,'low',clock_timestamp(),clock_timestamp(),1)
ON CONFLICT(event_id) DO NOTHING`, unmatchedID, eventID)
				if err != nil {
					return err
				}
				result.Outcome = application.SettlementIgnored
				return nil
			}
			potential, tenantID, potentialClassification, err := findPotentialCandidates(ctx, tx, event, s.now())
			if err != nil {
				return err
			}
			if potentialClassification != "" {
				classification = potentialClassification
			}
			unmatchedID, err := ids.New()
			if err != nil {
				return err
			}
			workflowStatus := "new"
			if len(potential) > 0 && tenantID != "" {
				workflowStatus = "candidates_ready"
			}
			var effectiveUnmatchedID string
			err = tx.QueryRow(ctx, `INSERT INTO unmatched_payments (id,tenant_id,event_id,classification,status,workflow_version,severity,created_at,updated_at,version) VALUES ($1,NULLIF($2,'')::uuid,$3,$4,$5,1,$6,clock_timestamp(),clock_timestamp(),1) ON CONFLICT (event_id) DO UPDATE SET tenant_id=EXCLUDED.tenant_id,classification=EXCLUDED.classification,status=EXCLUDED.status,severity=EXCLUDED.severity,updated_at=clock_timestamp(),version=unmatched_payments.version+1 WHERE unmatched_payments.status='reorged' RETURNING id::text`, unmatchedID, tenantID, eventID, classification, workflowStatus, map[bool]string{true: "critical", false: "high"}[len(candidates) > 1 || classification == "cross_tenant_ambiguous"]).Scan(&effectiveUnmatchedID)
			if err != nil {
				return err
			}
			if tenantID != "" {
				automatic, autoEligible, err := automaticSettlementCandidate(ctx, tx, potential)
				if err != nil {
					return err
				}
				if autoEligible {
					potential[0] = automatic
				}
				for rank, candidate := range potential {
					candidateID, err := ids.New()
					if err != nil {
						return err
					}
					evidence, err := json.Marshal(map[string]any{"class": candidate.Class, "amount_delta": candidate.AmountDelta, "reason_codes": candidate.Reasons, "deterministic": true})
					if err != nil {
						return err
					}
					_, err = tx.Exec(ctx, `INSERT INTO match_candidates (id,tenant_id,unmatched_id,route_id,rank,score,evidence,disqualifiers,candidate_set_version,created_at) VALUES ($1,$2,$3,$4,$5,$6,$7::jsonb,'{}',1,clock_timestamp()) ON CONFLICT DO NOTHING`, candidateID, tenantID, effectiveUnmatchedID, candidate.RouteID, rank+1, candidate.Score, evidence)
					if err != nil {
						return err
					}
				}
				if autoEligible {
					if err := recordExceptionIntent(ctx, tx, tenantID, potential[0], event, s.now()); err != nil {
						return err
					}
					// A unique score above 80 enters deterministic settlement. The
					// reducer still enforces finality, identity, five-percent
					// underpayment tolerance and excess-payment accounting.
					if err := enqueueAutomatedMatchingCandidates(ctx, tx, tenantID, []application.Candidate{automatic}, s.now()); err != nil {
						return err
					}
				} else if len(potential) > 0 {
					if err := recordExceptionIntent(ctx, tx, tenantID, potential[0], event, s.now()); err != nil {
						return err
					}
				}
			}
			return nil
		}
		candidate := candidates[0]
		if event.Confirmations < candidate.RequiredFinality {
			if _, err := advancePaymentObservation(ctx, tx, candidate, event, s.now()); err != nil {
				return err
			}
			result.Outcome = application.SettlementObserved
			return nil
		}
		observation, err := advancePaymentObservation(ctx, tx, candidate, event, s.now())
		if err != nil {
			return err
		}
		result.PaymentIntentID = candidate.IntentID
		result.PaymentRouteID = candidate.RouteID
		settlementID, err := ids.New()
		if err != nil {
			return err
		}
		matchID, err := ids.New()
		if err != nil {
			return err
		}
		ledgerID := settlementID
		webhookID, err := ids.New()
		if err != nil {
			return err
		}
		now := s.now()
		_, err = tx.Exec(ctx, `INSERT INTO payment_matches (id,tenant_id,event_id,route_id,intent_id,match_kind,expected_atomic,received_atomic,credited_atomic,state,evidence,policy_version,created_at,finalized_at) VALUES ($1,$2,$3,$4,$5,'exact',$6::numeric,$7::numeric,$7::numeric,'finalized',$8::jsonb,1,$9,$9)`, matchID, candidate.TenantID, eventID, candidate.RouteID, candidate.IntentID, candidate.Expected.String(), event.Amount.String(), []byte(`{"deterministic":true,"rule":"exact_route_v1"}`), now)
		if err != nil {
			return classify(err)
		}
		debitID, creditID, err := ensureSettlementAccounts(ctx, tx, candidate, event.Identity.AssetID)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO ledger_transactions (id,tenant_id,business_type,business_reference,effective_at,booked_at,correlation_id,policy_version) VALUES ($1,$2,'payment_settlement',$3,$4,$5,$6,1)`, ledgerID, candidate.TenantID, settlementID, event.OnChainTime, now, eventID)
		if err != nil {
			return classify(err)
		}
		_, err = tx.Exec(ctx, `INSERT INTO ledger_entries (transaction_id,tenant_id,sequence,account_id,asset_id,direction,amount_atomic,created_at) VALUES ($1,$2,1,$3,$4,'debit',$5::numeric,$6),($1,$2,2,$7,$4,'credit',$5::numeric,$6)`, ledgerID, candidate.TenantID, debitID, event.Identity.AssetID, event.Amount.String(), now, creditID)
		if err != nil {
			return err
		}
		command, err := tx.Exec(ctx, `UPDATE payment_intents SET status='settled',status_reason='exact_finalized_transfer',settled_at=$1,updated_at=$1,version=version+1 WHERE id=$2 AND tenant_id=$3 AND version=$4 AND status IN ('pending','observed','partially_paid','confirmed','expired','needs_review','reorg_review')`, now, candidate.IntentID, candidate.TenantID, candidate.IntentVersion)
		if err != nil {
			return err
		}
		if command.RowsAffected() != 1 {
			return domain.ErrVersionConflict
		}
		_, err = tx.Exec(ctx, `UPDATE payment_routes SET status='settled',updated_at=$1,version=version+1 WHERE id=$2 AND tenant_id=$3 AND status IN ('active','expired')`, now, candidate.RouteID, candidate.TenantID)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE payment_routes SET status='superseded',updated_at=$1,version=version+1 WHERE intent_id=$2 AND tenant_id=$3 AND id<>$4 AND status IN ('active','expired')`, now, candidate.IntentID, candidate.TenantID, candidate.RouteID); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE amount_reservations ar SET state='released',release_reason='sibling_settled',updated_at=$1,version=ar.version+1 FROM payment_routes r WHERE ar.route_id=r.id AND ar.tenant_id=r.tenant_id AND r.intent_id=$2 AND r.id<>$3 AND r.tenant_id=$4 AND ar.state='active'`, now, candidate.IntentID, candidate.RouteID, candidate.TenantID); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE provider_orders po SET provider_status='superseded',updated_at=$1,version=po.version+1 FROM payment_routes r WHERE po.route_id=r.id AND po.tenant_id=r.tenant_id AND r.intent_id=$2 AND r.id<>$3 AND r.tenant_id=$4 AND po.provider_status IN ('pending','authorized','cancel_requested')`, now, candidate.IntentID, candidate.RouteID, candidate.TenantID); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE amount_reservations SET state='consumed',release_reason='settled',updated_at=$1,version=version+1 WHERE route_id=$2 AND tenant_id=$3 AND state='active'`, now, candidate.RouteID, candidate.TenantID)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, candidate.MerchantID); err != nil {
			return err
		}
		sequence, err := nextMerchantEventSequence(ctx, tx, candidate.TenantID, candidate.MerchantID)
		if err != nil {
			return err
		}
		body, err := webhook.CanonicalBody(webhook.Event{EventID: webhookID, EventType: "payment.settled", SchemaVersion: "1", Sequence: sequence, OccurredAt: now, MerchantID: candidate.MerchantID, Livemode: candidate.Livemode, PaymentIntent: webhook.PaymentIntentSnapshot{ID: candidate.IntentID, MerchantOrderID: candidate.MerchantOrderID, Status: "settled", AmountMinor: candidate.AmountMinor, Currency: candidate.Currency}, Settlement: &webhook.Settlement{SettlementID: settlementID, AssetID: event.Identity.AssetID, Network: event.Identity.ChainID, ExpectedRaw: candidate.Expected, ReceivedRaw: event.Amount, CreditedRaw: event.Amount, TransactionHash: event.Identity.TransactionID, EventIndex: event.Identity.EventIndex, BlockHeight: fmt.Sprintf("%d", event.BlockHeight), BlockTime: event.OnChainTime, Finality: "finalized", ManualResolution: false}})
		if err != nil {
			return err
		}
		bodyHash := sha256.Sum256(body)
		var signingKey string
		if err = tx.QueryRow(ctx, `SELECT COALESCE(min(signing_key_id),'unconfigured') FROM webhook_endpoints WHERE tenant_id=$1 AND merchant_id=$2 AND status='active'`, candidate.TenantID, candidate.MerchantID).Scan(&signingKey); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO callback_events (id,tenant_id,merchant_id,intent_id,event_type,schema_version,canonical_payload,canonical_body,body_hash,signing_key_id,merchant_sequence,aggregate_sequence,occurred_at,created_at) VALUES ($1,$2,$3,$4,'payment.settled','1',$5::jsonb,$6,$7,$8,$9,$10,$11,$11)`, webhookID, candidate.TenantID, candidate.MerchantID, candidate.IntentID, string(body), body, bodyHash[:], signingKey, sequence, candidate.IntentVersion+1, now)
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT id::text,signing_key_id FROM webhook_endpoints WHERE tenant_id=$1 AND merchant_id=$2 AND status='active' AND ('payment.settled'=ANY(event_types) OR '*'=ANY(event_types)) FOR UPDATE`, candidate.TenantID, candidate.MerchantID)
		if err != nil {
			return err
		}
		type deliveryEndpoint struct{ id, signingKeyID string }
		var endpoints []deliveryEndpoint
		for rows.Next() {
			var endpoint deliveryEndpoint
			if err := rows.Scan(&endpoint.id, &endpoint.signingKeyID); err != nil {
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
			if _, err = tx.Exec(ctx, `INSERT INTO callback_deliveries (id,tenant_id,callback_event_id,endpoint_id,signing_key_id,status,attempt_count,next_attempt_at,created_at,updated_at,version) VALUES ($1,$2,$3,$4,$5,'pending',0,$6,$6,$6,1)`, deliveryID, candidate.TenantID, webhookID, endpoint.id, endpoint.signingKeyID, now); err != nil {
				return err
			}
		}
		outboxID, err := ids.New()
		if err != nil {
			return err
		}
		principal := application.Principal{TenantID: candidate.TenantID, MerchantID: candidate.MerchantID}
		if err = insertOutbox(ctx, tx, outboxID, principal, candidate.IntentID, candidate.IntentVersion+1, "payment.settled", eventID, body, now); err != nil {
			return err
		}
		if err = linkObservationEvent(ctx, tx, observation, "payment.settled", webhookID, outboxID, now); err != nil {
			return err
		}
		result.Outcome = application.SettlementSettled
		result.SettlementID = settlementID
		result.WebhookEventID = webhookID
		return nil
	})
	return result, err
}

// belowUnmatchedDustThreshold is evaluated only after exact settlement has
// failed. A configured threshold therefore suppresses wallet fee rebates and
// similar economic dust without ever hiding a valid exact payment route.
func belowUnmatchedDustThreshold(ctx context.Context, tx pgx.Tx, event domain.TransferEvent) (bool, error) {
	var thresholdText string
	var plausiblePayment bool
	if err := tx.QueryRow(ctx, `SELECT a.dust_threshold::text,
EXISTS(
  SELECT 1
  FROM payment_routes r
  JOIN payment_intents i ON i.id=r.intent_id AND i.tenant_id=r.tenant_id
  JOIN payment_route_policy_bindings b ON b.route_id=r.id AND b.tenant_id=r.tenant_id
  WHERE r.provider='on_chain'
    AND r.chain_id=$1
    AND r.asset_id=$2
    AND r.receiving_address=$3
    AND r.status IN ('active','expired')
    AND i.status IN ('pending','observed','partially_paid','confirmed','expired','needs_review','reorg_review')
    AND (
      $5::timestamptz BETWEEN r.starts_at AND r.expires_at
      OR (
        COALESCE((b.policy_snapshot->>'accept_late_within_grace')::boolean,false)
        AND $5::timestamptz>r.expires_at
        AND $5::timestamptz<=r.grace_ends_at
      )
    )
    AND $4::numeric*10000 >= r.expected_amount_atomic*(
      10000-LEAST(10000,GREATEST(0,COALESCE((b.policy_snapshot->>'underpayment_tolerance_bps')::numeric,0)))
    )
)
FROM assets a
WHERE a.chain_id=$1 AND a.id=$2`, event.Identity.ChainID, event.Identity.AssetID, event.Identity.ToAddress, event.Amount.String(), event.OnChainTime).Scan(&thresholdText, &plausiblePayment); err != nil {
		return false, err
	}
	threshold, err := money.Parse(thresholdText)
	if err != nil {
		return false, err
	}
	return !plausiblePayment && !threshold.IsZero() && event.Amount.Cmp(threshold) <= 0, nil
}

func insertCanonicalTransfer(ctx context.Context, tx pgx.Tx, event domain.TransferEvent) (bool, string, domain.TransferEvent, error) {
	evidence, decodeErr := hex.DecodeString(event.EvidenceHash)
	if decodeErr != nil || len(evidence) != sha256.Size || hex.EncodeToString(evidence) != event.EvidenceHash {
		return false, "", domain.TransferEvent{}, fmt.Errorf("%w: invalid transfer evidence hash", domain.ErrValidation)
	}
	command, err := tx.Exec(ctx, `INSERT INTO transfer_events (id,chain_id,transaction_id,event_identity,event_kind,asset_id,from_address,to_address,amount_atomic,asset_decimals,block_hash,block_height,on_chain_time,confirmations,status,parser_version,evidence_hash,created_at,updated_at,version) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9::numeric,$10,$11,$12::numeric,$13,$14,$15,$16,$17,clock_timestamp(),clock_timestamp(),1) ON CONFLICT (chain_id,transaction_id,event_identity,asset_id,to_address) DO NOTHING`, event.ID, event.Identity.ChainID, event.Identity.TransactionID, event.Identity.EventIndex, event.Kind, event.Identity.AssetID, event.FromAddress, event.Identity.ToAddress, event.Amount.String(), event.AssetDecimals, event.BlockHash, fmt.Sprintf("%d", event.BlockHeight), event.OnChainTime, event.Confirmations, event.Status, event.ParserVersion, evidence)
	if err != nil {
		return false, "", domain.TransferEvent{}, err
	}
	if command.RowsAffected() == 1 {
		return true, event.ID, event, nil
	}
	canonical := event
	var existingID, kind, from, amount, blockHash, height, status, parser string
	var decimals uint8
	var confirmations uint64
	var onChainTime time.Time
	var existingEvidence []byte
	err = tx.QueryRow(ctx, `SELECT id::text,event_kind,from_address,amount_atomic::text,asset_decimals,block_hash,block_height::text,on_chain_time,confirmations,status::text,parser_version,evidence_hash FROM transfer_events WHERE chain_id=$1 AND transaction_id=$2 AND event_identity=$3 AND asset_id=$4 AND to_address=$5 FOR UPDATE`, event.Identity.ChainID, event.Identity.TransactionID, event.Identity.EventIndex, event.Identity.AssetID, event.Identity.ToAddress).Scan(&existingID, &kind, &from, &amount, &decimals, &blockHash, &height, &onChainTime, &confirmations, &status, &parser, &existingEvidence)
	if err != nil {
		return false, "", domain.TransferEvent{}, err
	}
	blockHeight, parseErr := strconv.ParseUint(height, 10, 64)
	if parseErr != nil {
		return false, "", domain.TransferEvent{}, parseErr
	}
	if kind != event.Kind || from != event.FromAddress || amount != event.Amount.String() || decimals != event.AssetDecimals || parser != event.ParserVersion {
		return false, "", domain.TransferEvent{}, fmt.Errorf("%w: duplicate transfer identity has different canonical facts", domain.ErrInvariantViolation)
	}
	if domain.TransferStatus(status) == domain.TransferReorged {
		if event.Status != domain.TransferObserved && event.Status != domain.TransferConfirmed && event.Status != domain.TransferFinalized {
			return false, "", domain.TransferEvent{}, fmt.Errorf("%w: re-included transfer has invalid finality", domain.ErrStateConflict)
		}
		command, err := tx.Exec(ctx, `UPDATE transfer_events SET block_hash=$1,block_height=$2::numeric,on_chain_time=$3,confirmations=$4,status=$5,evidence_hash=$6,updated_at=clock_timestamp(),version=version+1 WHERE id=$7 AND status='reorged'`, event.BlockHash, fmt.Sprintf("%d", event.BlockHeight), event.OnChainTime, event.Confirmations, event.Status, evidence, existingID)
		if err != nil {
			return false, "", domain.TransferEvent{}, err
		}
		if command.RowsAffected() != 1 {
			return false, "", domain.TransferEvent{}, domain.ErrVersionConflict
		}
		canonical.ID = existingID
		return true, existingID, canonical, nil
	}
	if blockHash != event.BlockHash || blockHeight != event.BlockHeight || !onChainTime.Equal(event.OnChainTime) {
		return false, "", domain.TransferEvent{}, fmt.Errorf("%w: duplicate transfer identity has different canonical inclusion facts", domain.ErrInvariantViolation)
	}
	if !bytes.Equal(existingEvidence, evidence) {
		if !matchesLegacyNativeEVMEvidence(event, existingEvidence) {
			return false, "", domain.TransferEvent{}, fmt.Errorf("%w: duplicate transfer identity has different canonical inclusion facts", domain.ErrInvariantViolation)
		}
		var nativeAsset bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM assets WHERE id=$1 AND chain_id=$2 AND kind='native' AND decimals=18)`, event.Identity.AssetID, event.Identity.ChainID).Scan(&nativeAsset); err != nil {
			return false, "", domain.TransferEvent{}, err
		}
		if !nativeAsset {
			return false, "", domain.TransferEvent{}, fmt.Errorf("%w: legacy EVM evidence requires a native asset", domain.ErrInvariantViolation)
		}
		// Keep the original canonical evidence, including the hash already in
		// immutable observations and emitted webhooks. Only confirmation/finality
		// progress below may be written; the ordinary classified-event guard
		// prevents a second match, ledger transaction or settlement notification.
		canonical.EvidenceHash = hex.EncodeToString(existingEvidence)
		canonical.NativeEVMRawEvidence = nil
	}
	canonical.ID = existingID
	canonical.Status = domain.TransferStatus(status)
	canonical.Confirmations = confirmations
	newStatus, newConfirmations, progressed, mergeErr := mergeTransferProgress(domain.TransferStatus(status), confirmations, event.Status, event.Confirmations)
	if mergeErr != nil {
		return false, "", domain.TransferEvent{}, mergeErr
	}
	if progressed {
		command, err := tx.Exec(ctx, `UPDATE transfer_events SET status=$1,confirmations=$2,updated_at=clock_timestamp(),version=version+1 WHERE id=$3 AND status=$4 AND confirmations=$5`, newStatus, newConfirmations, existingID, status, confirmations)
		if err != nil {
			return false, "", domain.TransferEvent{}, err
		}
		if command.RowsAffected() != 1 {
			return false, "", domain.TransferEvent{}, domain.ErrVersionConflict
		}
		canonical.Status = newStatus
		canonical.Confirmations = newConfirmations
		// Confirmation growth is actionable even when the adapter's coarse
		// finality label is unchanged. The observation aggregate decides whether
		// the growth crossed a policy milestone; stale/decreasing reports remain
		// non-actionable and cannot create callback spam.
		return true, existingID, canonical, nil
	}
	return false, existingID, canonical, nil
}

// These field orders match the two version-1 provider evidence encodings. The
// compatibility proof must bind BOTH hashes to the same complete receipt; a
// caller-supplied alternative digest alone is never sufficient.
type nativeEVMCompatibilityTransaction struct {
	Hash             string `json:"hash"`
	From             string `json:"from"`
	To               string `json:"to"`
	Value            string `json:"value"`
	TransactionIndex string `json:"transactionIndex"`
}

type nativeEVMCompatibilityReceipt struct {
	TransactionHash  string          `json:"transactionHash"`
	BlockHash        string          `json:"blockHash"`
	BlockNumber      string          `json:"blockNumber"`
	Status           string          `json:"status"`
	TransactionIndex string          `json:"transactionIndex"`
	Logs             json.RawMessage `json:"logs"`
}

func matchesLegacyNativeEVMEvidence(event domain.TransferEvent, storedEvidence []byte) bool {
	if !strings.HasPrefix(event.Identity.ChainID, "eip155:") || event.Kind != "native_top_level" || event.Identity.EventIndex != "native:0" || event.ParserVersion != "evm-v1" || event.AssetDecimals != 18 || event.Status != domain.TransferFinalized || len(storedEvidence) != sha256.Size || len(event.NativeEVMRawEvidence) == 0 || len(event.NativeEVMRawEvidence) > 1<<20 {
		return false
	}
	if chain, err := strconv.ParseUint(strings.TrimPrefix(event.Identity.ChainID, "eip155:"), 10, 64); err != nil || chain == 0 {
		return false
	}
	var proof struct {
		Transaction nativeEVMCompatibilityTransaction `json:"transaction"`
		Receipt     nativeEVMCompatibilityReceipt     `json:"receipt"`
	}
	decoder := json.NewDecoder(bytes.NewReader(event.NativeEVMRawEvidence))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&proof) != nil {
		return false
	}
	canonical, err := json.Marshal(proof)
	if err != nil || !bytes.Equal(canonical, event.NativeEVMRawEvidence) {
		return false // Also rejects duplicate/missing keys and trailing JSON.
	}
	currentHash := sha256.Sum256(canonical)
	if hex.EncodeToString(currentHash[:]) != event.EvidenceHash {
		return false
	}
	tx, receipt := proof.Transaction, proof.Receipt
	if !sameNativeEVMHex(tx.Hash, event.Identity.TransactionID, 32) || !sameNativeEVMHex(receipt.TransactionHash, event.Identity.TransactionID, 32) || !sameNativeEVMHex(tx.From, event.FromAddress, 20) || !sameNativeEVMHex(tx.To, event.Identity.ToAddress, 20) || !sameNativeEVMHex(receipt.BlockHash, event.BlockHash, 32) || receipt.Status != "0x1" {
		return false
	}
	height, heightErr := nativeEVMHexUint64(receipt.BlockNumber)
	txIndex, txIndexErr := nativeEVMHexUint64(tx.TransactionIndex)
	receiptIndex, receiptIndexErr := nativeEVMHexUint64(receipt.TransactionIndex)
	if heightErr != nil || height != event.BlockHeight || txIndexErr != nil || receiptIndexErr != nil || txIndex != receiptIndex || !strings.HasPrefix(tx.Value, "0x") {
		return false
	}
	value, ok := new(big.Int).SetString(strings.TrimPrefix(tx.Value, "0x"), 16)
	if !ok || value.Sign() <= 0 || value.BitLen() > 256 || value.String() != event.Amount.String() {
		return false
	}
	legacy, err := json.Marshal(struct {
		Receipt nativeEVMCompatibilityReceipt `json:"receipt"`
	}{Receipt: receipt})
	if err != nil {
		return false
	}
	legacyHash := sha256.Sum256(legacy)
	return bytes.Equal(legacyHash[:], storedEvidence)
}

func sameNativeEVMHex(left, right string, size int) bool {
	if len(left) != 2+size*2 || len(right) != 2+size*2 || !strings.HasPrefix(left, "0x") || !strings.HasPrefix(right, "0x") || !strings.EqualFold(left, right) {
		return false
	}
	_, err := hex.DecodeString(left[2:])
	return err == nil
}

func nativeEVMHexUint64(value string) (uint64, error) {
	if !strings.HasPrefix(value, "0x") {
		return 0, errors.New("invalid EVM hex quantity")
	}
	return strconv.ParseUint(value[2:], 16, 64)
}

func mergeTransferProgress(current domain.TransferStatus, confirmations uint64, reported domain.TransferStatus, reportedConfirmations uint64) (domain.TransferStatus, uint64, bool, error) {
	if current != reported && !domain.CanTransitionTransfer(current, reported) {
		return current, confirmations, false, fmt.Errorf("%w: invalid transfer finality transition from %s to %s", domain.ErrStateConflict, current, reported)
	}
	resultStatus := current
	if current != reported {
		resultStatus = reported
	}
	resultConfirmations := confirmations
	if reportedConfirmations > resultConfirmations {
		resultConfirmations = reportedConfirmations
	}
	return resultStatus, resultConfirmations, resultStatus != current || resultConfirmations != confirmations, nil
}

func recordExceptionIntent(ctx context.Context, tx pgx.Tx, tenantID string, candidate application.Candidate, event domain.TransferEvent, now time.Time) error {
	status := domain.IntentNeedsReview
	eventType := "payment.needs_review"
	if candidate.Class == application.ExceptionPartial {
		status = domain.IntentPartiallyPaid
		eventType = "payment.partially_paid"
	}
	var merchantID, orderID, currency, amount string
	var version int64
	err := tx.QueryRow(ctx, `UPDATE payment_intents i SET status=$1,status_reason=$2,updated_at=$3,version=i.version+1
FROM payment_routes r WHERE i.id=$4 AND i.tenant_id=$5 AND r.id=$6 AND r.intent_id=i.id AND r.tenant_id=i.tenant_id
AND i.status IN ('pending','observed','partially_paid','confirmed','expired','needs_review')
RETURNING i.merchant_id::text,i.merchant_order_id,i.currency,i.amount_minor::text,i.version`, status, string(candidate.Class), now, candidate.IntentID, tenantID, candidate.RouteID).Scan(&merchantID, &orderID, &currency, &amount, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	amountMinor, err := money.Parse(amount)
	if err != nil {
		return err
	}
	webhookID, err := ids.New()
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, merchantID); err != nil {
		return err
	}
	sequence, err := nextMerchantEventSequence(ctx, tx, tenantID, merchantID)
	if err != nil {
		return err
	}
	body, err := webhook.CanonicalBody(webhook.Event{EventID: webhookID, EventType: eventType, SchemaVersion: "1", Sequence: sequence, OccurredAt: now, MerchantID: merchantID, Livemode: true, PaymentIntent: webhook.PaymentIntentSnapshot{ID: candidate.IntentID, MerchantOrderID: orderID, Status: string(status), AmountMinor: amountMinor, Currency: currency}})
	if err != nil {
		return err
	}
	bodyHash := sha256.Sum256(body)
	var signingKey string
	if err = tx.QueryRow(ctx, `SELECT COALESCE(min(signing_key_id),'unconfigured') FROM webhook_endpoints WHERE merchant_id=$1 AND status='active'`, merchantID).Scan(&signingKey); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO callback_events (id,tenant_id,merchant_id,intent_id,event_type,schema_version,canonical_payload,canonical_body,body_hash,signing_key_id,merchant_sequence,aggregate_sequence,occurred_at,created_at) VALUES ($1,$2,$3,$4,$5,'1',$6::jsonb,$7,$8,$9,$10,$11,$12,$12)`, webhookID, tenantID, merchantID, candidate.IntentID, eventType, string(body), body, bodyHash[:], signingKey, sequence, version, now)
	if err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT id::text,signing_key_id FROM webhook_endpoints WHERE merchant_id=$1 AND status='active' AND ($2=ANY(event_types) OR '*'=ANY(event_types)) FOR UPDATE`, merchantID, eventType)
	if err != nil {
		return err
	}
	type deliveryEndpoint struct{ id, signingKeyID string }
	var endpoints []deliveryEndpoint
	for rows.Next() {
		var endpoint deliveryEndpoint
		if err := rows.Scan(&endpoint.id, &endpoint.signingKeyID); err != nil {
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
		if _, err = tx.Exec(ctx, `INSERT INTO callback_deliveries (id,tenant_id,callback_event_id,endpoint_id,signing_key_id,status,attempt_count,next_attempt_at,created_at,updated_at,version) VALUES ($1,$2,$3,$4,$5,'pending',0,$6,$6,$6,1)`, deliveryID, tenantID, webhookID, endpoint.id, endpoint.signingKeyID, now); err != nil {
			return err
		}
	}
	outboxID, err := ids.New()
	if err != nil {
		return err
	}
	return insertOutbox(ctx, tx, outboxID, application.Principal{TenantID: tenantID, MerchantID: merchantID}, candidate.IntentID, version, eventType, event.ID, body, now)
}

func findSettlementCandidates(ctx context.Context, tx pgx.Tx, event domain.TransferEvent) ([]settlementCandidate, error) {
	// Financial matching must wait for a briefly locked exact route. SKIP LOCKED
	// would misclassify a valid payment as permanently unmatched.
	rows, err := tx.Query(ctx, `SELECT r.tenant_id::text,r.merchant_id::text,r.intent_id::text,r.id::text,i.merchant_order_id,i.amount_minor::text,i.currency,(m.environment='live'),i.version,r.expected_amount_atomic::text,r.required_finality FROM payment_routes r JOIN payment_intents i ON i.id=r.intent_id AND i.tenant_id=r.tenant_id JOIN merchants m ON m.id=r.merchant_id AND m.tenant_id=r.tenant_id WHERE r.provider='on_chain' AND r.chain_id=$1 AND r.asset_id=$2 AND r.receiving_address=$3 AND r.expected_amount_atomic=$4::numeric AND r.status IN ('active','expired') AND i.status IN ('pending','observed','partially_paid','confirmed','expired','needs_review','reorg_review') AND ($5 BETWEEN r.starts_at AND r.expires_at OR EXISTS(SELECT 1 FROM payment_observations o WHERE o.transfer_event_id=$6 AND o.tenant_id=r.tenant_id AND o.route_id=r.id AND o.finality='reorged')) ORDER BY r.created_at,r.id LIMIT 2 FOR UPDATE OF r,i`, event.Identity.ChainID, event.Identity.AssetID, event.Identity.ToAddress, event.Amount.String(), event.OnChainTime, event.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []settlementCandidate
	for rows.Next() {
		var c settlementCandidate
		var amount, expected string
		if err := rows.Scan(&c.TenantID, &c.MerchantID, &c.IntentID, &c.RouteID, &c.MerchantOrderID, &amount, &c.Currency, &c.Livemode, &c.IntentVersion, &expected, &c.RequiredFinality); err != nil {
			return nil, err
		}
		c.AmountMinor, err = money.Parse(amount)
		if err != nil {
			return nil, err
		}
		c.Expected, err = money.Parse(expected)
		if err != nil {
			return nil, err
		}
		result = append(result, c)
	}
	return result, rows.Err()
}

func findPotentialCandidates(ctx context.Context, tx pgx.Tx, event domain.TransferEvent, now time.Time) ([]application.Candidate, string, string, error) {
	rows, err := tx.Query(ctx, `SELECT r.tenant_id::text,r.id::text,r.intent_id::text,r.chain_id,r.asset_id,r.expected_amount_atomic::text,r.receiving_address,r.status::text,r.starts_at,r.expires_at,r.grace_ends_at
FROM payment_routes r
JOIN payment_intents i ON i.id=r.intent_id AND i.tenant_id=r.tenant_id
WHERE r.chain_id=$1 AND r.receiving_address=$2 AND r.status IN ('active','expired')
  AND i.status IN ('pending','observed','partially_paid','confirmed','expired','needs_review','reorg_review')
  AND $3 BETWEEN r.starts_at - interval '24 hours' AND r.grace_ends_at + interval '24 hours'
ORDER BY r.created_at DESC,r.id LIMIT 100
FOR UPDATE OF r`, event.Identity.ChainID, event.Identity.ToAddress, event.OnChainTime)
	if err != nil {
		return nil, "", "", err
	}
	defer rows.Close()
	var routes []potentialRoute
	tenantSet := map[string]bool{}
	for rows.Next() {
		var item potentialRoute
		var amount, status string
		if err := rows.Scan(&item.TenantID, &item.Route.ID, &item.Route.IntentID, &item.Route.ChainID, &item.Route.AssetID, &amount, &item.Route.Address, &status, &item.Route.StartsAt, &item.Route.ExpiresAt, &item.Route.GraceEndsAt); err != nil {
			return nil, "", "", err
		}
		item.Route.ExpectedAmount, err = money.Parse(amount)
		if err != nil {
			return nil, "", "", err
		}
		item.Route.Status = domain.RouteStatus(status)
		routes = append(routes, item)
		tenantSet[item.TenantID] = true
	}
	if err := rows.Err(); err != nil {
		return nil, "", "", err
	}
	if len(tenantSet) > 1 {
		return nil, "", "cross_tenant_ambiguous", nil
	}
	if len(routes) == 0 {
		return nil, "", "no_candidate_route", nil
	}
	domainRoutes := make([]domain.PaymentRoute, 0, len(routes))
	for _, item := range routes {
		domainRoutes = append(domainRoutes, item.Route)
	}
	candidates := application.BuildCandidates(event, domainRoutes, now)
	classification := "no_candidate_route"
	if len(candidates) > 0 {
		classification = string(candidates[0].Class)
	}
	return candidates, routes[0].TenantID, classification, nil
}

func ensureSettlementAccounts(ctx context.Context, tx pgx.Tx, c settlementCandidate, assetID string) (string, string, error) {
	for _, code := range []string{"treasury_asset", "merchant_settlement_liability"} {
		id, err := ids.New()
		if err != nil {
			return "", "", err
		}
		accountType := "asset"
		if code == "merchant_settlement_liability" {
			accountType = "liability"
		}
		_, err = tx.Exec(ctx, `INSERT INTO ledger_accounts (id,tenant_id,merchant_id,asset_id,account_code,account_type,created_at) VALUES ($1,$2,$3,$4,$5,$6,clock_timestamp()) ON CONFLICT (tenant_id,merchant_id,asset_id,account_code) DO NOTHING`, id, c.TenantID, c.MerchantID, assetID, code, accountType)
		if err != nil {
			return "", "", err
		}
	}
	var debit, credit string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM ledger_accounts WHERE tenant_id=$1 AND merchant_id=$2 AND asset_id=$3 AND account_code='treasury_asset'`, c.TenantID, c.MerchantID, assetID).Scan(&debit); err != nil {
		return "", "", err
	}
	if err := tx.QueryRow(ctx, `SELECT id::text FROM ledger_accounts WHERE tenant_id=$1 AND merchant_id=$2 AND asset_id=$3 AND account_code='merchant_settlement_liability'`, c.TenantID, c.MerchantID, assetID).Scan(&credit); err != nil {
		return "", "", err
	}
	return debit, credit, nil
}

var _ application.TransferSettlementStore = (*Store)(nil)
var _ = errors.Is
