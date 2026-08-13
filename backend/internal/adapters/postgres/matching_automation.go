package postgres

import (
	"bytes"
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

type automatedMatchingRoute struct {
	TenantID, MerchantID, IntentID, RouteID, MerchantOrderID, Currency string
	AmountMinor                                                        money.Amount
	IntentVersion                                                      int64
	Route                                                              domain.PaymentRoute
	Policy                                                             application.AutomatedMatchingPolicy
}

func enqueueAutomatedMatchingCandidates(ctx context.Context, tx pgx.Tx, tenantID string, candidates []application.Candidate, now time.Time) error {
	seen := map[string]bool{}
	for _, candidate := range candidates {
		if seen[candidate.RouteID] || candidate.Class == application.ExceptionWrongAsset || candidate.Class == application.ExceptionAmbiguous || candidate.Class == application.ExceptionUnmatched {
			continue
		}
		seen[candidate.RouteID] = true
		_, err := tx.Exec(ctx, `INSERT INTO automated_matching_jobs
(route_id,tenant_id,merchant_id,status,next_attempt_at,attempt_count,reschedule_requested,created_at,updated_at)
SELECT r.id,r.tenant_id,r.merchant_id,'pending',$3,0,false,$3,$3
FROM payment_routes r JOIN payment_route_policy_bindings b ON b.route_id=r.id AND b.tenant_id=r.tenant_id
WHERE r.id=$1 AND r.tenant_id=$2
ON CONFLICT (route_id) DO UPDATE SET
 reschedule_requested=CASE WHEN automated_matching_jobs.status='leased' THEN true ELSE automated_matching_jobs.reschedule_requested END,
 status=CASE WHEN automated_matching_jobs.status='leased' THEN automated_matching_jobs.status ELSE 'pending' END,
 next_attempt_at=CASE WHEN automated_matching_jobs.status='leased' THEN automated_matching_jobs.next_attempt_at ELSE LEAST(automated_matching_jobs.next_attempt_at,EXCLUDED.next_attempt_at) END,
 last_error=CASE WHEN automated_matching_jobs.status='leased' THEN automated_matching_jobs.last_error ELSE NULL END,
 updated_at=EXCLUDED.updated_at`, candidate.RouteID, tenantID, now)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ClaimAutomatedMatching(ctx context.Context, workerID string, now time.Time, lease time.Duration, limit int) ([]application.AutomatedMatchingJob, error) {
	if workerID == "" || lease <= 0 || limit < 1 || limit > 500 {
		return nil, domain.ErrValidation
	}
	rows, err := s.db.pool.Query(ctx, `WITH claim AS (
 SELECT route_id FROM automated_matching_jobs
 WHERE status IN ('pending','retry') AND next_attempt_at<=$1
 ORDER BY next_attempt_at,route_id LIMIT $2 FOR UPDATE SKIP LOCKED
)
UPDATE automated_matching_jobs j SET status='leased',locked_by=$3,locked_until=$1+($4::bigint*interval '1 millisecond'),
 lease_token=gen_random_uuid(),attempt_count=attempt_count+1,updated_at=$1
FROM claim WHERE j.route_id=claim.route_id
RETURNING j.route_id::text,j.lease_token::text,j.attempt_count`, now.UTC(), limit, workerID, lease.Milliseconds())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []application.AutomatedMatchingJob
	for rows.Next() {
		var job application.AutomatedMatchingJob
		if err := rows.Scan(&job.RouteID, &job.LeaseToken, &job.Attempt); err != nil {
			return nil, err
		}
		result = append(result, job)
	}
	return result, rows.Err()
}

func (s *Store) RetryAutomatedMatching(ctx context.Context, workerID string, job application.AutomatedMatchingJob, next time.Time, reason string, dead bool) error {
	status := "retry"
	if dead {
		status = "dead_letter"
	}
	command, err := s.db.pool.Exec(ctx, `UPDATE automated_matching_jobs SET status=$1,next_attempt_at=$2,last_error=$3,
 locked_by=NULL,locked_until=NULL,lease_token=NULL,updated_at=clock_timestamp()
WHERE route_id=$4 AND status='leased' AND locked_by=$5 AND lease_token=$6 AND locked_until>clock_timestamp()`, status, next.UTC(), reason, job.RouteID, workerID, job.LeaseToken)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return domain.ErrVersionConflict
	}
	return nil
}

func (s *Store) ReconcileAutomatedMatching(ctx context.Context, workerID string, job application.AutomatedMatchingJob, now time.Time) error {
	return pgx.BeginTxFunc(ctx, s.db.pool, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx pgx.Tx) error {
		var leaseValid bool
		if err := tx.QueryRow(ctx, `SELECT true FROM automated_matching_jobs WHERE route_id=$1 AND status='leased' AND locked_by=$2 AND lease_token=$3 AND locked_until>clock_timestamp() FOR UPDATE`, job.RouteID, workerID, job.LeaseToken).Scan(&leaseValid); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return domain.ErrVersionConflict
			}
			return err
		}
		route, err := loadAutomatedMatchingRoute(ctx, tx, job.RouteID)
		if err != nil {
			return err
		}
		events, ambiguous, err := loadAutomatedMatchingEvents(ctx, tx, route)
		if err != nil {
			return err
		}
		decision, err := application.EvaluateAutomatedMatch(route.Route, events, now, route.Policy)
		if err != nil {
			return err
		}
		if ambiguous {
			decision = failClosedAmbiguousDecision(route, events)
		}
		aggregateID, err := upsertAutomatedAggregate(ctx, tx, route, decision, now)
		if err != nil {
			return err
		}
		if err := appendAutomatedDecision(ctx, tx, route, aggregateID, decision, now); err != nil {
			return err
		}
		switch decision.Outcome {
		case application.AutomatedCollect:
			if err := persistAutomatedAllocations(ctx, tx, route, aggregateID, decision, "proposed", now); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE unmatched_payments SET status='bound',selected_route_id=$1,updated_at=$2,version=version+1 WHERE event_id=ANY($3::uuid[]) AND tenant_id=$4 AND status IN ('new','candidates_ready','bound')`, route.RouteID, now, allocationEventIDs(decision.Allocations), route.TenantID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE payment_intents SET status='partially_paid',status_reason='deterministic_aggregate_collecting',updated_at=$1,version=version+1 WHERE id=$2 AND tenant_id=$3 AND status IN ('pending','observed','partially_paid','needs_review')`, now, route.IntentID, route.TenantID); err != nil {
				return err
			}
			deadline := route.Route.ExpiresAt
			if route.Policy.AcceptLateWithinGrace {
				deadline = route.Route.GraceEndsAt
			}
			return finishAutomatedMatchingJob(ctx, tx, workerID, job, "pending", deadline, "")
		case application.AutomatedReview:
			if _, err := tx.Exec(ctx, `UPDATE payment_matches SET state='reversed',reversed_at=$1 WHERE aggregate_id=$2 AND tenant_id=$3 AND state='proposed'`, now, aggregateID, route.TenantID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE unmatched_payments SET status='candidates_ready',selected_route_id=NULL,updated_at=$1,version=version+1 WHERE event_id=ANY($2::uuid[]) AND tenant_id=$3 AND status='bound'`, now, allocationEventIDs(decision.Allocations), route.TenantID); err != nil {
				return err
			}
			return finishAutomatedMatchingJob(ctx, tx, workerID, job, "completed", now, "manual_review_required")
		case application.AutomatedSettle:
			if err := persistAutomatedAllocations(ctx, tx, route, aggregateID, decision, "finalized", now); err != nil {
				return err
			}
			if err := postAutomatedSettlementLedger(ctx, tx, route, aggregateID, decision, now); err != nil {
				return err
			}
			if err := finalizeAutomatedIntent(ctx, tx, route, aggregateID, decision, now); err != nil {
				return err
			}
			return finishAutomatedMatchingJob(ctx, tx, workerID, job, "completed", now, "")
		default:
			return domain.ErrInvariantViolation
		}
	})
}

func loadAutomatedMatchingRoute(ctx context.Context, tx pgx.Tx, routeID string) (automatedMatchingRoute, error) {
	var result automatedMatchingRoute
	var amountMinor, expected, status string
	var snapshot, storedHash []byte
	err := tx.QueryRow(ctx, `SELECT r.tenant_id::text,r.merchant_id::text,r.intent_id::text,r.id::text,
i.merchant_order_id,i.amount_minor::text,i.currency,i.version,r.chain_id,r.asset_id,r.provider,
r.expected_amount_atomic::text,r.asset_decimals,r.display_amount,r.receiving_address,COALESCE(r.memo,''),
r.required_finality,r.status::text,r.version,r.starts_at,r.expires_at,r.grace_ends_at,b.policy_snapshot::text,b.config_hash
FROM payment_routes r JOIN payment_intents i ON i.id=r.intent_id AND i.tenant_id=r.tenant_id
JOIN payment_route_policy_bindings b ON b.route_id=r.id AND b.tenant_id=r.tenant_id
WHERE r.id=$1 FOR UPDATE OF r,i`, routeID).Scan(&result.TenantID, &result.MerchantID, &result.IntentID, &result.RouteID,
		&result.MerchantOrderID, &amountMinor, &result.Currency, &result.IntentVersion, &result.Route.ChainID, &result.Route.AssetID, &result.Route.Provider,
		&expected, &result.Route.AssetDecimals, &result.Route.DisplayAmount, &result.Route.Address, &result.Route.Memo,
		&result.Route.RequiredFinality, &status, &result.Route.Version, &result.Route.StartsAt, &result.Route.ExpiresAt, &result.Route.GraceEndsAt, &snapshot, &storedHash)
	if err != nil {
		return result, err
	}
	hash := sha256.Sum256(snapshot)
	if !bytes.Equal(hash[:], storedHash) {
		return result, fmt.Errorf("%w: bound matching policy digest mismatch", domain.ErrInvariantViolation)
	}
	if err := json.Unmarshal(snapshot, &result.Policy); err != nil {
		return result, fmt.Errorf("decode matching policy: %w", err)
	}
	if err := result.Policy.Validate(); err != nil {
		return result, err
	}
	result.Route.ID, result.Route.IntentID, result.Route.Status = result.RouteID, result.IntentID, domain.RouteStatus(status)
	result.AmountMinor, err = money.Parse(amountMinor)
	if err != nil {
		return result, err
	}
	result.Route.ExpectedAmount, err = money.Parse(expected)
	return result, err
}

func loadAutomatedMatchingEvents(ctx context.Context, tx pgx.Tx, route automatedMatchingRoute) ([]domain.TransferEvent, bool, error) {
	rows, err := tx.Query(ctx, `WITH direct AS (
 SELECT te.transaction_id FROM transfer_events te
 WHERE te.chain_id=$1 AND te.asset_id=$2 AND te.to_address=$3 AND te.event_kind<>'gasfree_fee'
   AND te.status='finalized' AND te.confirmations>=$4 AND te.on_chain_time BETWEEN $5 AND $6
)
SELECT te.id::text,te.chain_id,te.transaction_id,te.event_identity,te.asset_id,te.to_address,te.event_kind,
 te.from_address,te.amount_atomic::text,te.asset_decimals,te.block_height::text,te.block_hash,te.on_chain_time,
 te.confirmations,te.status::text,te.parser_version,encode(te.evidence_hash,'hex')
FROM transfer_events te
WHERE ((te.chain_id=$1 AND te.asset_id=$2 AND te.to_address=$3 AND te.event_kind<>'gasfree_fee'
         AND te.status='finalized' AND te.confirmations>=$4 AND te.on_chain_time BETWEEN $5 AND $6)
    OR (te.chain_id=$1 AND te.asset_id=$2 AND te.event_kind='gasfree_fee' AND te.transaction_id IN (SELECT transaction_id FROM direct)
         AND te.status='finalized' AND te.confirmations>=$4))
  AND NOT EXISTS (SELECT 1 FROM payment_matches pm WHERE pm.event_id=te.id AND pm.state<>'reversed' AND pm.route_id<>$7)
ORDER BY te.on_chain_time,te.id FOR UPDATE OF te`, route.Route.ChainID, route.Route.AssetID, route.Route.Address, route.Route.RequiredFinality, route.Route.StartsAt, route.Route.GraceEndsAt, route.RouteID)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	var events []domain.TransferEvent
	for rows.Next() {
		var event domain.TransferEvent
		var amount, height, status string
		if err := rows.Scan(&event.ID, &event.Identity.ChainID, &event.Identity.TransactionID, &event.Identity.EventIndex, &event.Identity.AssetID, &event.Identity.ToAddress, &event.Kind,
			&event.FromAddress, &amount, &event.AssetDecimals, &height, &event.BlockHash, &event.OnChainTime, &event.Confirmations, &status, &event.ParserVersion, &event.EvidenceHash); err != nil {
			return nil, false, err
		}
		var parseErr error
		if event.Amount, parseErr = money.Parse(amount); parseErr != nil {
			return nil, false, parseErr
		}
		if event.BlockHeight, parseErr = strconv.ParseUint(height, 10, 64); parseErr != nil {
			return nil, false, parseErr
		}
		event.Status = domain.TransferStatus(status)
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	var ambiguous bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(
 SELECT 1 FROM transfer_events te
 JOIN payment_routes other ON other.chain_id=te.chain_id AND other.asset_id=te.asset_id AND other.receiving_address=te.to_address
 JOIN payment_route_policy_bindings b ON b.route_id=other.id AND b.tenant_id=other.tenant_id
 WHERE te.chain_id=$1 AND te.asset_id=$2 AND te.to_address=$3 AND te.event_kind<>'gasfree_fee'
   AND te.status='finalized' AND te.confirmations>=$4 AND te.on_chain_time BETWEEN $5 AND $6
   AND other.id<>$7 AND other.status IN ('active','expired') AND te.on_chain_time BETWEEN other.starts_at AND other.grace_ends_at
   -- A payment made inside this route's primary window must not be made
   -- ambiguous solely by an older route's observation grace.  Grace remains
   -- relevant when the payment itself is late, and simultaneous primary
   -- windows still fail closed.
   AND (te.on_chain_time>=$8 OR te.on_chain_time BETWEEN other.starts_at AND other.expires_at)
)`, route.Route.ChainID, route.Route.AssetID, route.Route.Address, route.Route.RequiredFinality, route.Route.StartsAt, route.Route.GraceEndsAt, route.RouteID, route.Route.ExpiresAt).Scan(&ambiguous)
	return events, ambiguous, err
}

func failClosedAmbiguousDecision(route automatedMatchingRoute, events []domain.TransferEvent) application.AutomatedMatchDecision {
	decision := application.AutomatedMatchDecision{Outcome: application.AutomatedReview, Class: application.ExceptionAmbiguous, PolicyID: route.Policy.ID, PolicyVersion: route.Policy.Version, Expected: route.Route.ExpectedAmount, Received: money.Zero(), Credited: money.Zero(), TreasuryReceived: money.Zero(), GasFreeFees: money.Zero(), ReasonCodes: []string{"multiple_policy_bound_routes_overlap"}}
	for _, event := range events {
		if event.Kind != "gasfree_fee" && event.Identity.ToAddress == route.Route.Address {
			decision.Received, _ = decision.Received.Add(event.Amount)
		}
	}
	body, _ := json.Marshal(struct {
		Route, Policy, Received string
		EventIDs                []string
	}{route.RouteID, fmt.Sprintf("%s:%d", route.Policy.ID, route.Policy.Version), decision.Received.String(), eventIDs(events)})
	hash := sha256.Sum256(body)
	decision.EvidenceHash = hex.EncodeToString(hash[:])
	return decision
}

func upsertAutomatedAggregate(ctx context.Context, tx pgx.Tx, route automatedMatchingRoute, decision application.AutomatedMatchDecision, now time.Time) (string, error) {
	aggregateID, err := ids.New()
	if err != nil {
		return "", err
	}
	evidence, err := json.Marshal(decision)
	if err != nil {
		return "", err
	}
	evidenceHash, err := hex.DecodeString(decision.EvidenceHash)
	if err != nil || len(evidenceHash) != sha256.Size {
		return "", domain.ErrInvariantViolation
	}
	state := "review"
	if decision.Outcome == application.AutomatedCollect {
		state = "collecting"
	} else if decision.Outcome == application.AutomatedSettle {
		state = "settled"
	}
	var effectiveID string
	err = tx.QueryRow(ctx, `INSERT INTO payment_match_aggregates
(id,tenant_id,merchant_id,route_id,intent_id,policy_id,policy_version,state,classification,expected_atomic,received_atomic,credited_atomic,treasury_received_atomic,gasfree_fees_atomic,evidence,evidence_hash,created_at,updated_at,settled_at,version)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::numeric,$11::numeric,$12::numeric,$13::numeric,$14::numeric,$15::jsonb,$16,$17::timestamptz,$17::timestamptz,CASE WHEN $8='settled' THEN $17::timestamptz ELSE NULL END,1)
ON CONFLICT (tenant_id,route_id) WHERE state<>'reversed' DO UPDATE SET state=EXCLUDED.state,classification=EXCLUDED.classification,
 received_atomic=EXCLUDED.received_atomic,credited_atomic=EXCLUDED.credited_atomic,treasury_received_atomic=EXCLUDED.treasury_received_atomic,
 gasfree_fees_atomic=EXCLUDED.gasfree_fees_atomic,evidence=EXCLUDED.evidence,evidence_hash=EXCLUDED.evidence_hash,
 updated_at=EXCLUDED.updated_at,settled_at=COALESCE(payment_match_aggregates.settled_at,EXCLUDED.settled_at),version=payment_match_aggregates.version+1
WHERE payment_match_aggregates.state<>'settled'
RETURNING id::text`, aggregateID, route.TenantID, route.MerchantID, route.RouteID, route.IntentID, route.Policy.ID, route.Policy.Version, state, string(decision.Class), decision.Expected.String(), decision.Received.String(), decision.Credited.String(), decision.TreasuryReceived.String(), decision.GasFreeFees.String(), evidence, evidenceHash, now).Scan(&effectiveID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", domain.ErrStateConflict
	}
	return effectiveID, err
}

func appendAutomatedDecision(ctx context.Context, tx pgx.Tx, route automatedMatchingRoute, aggregateID string, decision application.AutomatedMatchDecision, now time.Time) error {
	id, err := ids.New()
	if err != nil {
		return err
	}
	evidence, _ := json.Marshal(decision)
	hash, err := hex.DecodeString(decision.EvidenceHash)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO automated_matching_decisions
(id,tenant_id,merchant_id,aggregate_id,route_id,policy_id,policy_version,outcome,classification,canonical_evidence,evidence_hash,decided_at)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::jsonb,$11,$12) ON CONFLICT (tenant_id,aggregate_id,evidence_hash) DO NOTHING`, id, route.TenantID, route.MerchantID, aggregateID, route.RouteID, route.Policy.ID, route.Policy.Version, decision.Outcome, decision.Class, evidence, hash, now)
	return err
}

func persistAutomatedAllocations(ctx context.Context, tx pgx.Tx, route automatedMatchingRoute, aggregateID string, decision application.AutomatedMatchDecision, state string, now time.Time) error {
	for _, allocation := range decision.Allocations {
		matchID, err := ids.New()
		if err != nil {
			return err
		}
		evidence, _ := json.Marshal(map[string]any{"decision_evidence_sha256": decision.EvidenceHash, "policy_id": decision.PolicyID, "policy_version": decision.PolicyVersion, "allocation": allocation, "deterministic": true})
		command, err := tx.Exec(ctx, `INSERT INTO payment_matches
(id,tenant_id,event_id,route_id,intent_id,match_kind,expected_atomic,received_atomic,credited_atomic,state,evidence,policy_version,created_at,finalized_at,aggregate_id,allocation_role)
VALUES($1,$2,$3,$4,$5,$6,$7::numeric,$8::numeric,$9::numeric,$10,$11::jsonb,$12,$13::timestamptz,CASE WHEN $10='finalized' THEN $13::timestamptz ELSE NULL::timestamptz END,$14,$15)
ON CONFLICT (event_id) WHERE state<>'reversed' DO UPDATE SET
 credited_atomic=EXCLUDED.credited_atomic,state=EXCLUDED.state,evidence=EXCLUDED.evidence,
 finalized_at=CASE WHEN EXCLUDED.state='finalized' THEN EXCLUDED.finalized_at ELSE payment_matches.finalized_at END
WHERE payment_matches.route_id=EXCLUDED.route_id AND payment_matches.intent_id=EXCLUDED.intent_id
  AND payment_matches.aggregate_id=EXCLUDED.aggregate_id AND payment_matches.received_atomic=EXCLUDED.received_atomic
  AND payment_matches.policy_version=EXCLUDED.policy_version`, matchID, route.TenantID, allocation.EventID, route.RouteID, route.IntentID, allocation.MatchKind, route.Route.ExpectedAmount.String(), allocation.Received.String(), allocation.Credited.String(), state, evidence, decision.PolicyVersion, now, aggregateID, allocation.Role)
		if err != nil {
			return classify(err)
		}
		if command.RowsAffected() != 1 {
			return fmt.Errorf("%w: transfer already allocated by another aggregate", domain.ErrStateConflict)
		}
	}
	return nil
}

func postAutomatedSettlementLedger(ctx context.Context, tx pgx.Tx, route automatedMatchingRoute, aggregateID string, decision application.AutomatedMatchDecision, now time.Time) error {
	accounts, err := ensureAutomatedMatchingAccounts(ctx, tx, route, route.Route.AssetID)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO ledger_transactions
(id,tenant_id,business_type,business_reference,effective_at,booked_at,correlation_id,policy_version)
VALUES($1::uuid,$2,'payment_settlement',$1::uuid::text,$3,$3,$1::uuid,$4) ON CONFLICT (tenant_id,business_type,business_reference) DO NOTHING`, aggregateID, route.TenantID, now, decision.PolicyVersion)
	if err != nil {
		return err
	}
	var sequence int
	insertEntry := func(account, direction string, amount money.Amount) error {
		if amount.IsZero() {
			return nil
		}
		sequence++
		_, e := tx.Exec(ctx, `INSERT INTO ledger_entries(transaction_id,tenant_id,sequence,account_id,asset_id,direction,amount_atomic,created_at)
VALUES($1,$2,$3,$4,$5,$6,$7::numeric,$8) ON CONFLICT (transaction_id,sequence) DO NOTHING`, aggregateID, route.TenantID, sequence, account, route.Route.AssetID, direction, amount.String(), now)
		return e
	}
	if err := insertEntry(accounts["treasury_asset"], "debit", decision.TreasuryReceived); err != nil {
		return err
	}
	if err := insertEntry(accounts["merchant_settlement_liability"], "credit", decision.Credited); err != nil {
		return err
	}
	excess, err := decision.Received.Sub(decision.Credited)
	if err != nil {
		return err
	}
	return insertEntry(accounts["unmatched_overpayment_liability"], "credit", excess)
}

