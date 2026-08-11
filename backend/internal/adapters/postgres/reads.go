package postgres

import (
	"context"
	"encoding/json"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/application"
	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/jackc/pgx/v5"
)

func (s *Store) ListEvents(ctx context.Context, principal application.Principal, afterSequence int64, limit int) (items []domain.PublicEvent, err error) {
	err = s.db.WithinTenant(ctx, principal.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text,event_type,schema_version,COALESCE(intent_id,id)::text,CASE WHEN intent_id IS NULL THEN 'merchant' ELSE 'payment_intent' END,COALESCE(aggregate_sequence,merchant_sequence),merchant_sequence,convert_from(canonical_body,'UTF8'),occurred_at FROM callback_events WHERE tenant_id=$1 AND merchant_id=$2 AND merchant_sequence>$3 ORDER BY merchant_sequence ASC LIMIT $4`, principal.TenantID, principal.MerchantID, afterSequence, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item domain.PublicEvent
			var payload string
			if err := rows.Scan(&item.EventID, &item.EventType, &item.SchemaVersion, &item.AggregateID, &item.AggregateType, &item.AggregateVersion, &item.Sequence, &payload, &item.OccurredAt); err != nil {
				return err
			}
			item.Payload = json.RawMessage(payload)
			items = append(items, item)
		}
		return rows.Err()
	})
	return items, err
}

func (s *Store) ListTransfers(ctx context.Context, principal application.Principal, after string, limit int) (items []domain.MerchantTransfer, err error) {
	err = s.db.WithinTenant(ctx, principal.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT te.id::text,pm.intent_id::text,pm.route_id::text,te.chain_id,te.asset_id,te.transaction_id,te.event_identity,te.from_address,te.to_address,te.amount_atomic::text,te.block_height::text,te.block_hash,te.confirmations,te.status::text,pm.state,te.on_chain_time
FROM payment_matches pm JOIN transfer_events te ON te.id=pm.event_id JOIN payment_intents i ON i.id=pm.intent_id AND i.tenant_id=pm.tenant_id
WHERE pm.tenant_id=$1 AND i.merchant_id=$2 AND ($3='' OR te.id<$3::uuid) ORDER BY te.id DESC LIMIT $4`, principal.TenantID, principal.MerchantID, after, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item domain.MerchantTransfer
			var status string
			if err := rows.Scan(&item.TransferEventID, &item.PaymentIntentID, &item.PaymentRouteID, &item.ChainID, &item.AssetID, &item.TransactionID, &item.EventIndex, &item.FromAddress, &item.ToAddress, &item.AmountAtomic, &item.BlockHeight, &item.BlockHash, &item.Confirmations, &status, &item.MatchState, &item.OnChainTime); err != nil {
				return err
			}
			item.Status = domain.TransferStatus(status)
			items = append(items, item)
		}
		return rows.Err()
	})
	return items, err
}

func (s *Store) ListQuotes(ctx context.Context, principal application.Principal, after string, limit int) (items []domain.QuoteView, err error) {
	err = s.db.WithinTenant(ctx, principal.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT q.id::text,q.payment_intent_id::text,q.fiat_amount_minor::text,q.fiat_currency,q.fiat_scale,q.asset_id,q.crypto_amount_atomic::text,q.reference_price,q.spread_bps,q.policy_version,q.issued_at,q.expires_at
FROM rate_quotes q WHERE q.tenant_id=$1 AND q.merchant_id=$2 AND ($3='' OR q.id<$3::uuid)
AND EXISTS (SELECT 1 FROM payment_routes route WHERE route.tenant_id=q.tenant_id AND route.merchant_id=q.merchant_id AND route.quote_id=q.id)
ORDER BY q.id DESC LIMIT $4`, principal.TenantID, principal.MerchantID, after, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item domain.QuoteView
			if err := rows.Scan(&item.ID, &item.PaymentIntentID, &item.FiatAmountMinor, &item.FiatCurrency, &item.FiatScale, &item.AssetID, &item.CryptoAmountAtomic, &item.ReferencePrice, &item.SpreadBPS, &item.PolicyVersion, &item.IssuedAt, &item.ExpiresAt); err != nil {
				return err
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	return items, err
}

func (s *Store) ListBalances(ctx context.Context, principal application.Principal) (items []domain.BalanceView, err error) {
	err = s.db.WithinTenant(ctx, principal.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT a.account_code,a.asset_id,COALESCE(sum(e.amount_atomic) FILTER (WHERE e.direction='debit'),0)::text,COALESCE(sum(e.amount_atomic) FILTER (WHERE e.direction='credit'),0)::text FROM ledger_accounts a LEFT JOIN ledger_entries e ON e.account_id=a.id AND e.tenant_id=a.tenant_id AND e.asset_id=a.asset_id WHERE a.tenant_id=$1 AND a.merchant_id=$2 GROUP BY a.id,a.account_code,a.asset_id ORDER BY a.asset_id,a.account_code`, principal.TenantID, principal.MerchantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item domain.BalanceView
			if err := rows.Scan(&item.AccountCode, &item.AssetID, &item.DebitAtomic, &item.CreditAtomic); err != nil {
				return err
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	return items, err
}

func (s *Store) GetReconciliation(ctx context.Context, principal application.Principal) (summary domain.ReconciliationSummary, err error) {
	summary.IntentCounts = map[string]int64{}
	summary.GeneratedAt = time.Now().UTC()
	err = s.db.WithinTenant(ctx, principal.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT status::text,count(*) FROM payment_intents WHERE tenant_id=$1 AND merchant_id=$2 GROUP BY status`, principal.TenantID, principal.MerchantID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var status string
			var count int64
			if err := rows.Scan(&status, &count); err != nil {
				rows.Close()
				return err
			}
			summary.IntentCounts[status] = count
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM unmatched_payments u WHERE u.tenant_id=$1 AND u.status NOT IN ('resolved','ignored','invalid','reorged') AND EXISTS (SELECT 1 FROM match_candidates c JOIN payment_routes r ON r.id=c.route_id AND r.tenant_id=c.tenant_id WHERE c.unmatched_id=u.id AND c.tenant_id=u.tenant_id AND r.merchant_id=$2)`, principal.TenantID, principal.MerchantID).Scan(&summary.UnmatchedOpen); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE tenant_id=$1 AND merchant_id=$2 AND published_at IS NULL`, principal.TenantID, principal.MerchantID).Scan(&summary.PendingOutbox); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT count(*) FROM callback_deliveries d JOIN callback_events e ON e.id=d.callback_event_id AND e.tenant_id=d.tenant_id WHERE d.tenant_id=$1 AND e.merchant_id=$2 AND d.status='dead_letter'`, principal.TenantID, principal.MerchantID).Scan(&summary.DeadLetterCallbacks)
	})
	return summary, err
}
