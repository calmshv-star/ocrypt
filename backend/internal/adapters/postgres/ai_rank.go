package postgres

import (
	"context"
	"errors"

	"github.com/calmshv-star/ocrypt/backend/internal/application"
	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/ids"
	"github.com/jackc/pgx/v5"
)

// RecordAIRank persists a recommendation as immutable advisory evidence. It
// deliberately updates no unmatched-payment, route, intent, match, or ledger
// state; only a separately authenticated human workflow can do that.
func (s *Store) RecordAIRank(ctx context.Context, principal application.Principal, unmatchedID string, candidateSetVersion int64, result application.AIRankResult, model, endpointHost string) error {
	return s.db.WithinTenant(ctx, principal.TenantID, func(tx pgx.Tx) error {
		var exists bool
		err := tx.QueryRow(ctx, `SELECT true
FROM match_candidates c
JOIN payment_routes r ON r.id=c.route_id AND r.tenant_id=c.tenant_id
WHERE c.tenant_id=$1 AND c.unmatched_id=$2 AND c.route_id=$3 AND r.merchant_id=$4 AND c.candidate_set_version=$5
LIMIT 1`, principal.TenantID, unmatchedID, result.RecommendedRouteID, principal.MerchantID, candidateSetVersion).Scan(&exists)
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return err
		}
		id, err := ids.New()
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO ai_rank_suggestions
(id,tenant_id,unmatched_id,requested_by,model,endpoint_host,recommended_route_id,confidence,reason_codes,review_required,candidate_set_version,created_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,true,$10,clock_timestamp())`,
			id, principal.TenantID, unmatchedID, principal.ActorID, model, endpointHost, result.RecommendedRouteID, result.Confidence, result.ReasonCodes, candidateSetVersion)
		return classify(err)
	})
}
