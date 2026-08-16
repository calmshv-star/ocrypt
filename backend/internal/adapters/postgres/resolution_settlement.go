package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/application"
	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/ids"
	"github.com/calmshv-star/ocrypt/backend/internal/money"
	"github.com/calmshv-star/ocrypt/backend/internal/webhook"
	"github.com/jackc/pgx/v5"
)

type manualSettlementContext struct {
	TenantID, MerchantID, IntentID, RouteID, MerchantOrderID, Currency string
	IntentStatus                                                       domain.IntentStatus
	IntentVersion                                                      int64
	AmountMinor, Expected                                              money.Amount
	ChainID, AssetID, Address                                          string
	RequiredFinality                                                   uint64
	ExpiresAt, GraceEndsAt                                             time.Time
}

// ApplyFinalizedResolution is a serializable, lease-fenced financial boundary.
// It reloads the canonical scanner event and rechecks its evidence, the operator
// exception choices, route, finality and current state before posting a balanced
// settlement. AI ranking is intentionally absent and cannot authorize it.
func (s *Store) ApplyFinalizedResolution(ctx context.Context, resolution domain.ManualResolution, verified domain.TransferEvent) error {
	if !ids.Valid(resolution.ID) || !ids.Valid(resolution.ClaimToken) {
		return fmt.Errorf("%w: resolution lease identity is invalid", domain.ErrValidation)
	}
	return pgx.BeginTxFunc(ctx, s.db.pool, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx pgx.Tx) error {
		stored, financial, expected, err := loadManualSettlement(ctx, tx, resolution.ID, resolution.ClaimToken)
		if err != nil {
			return err
		}
		if stored.RequestedBy != resolution.RequestedBy || stored.ApprovedBy != resolution.ApprovedBy || stored.TargetRouteID != resolution.TargetRouteID || stored.TransferEventID != resolution.TransferEventID || stored.CandidateSetVersion != resolution.CandidateSetVersion || stored.AcceptShortfall != resolution.AcceptShortfall || stored.AcceptLatePayment != resolution.AcceptLatePayment || stored.AcceptCrossAsset != resolution.AcceptCrossAsset || stored.Reason != resolution.Reason {
			return fmt.Errorf("%w: claimed resolution facts changed", domain.ErrInvariantViolation)
		}
		expectedKey, _ := expected.Identity.Key()
		verifiedKey, err := verified.Identity.Key()
		if err != nil || expectedKey != verifiedKey || verified.ID != expected.ID || verified.Kind != expected.Kind || verified.FromAddress != expected.FromAddress || verified.Amount.Cmp(expected.Amount) != 0 || verified.AssetDecimals != expected.AssetDecimals || verified.BlockHash != expected.BlockHash || verified.BlockHeight != expected.BlockHeight || !verified.OnChainTime.Equal(expected.OnChainTime) || verified.Status != domain.TransferFinalized || verified.EvidenceHash != expected.EvidenceHash {
			return fmt.Errorf("%w: supplied scanner event disagrees with canonical transfer", domain.ErrInvariantViolation)
		}
		evidence, err := hex.DecodeString(verified.EvidenceHash)
		if err != nil || len(evidence) != sha256.Size || hex.EncodeToString(evidence) != verified.EvidenceHash {
			return fmt.Errorf("%w: scanner evidence must be lowercase SHA-256 hex", domain.ErrValidation)
		}
		if verified.Confirmations < financial.RequiredFinality {
			return fmt.Errorf("%w: transfer has not reached required finality", domain.ErrStateConflict)
		}
		if verified.Identity.ChainID != financial.ChainID || verified.Identity.ToAddress != financial.Address {
			return fmt.Errorf("%w: transfer does not target the selected route", domain.ErrInvariantViolation)
		}
		sameAsset := verified.Identity.AssetID == financial.AssetID
		if !sameAsset && !stored.AcceptCrossAsset {
			return fmt.Errorf("%w: cross-asset settlement was not approved", domain.ErrValidation)
		}
		if sameAsset && verified.Amount.Cmp(financial.Expected) < 0 && !stored.AcceptShortfall {
			return fmt.Errorf("%w: payment shortfall was not approved", domain.ErrValidation)
		}
		if verified.OnChainTime.After(financial.ExpiresAt) && !stored.AcceptLatePayment {
			return fmt.Errorf("%w: late payment was not approved", domain.ErrValidation)
		}
		var alreadyMatched bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM payment_matches WHERE event_id=$1 AND state<>'reversed')`, verified.ID).Scan(&alreadyMatched); err != nil {
			return err
		}
		if alreadyMatched {
			return domain.ErrStateConflict
		}
		settlementID, err := ids.New()
		if err != nil {
			return err
		}
		matchID, err := ids.New()
		if err != nil {
			return err
		}
		now := s.now()
		kind := "manual"
		if stored.AcceptCrossAsset {
			kind = "cross_asset_override"
		}
		matchEvidence, _ := json.Marshal(map[string]any{"manual_resolution_id": stored.ID, "candidate_set_version": stored.CandidateSetVersion, "requested_by": stored.RequestedBy, "approved_by": stored.ApprovedBy, "reason": stored.Reason, "accept_shortfall": stored.AcceptShortfall, "accept_late_payment": stored.AcceptLatePayment, "accept_cross_asset": stored.AcceptCrossAsset, "scanner_evidence_hash": verified.EvidenceHash})
		_, err = tx.Exec(ctx, `INSERT INTO payment_matches (id,tenant_id,event_id,route_id,intent_id,match_kind,expected_atomic,received_atomic,credited_atomic,state,evidence,policy_version,created_at,finalized_at) VALUES ($1,$2,$3,$4,$5,$6,$7::numeric,$8::numeric,$8::numeric,'finalized',$9::jsonb,1,$10,$10)`, matchID, financial.TenantID, verified.ID, financial.RouteID, financial.IntentID, kind, financial.Expected.String(), verified.Amount.String(), matchEvidence, now)
		if err != nil {
			return classify(err)
		}
		candidate := settlementCandidate{TenantID: financial.TenantID, MerchantID: financial.MerchantID, IntentID: financial.IntentID, RouteID: financial.RouteID, MerchantOrderID: financial.MerchantOrderID, Currency: financial.Currency, AmountMinor: financial.AmountMinor, IntentVersion: financial.IntentVersion, Expected: financial.Expected, RequiredFinality: financial.RequiredFinality}
		debitID, creditID, err := ensureSettlementAccounts(ctx, tx, candidate, verified.Identity.AssetID)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO ledger_transactions (id,tenant_id,business_type,business_reference,effective_at,booked_at,correlation_id,policy_version) VALUES ($1::uuid,$2,'payment_settlement',$1::uuid::text,$3,$4,$5,1)`, settlementID, financial.TenantID, verified.OnChainTime, now, verified.ID)
		if err != nil {
			return classify(err)
		}
		_, err = tx.Exec(ctx, `INSERT INTO ledger_entries (transaction_id,tenant_id,sequence,account_id,asset_id,direction,amount_atomic,created_at) VALUES ($1,$2,1,$3,$4,'debit',$5::numeric,$6),($1,$2,2,$7,$4,'credit',$5::numeric,$6)`, settlementID, financial.TenantID, debitID, verified.Identity.AssetID, verified.Amount.String(), now, creditID)
		if err != nil {
			return err
		}
		newStatus := domain.IntentSettled
		eventType := "payment.settled"
		if verified.Identity.AssetID == financial.AssetID && verified.Amount.Cmp(financial.Expected) > 0 {
			newStatus = domain.IntentOverpaid
			eventType = "payment.overpaid"
		}
		command, err := tx.Exec(ctx, `UPDATE payment_intents SET status=$1,status_reason='manual_scanner_resolution',settled_at=$2,updated_at=$2,version=version+1 WHERE id=$3 AND tenant_id=$4 AND version=$5 AND status IN ('pending','observed','partially_paid','confirmed','expired','needs_review','reorg_review')`, newStatus, now, financial.IntentID, financial.TenantID, financial.IntentVersion)
		if err != nil {
			return err
		}
		if command.RowsAffected() != 1 {
			return domain.ErrVersionConflict
		}
		_, err = tx.Exec(ctx, `UPDATE payment_routes SET status='settled',updated_at=$1,version=version+1 WHERE id=$2 AND tenant_id=$3 AND status IN ('active','expired')`, now, financial.RouteID, financial.TenantID)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE amount_reservations SET state='consumed',release_reason='manual_settlement',updated_at=$1,version=version+1 WHERE route_id=$2 AND tenant_id=$3 AND state='active'`, now, financial.RouteID, financial.TenantID)
		if err != nil {
			return err
		}
		command, err = tx.Exec(ctx, `UPDATE manual_resolutions SET status='resolved',verifier_evidence_hash=$1,completed_at=$2,updated_at=$2,locked_by=NULL,locked_until=NULL,lease_token=NULL,last_error=NULL,version=version+1 WHERE id=$3 AND lease_token=$4 AND locked_until>clock_timestamp()`, evidence, now, stored.ID, stored.ClaimToken)
		if err != nil {
			return err
		}
		if command.RowsAffected() != 1 {
			return domain.ErrVersionConflict
		}
		_, err = tx.Exec(ctx, `UPDATE unmatched_payments SET status='resolved',selected_route_id=$1,updated_at=$2,version=version+1 WHERE id=$3 AND tenant_id=$4`, financial.RouteID, now, stored.UnmatchedPaymentID, financial.TenantID)
		if err != nil {
			return err
		}
		return insertManualSettlementNotifications(ctx, tx, financial, verified, settlementID, eventType, newStatus, now)
	})
}

func loadManualSettlement(ctx context.Context, tx pgx.Tx, resolutionID, claimToken string) (domain.ManualResolution, manualSettlementContext, domain.TransferEvent, error) {
	var resolution domain.ManualResolution
	var financial manualSettlementContext
	var expected domain.TransferEvent
	var resolutionStatus, intentStatus, amountMinor, expectedAmount, transferAmount, transferHeight, transferStatus string
	var evidence []byte
	err := tx.QueryRow(ctx, `SELECT mr.id::text,mr.unmatched_id::text,mr.event_id::text,mr.target_route_id::text,mr.candidate_set_version,mr.idempotency_key,mr.requested_by::text,COALESCE(mr.approved_by::text,''),mr.accept_shortfall,mr.accept_late_payment,mr.accept_cross_asset,mr.human_reason,mr.status::text,mr.created_at,mr.version,mr.attempt_count,
