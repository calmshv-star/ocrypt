package postgres

import (
	"context"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/application"
	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/money"
	"github.com/jackc/pgx/v5"
)

// Load identity from the merchant-authenticated intents, never candidate JSON
// or the blockchain sender (which can be a shared exchange hot wallet).
func automaticSettlementCandidate(ctx context.Context, tx pgx.Tx, candidates []application.Candidate) (application.Candidate, bool, error) {
	if candidate, ok := application.UniqueAutomaticCandidate(candidates); ok {
		return candidate, true, nil
	}
	if len(candidates) < 2 || candidates[0].Class != application.ExceptionOverpaid || candidates[0].Score <= application.AutomaticCandidateScoreThreshold || candidates[0].Score != candidates[1].Score {
		return application.Candidate{}, false, nil
	}
	var intentIDs []string
	for _, candidate := range candidates {
		if candidate.Score != candidates[0].Score {
			break
		}
		intentIDs = append(intentIDs, candidate.IntentID)
	}
	rows, err := tx.Query(ctx, `SELECT id::text,tenant_id::text,merchant_id::text,COALESCE(customer_reference,''),amount_minor::text,currency,currency_scale,COALESCE(description,''),metadata FROM payment_intents WHERE id=ANY($1::uuid[]) FOR SHARE`, intentIDs)
	if err != nil {
		return application.Candidate{}, false, err
	}
	defer rows.Close()
	intents := map[string]domain.PaymentIntent{}
	for rows.Next() {
		var intent domain.PaymentIntent
		var amount string
		if err := rows.Scan(&intent.ID, &intent.TenantID, &intent.MerchantID, &intent.CustomerReference, &amount, &intent.Currency, &intent.CurrencyScale, &intent.Description, &intent.Metadata); err != nil {
			return application.Candidate{}, false, err
		}
		intent.AmountMinor, err = money.Parse(amount)
		if err != nil {
			return application.Candidate{}, false, err
		}
		intents[intent.ID] = intent
	}
	if err := rows.Err(); err != nil {
		return application.Candidate{}, false, err
	}
	candidate, ok := application.UniqueCustomerOverpaymentCandidate(candidates, intents)
	return candidate, ok, nil
}

// The matching worker independently rechecks current candidates before a
// financial write. Merely fixing the initial ranking would still leave these
// payments rejected by the overlapping-route guard during settlement.
func sameCustomerOverlapResolved(ctx context.Context, tx pgx.Tx, route automatedMatchingRoute, events []domain.TransferEvent, now time.Time) (bool, error) {
	seen := false
	for _, event := range events {
		if event.Kind == "gasfree_fee" {
			continue
		}
		candidates, tenant, _, err := findPotentialCandidates(ctx, tx, event, now)
		if err != nil {
			return false, err
		}
		if tenant != route.TenantID {
			return false, nil
		}
		candidate, ok, err := automaticSettlementCandidate(ctx, tx, candidates)
		if err != nil {
			return false, err
		}
		if !ok || candidate.RouteID != route.RouteID {
			return false, nil
		}
		qualified := false
		for _, reason := range candidate.Reasons {
			if reason == application.SameCustomerOverpaymentReason {
				qualified = true
			}
		}
		if !qualified {
			return false, nil
		}
		seen = true
	}
	return seen, nil
}