func ensureAutomatedMatchingAccounts(ctx context.Context, tx pgx.Tx, route automatedMatchingRoute, assetID string) (map[string]string, error) {
	types := map[string]string{"treasury_asset": "asset", "merchant_settlement_liability": "liability", "unmatched_overpayment_liability": "liability"}
	result := map[string]string{}
	for code, accountType := range types {
		id, err := ids.New()
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO ledger_accounts(id,tenant_id,merchant_id,asset_id,account_code,account_type,created_at)
VALUES($1,$2,$3,$4,$5,$6,clock_timestamp()) ON CONFLICT (tenant_id,merchant_id,asset_id,account_code) DO NOTHING`, id, route.TenantID, route.MerchantID, assetID, code, accountType); err != nil {
			return nil, err
		}
		var accountID string
		if err := tx.QueryRow(ctx, `SELECT id::text FROM ledger_accounts WHERE tenant_id=$1 AND merchant_id=$2 AND asset_id=$3 AND account_code=$4`, route.TenantID, route.MerchantID, assetID, code).Scan(&accountID); err != nil {
			return nil, err
		}
		result[code] = accountID
	}
	return result, nil
}

func finalizeAutomatedIntent(ctx context.Context, tx pgx.Tx, route automatedMatchingRoute, aggregateID string, decision application.AutomatedMatchDecision, now time.Time) error {
	status, reason, eventType := "settled", "deterministic_aggregate_settlement", "payment.settled"
	if decision.Received.Cmp(decision.Expected) > 0 {
		status, reason, eventType = "overpaid", "deterministic_overpayment_policy", "payment.overpaid"
	} else if decision.Received.Cmp(decision.Expected) < 0 {
		reason = "deterministic_underpayment_tolerance"
	} else if decision.Class == application.ExceptionLate {
		reason = "deterministic_late_grace_policy"
	}
	var version int64
	err := tx.QueryRow(ctx, `UPDATE payment_intents SET status=$1,status_reason=$2,settled_at=$3,updated_at=$3,version=version+1
WHERE id=$4 AND tenant_id=$5 AND status IN ('pending','observed','partially_paid','confirmed','expired','needs_review','reorg_review') RETURNING version`, status, reason, now, route.IntentID, route.TenantID).Scan(&version)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE payment_routes SET status='settled',updated_at=$1,version=version+1 WHERE id=$2 AND tenant_id=$3 AND status IN ('active','expired')`, now, route.RouteID, route.TenantID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE amount_reservations SET state='consumed',release_reason='settled',updated_at=$1,version=version+1 WHERE route_id=$2 AND tenant_id=$3 AND state='active'`, now, route.RouteID, route.TenantID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE unmatched_payments SET status='resolved',selected_route_id=$1,updated_at=$2,version=version+1 WHERE event_id=ANY($3::uuid[]) AND tenant_id=$4 AND status<>'reorged'`, route.RouteID, now, allocationEventIDs(decision.Allocations), route.TenantID); err != nil {
		return err
	}
	return insertAutomatedSettlementNotification(ctx, tx, route, aggregateID, decision, status, eventType, version, now)
}

