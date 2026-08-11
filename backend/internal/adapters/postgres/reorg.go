package postgres

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/application"
	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/ids"
	"github.com/calmshv-star/ocrypt/backend/internal/money"
	"github.com/calmshv-star/ocrypt/backend/internal/scanner"
	"github.com/calmshv-star/ocrypt/backend/internal/webhook"
	"github.com/jackc/pgx/v5"
)

type reorgSettlement struct {
	MatchID, TenantID, MerchantID, IntentID, RouteID, EventID string
	AggregateID                                               string
	OriginalLedgerID, SettlementID                            string
	MerchantOrderID, Currency                                 string
	ChainID, TransactionID, EventIndex, AssetID               string
	BlockHeight                                               uint64
	BlockTime                                                 time.Time
	Livemode                                                  bool
	IntentVersion, LedgerPolicyVersion                        int64
	AmountMinor, Expected, Received                           money.Amount
}

// RewindReorg is the durable reorg transaction boundary. It fences the cursor
// lease, invalidates replaced canonical blocks/transfers, reverses every posted
// ledger transaction with immutable opposite entries, moves affected intents
// to reorg_review, emits payment.reorged, and rewinds to the newest common
// ancestor visible in the overlap. No previously posted ledger row is deleted.
func (s *ScannerStore) RewindReorg(ctx context.Context, lease scanner.Lease, batch scanner.RangeBatch, incident scanner.ReorgError) error {
	if lease.ChainID == "" || lease.Shard == "" || lease.Owner == "" || lease.Hash == "" || incident.Height != lease.Height || len(batch.Blocks) == 0 || batch.From > lease.Height {
		return errors.New("invalid scanner reorg rewind")
	}
	return pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx pgx.Tx) error {
		// Lock the fenced cursor first. Only its current lease owner/version may
		// compensate money or move the durable rewind point.
		var locked bool
		err := tx.QueryRow(ctx, `SELECT true FROM scanner_cursors WHERE chain_id=$1 AND scanner_shard=$2 AND capability=$3 AND locked_by=$4 AND version=$5 AND locked_until>clock_timestamp() FOR UPDATE`, lease.ChainID, lease.Shard, s.capability, lease.Owner, lease.Version).Scan(&locked)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: scanner lease was lost", domain.ErrVersionConflict)
		}
		if err != nil {
			return err
		}

		// Highest overlapping block that still agrees with durable canonical
		// history is the common ancestor. If none agrees, rewind to immediately
		// before the covered overlap; the next run walks farther back, so a deep
		// reorg cannot permanently stall on the same cursor hash.
		rewindHeight := uint64(0)
		rewindHash := ""
		foundAncestor := false
		for _, block := range batch.Blocks {
			if block.Height > lease.Height {
				break
			}
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM chain_blocks WHERE chain_id=$1 AND height=$2::numeric AND block_hash=$3 AND canonical_status<>'reorged')`, lease.ChainID, strconv.FormatUint(block.Height, 10), block.Hash).Scan(&exists); err != nil {
				return err
			}
			if exists && (!foundAncestor || block.Height > rewindHeight) {
				foundAncestor, rewindHeight, rewindHash = true, block.Height, block.Hash
			}
		}
		if !foundAncestor && batch.From > 0 {
			rewindHeight = batch.From - 1
			err := tx.QueryRow(ctx, `SELECT block_hash FROM chain_blocks WHERE chain_id=$1 AND height=$2::numeric AND canonical_status<>'reorged' ORDER BY last_observed_at DESC LIMIT 1`, lease.ChainID, strconv.FormatUint(rewindHeight, 10)).Scan(&rewindHash)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			if errors.Is(err, pgx.ErrNoRows) {
				// Missing history is recorded as a gap and the empty hash makes the
				// next scan start forward without claiming a false ancestor.
				rewindHash = ""
			}
		}

		rows, err := tx.Query(ctx, `UPDATE chain_blocks SET canonical_status='reorged',last_observed_at=clock_timestamp() WHERE chain_id=$1 AND height>$2::numeric AND height<=$3::numeric AND canonical_status<>'reorged' RETURNING block_hash`, lease.ChainID, strconv.FormatUint(rewindHeight, 10), strconv.FormatUint(lease.Height, 10))
		if err != nil {
			return err
		}
		var replacedHashes []string
		for rows.Next() {
			var hash string
			if err := rows.Scan(&hash); err != nil {
				rows.Close()
				return err
			}
			replacedHashes = append(replacedHashes, hash)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()

		var eventIDs []string
		if len(replacedHashes) > 0 {
			eventRows, err := tx.Query(ctx, `SELECT id::text FROM transfer_events WHERE chain_id=$1 AND block_hash=ANY($2::text[]) AND status<>'reorged' FOR UPDATE`, lease.ChainID, replacedHashes)
			if err != nil {
				return err
			}
			for eventRows.Next() {
				var eventID string
				if err := eventRows.Scan(&eventID); err != nil {
					eventRows.Close()
					return err
				}
				eventIDs = append(eventIDs, eventID)
			}
			if err := eventRows.Err(); err != nil {
				eventRows.Close()
				return err
			}
			eventRows.Close()
		}

		for _, eventID := range eventIDs {
			settlements, err := loadReorgSettlements(ctx, tx, eventID)
			if err != nil {
				return err
			}
			// A pre-settlement observation has no payment_match or ledger row, but
			// it is still a merchant-visible state that must not remain stuck after
			// its block is orphaned. Settled observations are marked here and their
			// single callback is emitted by the compensating-ledger path below.
			if err := reorgPaymentObservation(ctx, tx, eventID, len(settlements) == 0, time.Now().UTC()); err != nil {
				return err
			}
			for _, settlement := range settlements {
				if err := compensateSettlement(ctx, tx, settlement); err != nil {
					return err
				}
			}
			_, err = tx.Exec(ctx, `UPDATE payment_matches SET state='reversed',reversed_at=clock_timestamp() WHERE event_id=$1 AND state<>'reversed'`, eventID)
			if err != nil {
				return err
			}
			_, err = tx.Exec(ctx, `UPDATE unmatched_payments SET status='reorged',updated_at=clock_timestamp(),version=version+1 WHERE event_id=$1 AND status<>'reorged'`, eventID)
			if err != nil {
				return err
			}
		}
		if len(eventIDs) > 0 {
			if _, err := tx.Exec(ctx, `UPDATE transfer_events SET status='reorged',updated_at=clock_timestamp(),version=version+1 WHERE id=ANY($1::uuid[]) AND status<>'reorged'`, eventIDs); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE scanner_transfer_queue SET status='reorged',locked_by=NULL,locked_until=NULL,last_error='canonical_block_replaced',updated_at=clock_timestamp() WHERE event_id=ANY($1::uuid[]) AND status<>'reorged'`, eventIDs); err != nil {
				return err
			}
		}

		gapID, err := ids.New()
		if err != nil {
			return err
		}
		gapFrom := rewindHeight + 1
		_, err = tx.Exec(ctx, `INSERT INTO scanner_gaps (id,chain_id,from_height,to_height,reason,status,occurrence_count,first_seen_at,last_seen_at) VALUES ($1,$2,$3::numeric,$4::numeric,'canonical_reorg','open',1,clock_timestamp(),clock_timestamp()) ON CONFLICT (chain_id,from_height,to_height) WHERE status='open' DO UPDATE SET reason='canonical_reorg',occurrence_count=scanner_gaps.occurrence_count+1,last_seen_at=clock_timestamp()`, gapID, lease.ChainID, strconv.FormatUint(gapFrom, 10), strconv.FormatUint(lease.Height, 10))
		if err != nil {
			return err
		}
		command, err := tx.Exec(ctx, `UPDATE scanner_cursors SET cursor_height=$1::numeric,cursor_hash=$2,locked_by=NULL,locked_until=NULL,heartbeat_at=clock_timestamp(),version=version+1,updated_at=clock_timestamp() WHERE chain_id=$3 AND scanner_shard=$4 AND capability=$5 AND locked_by=$6 AND version=$7 AND locked_until>clock_timestamp()`, strconv.FormatUint(rewindHeight, 10), rewindHash, lease.ChainID, lease.Shard, s.capability, lease.Owner, lease.Version)
		if err != nil {
			return err
		}
		if command.RowsAffected() != 1 {
			return fmt.Errorf("%w: scanner lease was lost", domain.ErrVersionConflict)
		}
		return nil
	})
}

