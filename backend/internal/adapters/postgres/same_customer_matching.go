package postgres

import (
	"context"

	"github.com/calmshv-star/ocrypt/backend/internal/application"
	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/money"
	"github.com/jackc/pgx/v5"
)

// loadAutomaticCandidateContexts locks the authoritative records in the same
// serializable transaction as selection/settlement. A missing or mismatched
// route/intent is deliberately absent from the map so the selector fails closed.
func loadAutomaticCandidateContexts(ctx context.Context, tx pgx.Tx, candidates []application.Candidate, tenantID string) (map[string]application.AutomaticCandidateContext, error) {
	contexts := make(map[string]application.AutomaticCandidateContext)
	if len(candidates) < 2 || candidates[0].Score != 100 || candidates[1].Score != 100 {
		return contexts, nil
	}
	var routeIDs []string
	for _, candidate := range candidates {
		if candidate.Score == candidates[0].Score {
			routeIDs = append(routeIDs, candidate.RouteID)
		}
	}
	rows, err := tx.Query(ctx, `SELECT r.id::text,r.intent_id::text,r.chain_id,r.asset_id,
r.expected_amount_atomic::text,r.asset_decimals,r.receiving_address,COALESCE(r.memo,''),r.status::text,
r.starts_at,r.expires_at,r.grace_ends_at,i.id::text,i.tenant_id::text,i.merchant_id::text,
COALESCE(i.customer_reference,''),i.amount_minor::text,i.currency,i.currency_scale,i.metadata::text
FROM payment_routes r
JOIN payment_intents i ON i.id=r.intent_id AND i.tenant_id=r.tenant_id AND i.merchant_id=r.merchant_id
WHERE r.id=ANY($1::uuid[]) AND r.tenant_id=$2
ORDER BY r.id FOR UPDATE OF r,i`, routeIDs, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var result application.AutomaticCandidateContext
		var expected, amountMinor, status string
		if err := rows.Scan(&result.Route.ID, &result.Route.IntentID, &result.Route.ChainID, &result.Route.AssetID,
			&expected, &result.Route.AssetDecimals, &result.Route.Address, &result.Route.Memo, &status,
			&result.Route.StartsAt, &result.Route.ExpiresAt, &result.Route.GraceEndsAt,
			&result.Intent.ID, &result.Intent.TenantID, &result.Intent.MerchantID, &result.Intent.CustomerReference,
			&amountMinor, &result.Intent.Currency, &result.Intent.CurrencyScale, &result.Intent.Metadata); err != nil {
			return nil, err
		}
		result.Route.Status = domain.RouteStatus(status)
		if result.Route.ExpectedAmount, err = money.Parse(expected); err != nil {
			return nil, err
		}
		if result.Intent.AmountMinor, err = money.Parse(amountMinor); err != nil {
			return nil, err
		}
		contexts[result.Route.ID] = result
	}
	return contexts, rows.Err()
}