r.tenant_id::text,r.merchant_id::text,r.intent_id::text,r.id::text,r.chain_id,r.asset_id,r.receiving_address,r.expected_amount_atomic::text,r.required_finality,r.expires_at,r.grace_ends_at,
i.merchant_order_id,i.currency,i.amount_minor::text,i.status::text,i.version,
te.id::text,te.chain_id,te.transaction_id,te.event_identity,te.asset_id,te.to_address,te.event_kind,te.from_address,te.amount_atomic::text,te.asset_decimals,te.block_height::text,te.block_hash,te.on_chain_time,te.confirmations,te.status::text,te.parser_version,te.evidence_hash
FROM manual_resolutions mr
JOIN payment_routes r ON r.id=mr.target_route_id AND r.tenant_id=mr.tenant_id
JOIN payment_intents i ON i.id=r.intent_id AND i.tenant_id=r.tenant_id
JOIN transfer_events te ON te.id=mr.event_id
JOIN match_candidates c ON c.tenant_id=mr.tenant_id AND c.unmatched_id=mr.unmatched_id AND c.route_id=mr.target_route_id AND c.candidate_set_version=mr.candidate_set_version AND cardinality(c.disqualifiers)=0
WHERE mr.id=$1 AND mr.lease_token=$2 AND mr.locked_until>clock_timestamp() AND mr.status IN ('verification_requested','verification_retry')
AND c.candidate_set_version=(SELECT max(latest.candidate_set_version) FROM match_candidates latest WHERE latest.tenant_id=mr.tenant_id AND latest.unmatched_id=mr.unmatched_id)
FOR UPDATE OF mr,r,i,te`, resolutionID, claimToken).Scan(
		&resolution.ID, &resolution.UnmatchedPaymentID, &resolution.TransferEventID, &resolution.TargetRouteID, &resolution.CandidateSetVersion, &resolution.IdempotencyKey, &resolution.RequestedBy, &resolution.ApprovedBy, &resolution.AcceptShortfall, &resolution.AcceptLatePayment, &resolution.AcceptCrossAsset, &resolution.Reason, &resolutionStatus, &resolution.CreatedAt, &resolution.Version, &resolution.Attempt,
		&financial.TenantID, &financial.MerchantID, &financial.IntentID, &financial.RouteID, &financial.ChainID, &financial.AssetID, &financial.Address, &expectedAmount, &financial.RequiredFinality, &financial.ExpiresAt, &financial.GraceEndsAt,
		&financial.MerchantOrderID, &financial.Currency, &amountMinor, &intentStatus, &financial.IntentVersion,
		&expected.ID, &expected.Identity.ChainID, &expected.Identity.TransactionID, &expected.Identity.EventIndex, &expected.Identity.AssetID, &expected.Identity.ToAddress, &expected.Kind, &expected.FromAddress, &transferAmount, &expected.AssetDecimals, &transferHeight, &expected.BlockHash, &expected.OnChainTime, &expected.Confirmations, &transferStatus, &expected.ParserVersion, &evidence)
	if errors.Is(err, pgx.ErrNoRows) {
		return resolution, financial, expected, domain.ErrVersionConflict
	}
	if err != nil {
		return resolution, financial, expected, err
	}
	resolution.Status = domain.UnmatchedStatus(resolutionStatus)
	resolution.ClaimToken = claimToken
	financial.IntentStatus = domain.IntentStatus(intentStatus)
	expected.Status = domain.TransferStatus(transferStatus)
	expected.EvidenceHash = hex.EncodeToString(evidence)
	if financial.AmountMinor, err = money.Parse(amountMinor); err != nil {
		return resolution, financial, expected, err
	}
	if financial.Expected, err = money.Parse(expectedAmount); err != nil {
		return resolution, financial, expected, err
	}
	if expected.Amount, err = money.Parse(transferAmount); err != nil {
		return resolution, financial, expected, err
	}
	if expected.BlockHeight, err = strconv.ParseUint(transferHeight, 10, 64); err != nil {
		return resolution, financial, expected, err
	}
	return resolution, financial, expected, nil
}

func insertManualSettlementNotifications(ctx context.Context, tx pgx.Tx, financial manualSettlementContext, event domain.TransferEvent, settlementID, eventType string, status domain.IntentStatus, now time.Time) error {
	webhookID, err := ids.New()
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, financial.MerchantID); err != nil {
		return err
	}
	sequence, err := nextMerchantEventSequence(ctx, tx, financial.TenantID, financial.MerchantID)
	if err != nil {
		return err
	}
	body, err := webhook.CanonicalBody(webhook.Event{EventID: webhookID, EventType: eventType, SchemaVersion: "1", Sequence: sequence, OccurredAt: now, MerchantID: financial.MerchantID, Livemode: true, PaymentIntent: webhook.PaymentIntentSnapshot{ID: financial.IntentID, MerchantOrderID: financial.MerchantOrderID, Status: string(status), AmountMinor: financial.AmountMinor, Currency: financial.Currency}, Settlement: &webhook.Settlement{SettlementID: settlementID, AssetID: event.Identity.AssetID, Network: event.Identity.ChainID, ExpectedRaw: financial.Expected, ReceivedRaw: event.Amount, CreditedRaw: event.Amount, TransactionHash: event.Identity.TransactionID, EventIndex: event.Identity.EventIndex, BlockHeight: strconv.FormatUint(event.BlockHeight, 10), BlockTime: event.OnChainTime, Finality: "finalized", ManualResolution: true}})
	if err != nil {
		return err
	}
	bodyHash := sha256.Sum256(body)
	var signingKey string
	if err = tx.QueryRow(ctx, `SELECT COALESCE(min(signing_key_id),'unconfigured') FROM webhook_endpoints WHERE merchant_id=$1 AND status='active'`, financial.MerchantID).Scan(&signingKey); err != nil {
		return err
	}
	aggregateVersion := financial.IntentVersion + 1
	_, err = tx.Exec(ctx, `INSERT INTO callback_events (id,tenant_id,merchant_id,intent_id,event_type,schema_version,canonical_payload,canonical_body,body_hash,signing_key_id,merchant_sequence,aggregate_sequence,occurred_at,created_at) VALUES ($1,$2,$3,$4,$5,'1',$6::jsonb,$7,$8,$9,$10,$11,$12,$12)`, webhookID, financial.TenantID, financial.MerchantID, financial.IntentID, eventType, string(body), body, bodyHash[:], signingKey, sequence, aggregateVersion, now)
	if err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT id::text,signing_key_id FROM webhook_endpoints WHERE merchant_id=$1 AND status='active' AND ($2=ANY(event_types) OR '*'=ANY(event_types)) FOR UPDATE`, financial.MerchantID, eventType)
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
		if _, err = tx.Exec(ctx, `INSERT INTO callback_deliveries (id,tenant_id,callback_event_id,endpoint_id,signing_key_id,status,attempt_count,next_attempt_at,created_at,updated_at,version) VALUES ($1,$2,$3,$4,$5,'pending',0,$6,$6,$6,1)`, deliveryID, financial.TenantID, webhookID, endpoint.id, endpoint.signingKeyID, now); err != nil {
			return err
		}
	}
	outboxID, err := ids.New()
	if err != nil {
		return err
	}
	principal := application.Principal{TenantID: financial.TenantID, MerchantID: financial.MerchantID}
	return insertOutbox(ctx, tx, outboxID, principal, financial.IntentID, aggregateVersion, eventType, settlementID, body, now)
}

var _ application.ResolutionQueueStore = (*Store)(nil)