func insertAutomatedSettlementNotification(ctx context.Context, tx pgx.Tx, route automatedMatchingRoute, aggregateID string, decision application.AutomatedMatchDecision, status, eventType string, version int64, now time.Time) error {
	webhookID, err := ids.New()
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, route.MerchantID); err != nil {
		return err
	}
	sequence, err := nextMerchantEventSequence(ctx, tx, route.TenantID, route.MerchantID)
	if err != nil {
		return err
	}
	contributions := make([]webhook.SettlementContribution, 0, len(decision.Allocations))
	last := application.MatchAllocation{}
	for _, allocation := range decision.Allocations {
		contributions = append(contributions, webhook.SettlementContribution{TransactionHash: allocation.TransactionID, EventIndex: allocation.EventIndex, Role: allocation.Role, ReceivedRaw: allocation.Received, CreditedRaw: allocation.Credited, BlockHeight: strconv.FormatUint(allocation.BlockHeight, 10), BlockHash: allocation.BlockHash, BlockTime: allocation.OnChainTime, EvidenceHash: allocation.EvidenceHash})
		if allocation.Role == "payment" {
			last = allocation
		}
	}
	var gasFreeFees *money.Amount
	if !decision.GasFreeFees.IsZero() {
		fees := decision.GasFreeFees
		gasFreeFees = &fees
	}
	body, err := webhook.CanonicalBody(webhook.Event{EventID: webhookID, EventType: eventType, SchemaVersion: "1", Sequence: sequence, OccurredAt: now, MerchantID: route.MerchantID, Livemode: true,
		PaymentIntent: webhook.PaymentIntentSnapshot{ID: route.IntentID, MerchantOrderID: route.MerchantOrderID, Status: status, AmountMinor: route.AmountMinor, Currency: route.Currency},
		Settlement:    &webhook.Settlement{SettlementID: aggregateID, AssetID: route.Route.AssetID, Network: route.Route.ChainID, ExpectedRaw: decision.Expected, ReceivedRaw: decision.Received, CreditedRaw: decision.Credited, TransactionHash: last.TransactionID, EventIndex: "aggregate:" + strconv.Itoa(len(decision.Allocations)), BlockHeight: strconv.FormatUint(last.BlockHeight, 10), BlockTime: last.OnChainTime, Finality: "finalized", ManualResolution: false, PolicyVersion: decision.PolicyVersion, EvidenceHash: decision.EvidenceHash, GasFreeFeesRaw: gasFreeFees, Contributions: contributions}})
	if err != nil {
		return err
	}
	bodyHash := sha256.Sum256(body)
	var signingKey string
	if err = tx.QueryRow(ctx, `SELECT COALESCE(min(signing_key_id),'unconfigured') FROM webhook_endpoints WHERE merchant_id=$1 AND status='active'`, route.MerchantID).Scan(&signingKey); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO callback_events(id,tenant_id,merchant_id,intent_id,event_type,schema_version,canonical_payload,canonical_body,body_hash,signing_key_id,merchant_sequence,aggregate_sequence,occurred_at,created_at)