func loadReorgSettlements(ctx context.Context, tx pgx.Tx, eventID string) ([]reorgSettlement, error) {
	rows, err := tx.Query(ctx, `SELECT pm.id::text,pm.tenant_id::text,i.merchant_id::text,pm.intent_id::text,pm.route_id::text,te.id::text,
COALESCE(pm.aggregate_id::text,''),lt.id::text,lt.business_reference,lt.policy_version,i.merchant_order_id,i.currency,i.version,i.amount_minor::text,
COALESCE(a.expected_atomic,pm.expected_atomic)::text,COALESCE(a.received_atomic,pm.received_atomic)::text,
te.chain_id,te.transaction_id,te.event_identity,te.asset_id,te.block_height::text,te.on_chain_time,(m.environment='live')
FROM payment_matches pm
JOIN transfer_events te ON te.id=pm.event_id
JOIN payment_intents i ON i.id=pm.intent_id AND i.tenant_id=pm.tenant_id
JOIN merchants m ON m.id=i.merchant_id AND m.tenant_id=i.tenant_id
LEFT JOIN payment_match_aggregates a ON a.id=pm.aggregate_id AND a.tenant_id=pm.tenant_id AND a.state='settled'
JOIN ledger_transactions lt ON lt.tenant_id=pm.tenant_id AND lt.business_type='payment_settlement'
 AND lt.correlation_id=CASE WHEN pm.aggregate_id IS NULL THEN te.id::text ELSE pm.aggregate_id::text END
WHERE pm.event_id=$1 AND pm.state<>'reversed'
FOR UPDATE OF pm,i,lt`, eventID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []reorgSettlement
	for rows.Next() {
		var settlement reorgSettlement
		var amountMinor, expected, received, height string
		if err := rows.Scan(&settlement.MatchID, &settlement.TenantID, &settlement.MerchantID, &settlement.IntentID, &settlement.RouteID, &settlement.EventID, &settlement.AggregateID, &settlement.OriginalLedgerID, &settlement.SettlementID, &settlement.LedgerPolicyVersion, &settlement.MerchantOrderID, &settlement.Currency, &settlement.IntentVersion, &amountMinor, &expected, &received, &settlement.ChainID, &settlement.TransactionID, &settlement.EventIndex, &settlement.AssetID, &height, &settlement.BlockTime, &settlement.Livemode); err != nil {
			return nil, err
		}
		var err error
		if settlement.AmountMinor, err = money.Parse(amountMinor); err != nil {
			return nil, err
		}
		if settlement.Expected, err = money.Parse(expected); err != nil {
			return nil, err
		}
		if settlement.Received, err = money.Parse(received); err != nil {
			return nil, err
		}
		if settlement.BlockHeight, err = strconv.ParseUint(height, 10, 64); err != nil {
			return nil, err
		}
		result = append(result, settlement)
	}
	return result, rows.Err()
}

func compensateSettlement(ctx context.Context, tx pgx.Tx, settlement reorgSettlement) error {
	now := time.Now().UTC()
	reversalID, err := ids.New()
	if err != nil {
		return err
	}
	correlationID := settlement.EventID
	if settlement.AggregateID != "" {
		correlationID = settlement.AggregateID
	}
	_, err = tx.Exec(ctx, `INSERT INTO ledger_transactions (id,tenant_id,business_type,business_reference,reversal_of,effective_at,booked_at,correlation_id,policy_version) VALUES ($1,$2,'payment_settlement.reversal',$3,$4,$5,$5,$6,$7)`, reversalID, settlement.TenantID, "reorg:"+settlement.OriginalLedgerID, settlement.OriginalLedgerID, now, correlationID, settlement.LedgerPolicyVersion)
	if err != nil {
		return classify(err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO ledger_entries (transaction_id,tenant_id,sequence,account_id,asset_id,direction,amount_atomic,created_at)
SELECT $1,tenant_id,sequence,account_id,asset_id,CASE direction WHEN 'debit' THEN 'credit'::ledger_direction ELSE 'debit'::ledger_direction END,amount_atomic,$2
FROM ledger_entries WHERE transaction_id=$3 ORDER BY sequence`, reversalID, now, settlement.OriginalLedgerID)
	if err != nil {
		return err
	}
	command, err := tx.Exec(ctx, `UPDATE payment_intents SET status='reorg_review',status_reason='canonical_transfer_reorged',updated_at=$1,version=version+1 WHERE id=$2 AND tenant_id=$3 AND version=$4 AND status IN ('settled','overpaid','confirmed')`, now, settlement.IntentID, settlement.TenantID, settlement.IntentVersion)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return domain.ErrVersionConflict
	}
	newVersion := settlement.IntentVersion + 1
	_, err = tx.Exec(ctx, `UPDATE payment_routes SET status=CASE WHEN grace_ends_at>$1 THEN 'active'::route_status ELSE 'expired'::route_status END,updated_at=$1,version=version+1 WHERE id=$2 AND tenant_id=$3 AND status='settled'`, now, settlement.RouteID, settlement.TenantID)
	if err != nil {
		return err
	}
	// Restore the exact-amount exclusion only when it is still within its
	// original window and cannot conflict. If capacity was reused after the
	// settlement, matching sees both routes and fails safely as ambiguous.
	_, err = tx.Exec(ctx, `UPDATE amount_reservations ar SET state='active',release_reason=NULL,updated_at=$1,version=version+1
WHERE ar.route_id=$2 AND ar.tenant_id=$3 AND ar.state='consumed' AND upper(ar.active_window)>$1
AND NOT EXISTS (SELECT 1 FROM amount_reservations other WHERE other.id<>ar.id AND other.state='active' AND other.chain_id=ar.chain_id AND other.receiving_address=ar.receiving_address AND other.asset_id=ar.asset_id AND other.exact_amount_atomic=ar.exact_amount_atomic AND other.active_window && ar.active_window)`, now, settlement.RouteID, settlement.TenantID)
	if err != nil {
		return err
	}
	if settlement.AggregateID == "" {
		if _, err = tx.Exec(ctx, `UPDATE payment_matches SET state='reversed',reversed_at=$1 WHERE id=$2 AND state<>'reversed'`, now, settlement.MatchID); err != nil {
			return err
		}
	} else {
		if _, err = tx.Exec(ctx, `UPDATE payment_match_aggregates SET state='reversed',reversed_at=$1,updated_at=$1,version=version+1 WHERE id=$2 AND tenant_id=$3 AND state='settled'`, now, settlement.AggregateID, settlement.TenantID); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE payment_matches SET state='reversed',reversed_at=$1 WHERE aggregate_id=$2 AND tenant_id=$3 AND state<>'reversed'`, now, settlement.AggregateID, settlement.TenantID); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO automated_matching_jobs(route_id,tenant_id,merchant_id,status,next_attempt_at,attempt_count,reschedule_requested,created_at,updated_at)
VALUES($1,$2,$3,'pending',$4,0,false,$4,$4)
ON CONFLICT(route_id) DO UPDATE SET status=CASE WHEN automated_matching_jobs.status='leased' THEN automated_matching_jobs.status ELSE 'pending' END,
reschedule_requested=automated_matching_jobs.status='leased',next_attempt_at=LEAST(automated_matching_jobs.next_attempt_at,EXCLUDED.next_attempt_at),updated_at=EXCLUDED.updated_at`, settlement.RouteID, settlement.TenantID, settlement.MerchantID, now); err != nil {
			return err
		}
	}
	return insertReorgNotifications(ctx, tx, settlement, newVersion, reversalID, now)
}

func insertReorgNotifications(ctx context.Context, tx pgx.Tx, settlement reorgSettlement, aggregateVersion int64, reversalID string, now time.Time) error {
	webhookID, err := ids.New()
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, settlement.MerchantID); err != nil {
		return err
	}
	sequence, err := nextMerchantEventSequence(ctx, tx, settlement.TenantID, settlement.MerchantID)
	if err != nil {
		return err
	}
	body, err := webhook.CanonicalBody(webhook.Event{
		EventID: webhookID, EventType: "payment.reorged", SchemaVersion: "1", Sequence: sequence, OccurredAt: now, MerchantID: settlement.MerchantID, Livemode: settlement.Livemode,
		PaymentIntent: webhook.PaymentIntentSnapshot{ID: settlement.IntentID, MerchantOrderID: settlement.MerchantOrderID, Status: "reorg_review", AmountMinor: settlement.AmountMinor, Currency: settlement.Currency},
		Settlement:    &webhook.Settlement{SettlementID: settlement.SettlementID, AssetID: settlement.AssetID, Network: settlement.ChainID, ExpectedRaw: settlement.Expected, ReceivedRaw: settlement.Received, CreditedRaw: money.Zero(), TransactionHash: settlement.TransactionID, EventIndex: settlement.EventIndex, BlockHeight: strconv.FormatUint(settlement.BlockHeight, 10), BlockTime: settlement.BlockTime, Finality: "reorged", ManualResolution: false},
	})
	if err != nil {
		return err
	}
	bodyHash := sha256.Sum256(body)
	var signingKey string
	if err = tx.QueryRow(ctx, `SELECT COALESCE(min(signing_key_id),'unconfigured') FROM webhook_endpoints WHERE tenant_id=$1 AND merchant_id=$2 AND status='active'`, settlement.TenantID, settlement.MerchantID).Scan(&signingKey); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO callback_events (id,tenant_id,merchant_id,intent_id,event_type,schema_version,canonical_payload,canonical_body,body_hash,signing_key_id,merchant_sequence,aggregate_sequence,occurred_at,created_at) VALUES ($1,$2,$3,$4,'payment.reorged','1',$5::jsonb,$6,$7,$8,$9,$10,$11,$11)`, webhookID, settlement.TenantID, settlement.MerchantID, settlement.IntentID, string(body), body, bodyHash[:], signingKey, sequence, aggregateVersion, now)
	if err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT id::text,signing_key_id FROM webhook_endpoints WHERE tenant_id=$1 AND merchant_id=$2 AND status='active' AND ('payment.reorged'=ANY(event_types) OR '*'=ANY(event_types)) FOR UPDATE`, settlement.TenantID, settlement.MerchantID)
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
		if _, err = tx.Exec(ctx, `INSERT INTO callback_deliveries (id,tenant_id,callback_event_id,endpoint_id,signing_key_id,status,attempt_count,next_attempt_at,created_at,updated_at,version) VALUES ($1,$2,$3,$4,$5,'pending',0,$6,$6,$6,1)`, deliveryID, settlement.TenantID, webhookID, endpoint.id, endpoint.signingKeyID, now); err != nil {
			return err
		}
	}
	outboxID, err := ids.New()
	if err != nil {
		return err
	}
	principal := application.Principal{TenantID: settlement.TenantID, MerchantID: settlement.MerchantID}
	if err = insertOutbox(ctx, tx, outboxID, principal, settlement.IntentID, aggregateVersion, "payment.reorged", reversalID, body, now); err != nil {
		return err
	}
	observation, found, err := loadPaymentObservation(ctx, tx, settlement.EventID)
	if err != nil || !found {
		return err
	}
	return linkObservationEvent(ctx, tx, observation, "payment.reorged", webhookID, outboxID, now)
}