VALUES($1,$2,$3,$4,$5,'1',$6::jsonb,$7,$8,$9,$10,$11,$12,$12)`, webhookID, route.TenantID, route.MerchantID, route.IntentID, eventType, string(body), body, bodyHash[:], signingKey, sequence, version, now)
	if err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT id::text,signing_key_id FROM webhook_endpoints WHERE merchant_id=$1 AND status='active' AND ($2=ANY(event_types) OR '*'=ANY(event_types)) FOR UPDATE`, route.MerchantID, eventType)
	if err != nil {
		return err
	}
	type endpointKey struct{ id, keyID string }
	var endpoints []endpointKey
	for rows.Next() {
		var endpoint endpointKey
		if err := rows.Scan(&endpoint.id, &endpoint.keyID); err != nil {
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
		delivery, err := ids.New()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO callback_deliveries(id,tenant_id,callback_event_id,endpoint_id,signing_key_id,status,attempt_count,next_attempt_at,created_at,updated_at,version) VALUES($1,$2,$3,$4,$5,'pending',0,$6,$6,$6,1)`, delivery, route.TenantID, webhookID, endpoint.id, endpoint.keyID, now); err != nil {
			return err
		}
	}
	outboxID, err := ids.New()
	if err != nil {
		return err
	}
	return insertOutbox(ctx, tx, outboxID, application.Principal{TenantID: route.TenantID, MerchantID: route.MerchantID}, route.IntentID, version, eventType, aggregateID, body, now)
}

func finishAutomatedMatchingJob(ctx context.Context, tx pgx.Tx, workerID string, job application.AutomatedMatchingJob, status string, next time.Time, reason string) error {
	command, err := tx.Exec(ctx, `UPDATE automated_matching_jobs SET
 status=CASE WHEN reschedule_requested AND $1='completed' THEN 'pending' ELSE $1 END,
 next_attempt_at=CASE WHEN reschedule_requested THEN LEAST(next_attempt_at,$2) ELSE $2 END,
 locked_by=NULL,locked_until=NULL,lease_token=NULL,reschedule_requested=false,last_error=NULLIF($3,''),updated_at=clock_timestamp()
WHERE route_id=$4 AND status='leased' AND locked_by=$5 AND lease_token=$6 AND locked_until>clock_timestamp()`, status, next, reason, job.RouteID, workerID, job.LeaseToken)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return domain.ErrVersionConflict
	}
	return nil
}

func allocationEventIDs(allocations []application.MatchAllocation) []string {
	result := make([]string, 0, len(allocations))
	for _, allocation := range allocations {
		result = append(result, allocation.EventID)
	}
	return result
}

func eventIDs(events []domain.TransferEvent) []string {
	result := make([]string, 0, len(events))
	for _, event := range events {
		result = append(result, event.ID)
	}
	return result
}

var _ application.AutomatedMatchingStore = (*Store)(nil)
