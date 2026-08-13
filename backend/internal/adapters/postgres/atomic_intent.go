package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/application"
	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/jackc/pgx/v5"
)

// A live merchant integration is incomplete until terminal and exception
// payment states have a durable push channel. Command credentials create
// orders; they are not a substitute for lifecycle delivery.
const requiredWebhookEventTypesSQL = `ARRAY[
  'payment.partially_paid',
  'payment.needs_review',
  'payment.settled',
  'payment.overpaid',
  'payment.expired',
  'payment.cancelled',
  'payment.reorged'
]::text[]`

// CheckoutSessionInsert is the typed capability passed to CreateIntentInTx.
// It cannot execute arbitrary SQL and contains only checkout-safe fields.
type CheckoutSessionInsert struct {
	Token          string
	TokenHash      [32]byte
	Audience       string
	AllowedOrigin  string
	AllowedActions []string
	PaymentLinkID  string
	SessionNonce   string
}

type IntentInsertIDs struct {
	IntentID string
	EventID  string
	Now      time.Time
	Checkout CheckoutSessionInsert
}

// CreateIntentInTx is shared by the normal merchant flow and the atomic
// payment-link redemption composite. The caller owns transaction boundaries;
// this typed helper preserves the single intent/session/outbox implementation.
func (s *Store) CreateIntentInTx(ctx context.Context, tx pgx.Tx, cmd application.CreateIntent, ids IntentInsertIDs) (domain.PaymentIntent, error) {
	if tx == nil || ids.IntentID == "" || ids.EventID == "" || ids.Checkout.Token == "" || ids.Checkout.SessionNonce == "" || ids.Now.IsZero() {
		return domain.PaymentIntent{}, fmt.Errorf("%w: invalid atomic intent insert", domain.ErrValidation)
	}
	if err := setCorrelation(ctx, tx, cmd.CorrelationID); err != nil {
		return domain.PaymentIntent{}, err
	}
	var webhookReady bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(
SELECT 1
FROM webhook_endpoints w
JOIN management_webhook_verifications v
  ON v.endpoint_id=w.id AND v.tenant_id=w.tenant_id AND v.verified_at IS NOT NULL
WHERE w.tenant_id=$1 AND w.merchant_id=$2 AND w.status='active'
  AND ('*'=ANY(w.event_types) OR w.event_types @> `+requiredWebhookEventTypesSQL+`)
)`, cmd.Principal.TenantID, cmd.Principal.MerchantID).Scan(&webhookReady); err != nil {
		return domain.PaymentIntent{}, err
	}
	if !webhookReady {
		return domain.PaymentIntent{}, fmt.Errorf("%w: an active webhook covering required payment lifecycle events is required", domain.ErrStateConflict)
	}
	metadata := cmd.Metadata
	if len(metadata) == 0 {
		metadata = []byte(`{}`)
	}
	allowed, _ := json.Marshal(cmd.AllowedRoutes)
	_, err := tx.Exec(ctx, `INSERT INTO payment_intents (
id, tenant_id, merchant_id, merchant_order_id, customer_reference, amount_minor, currency, currency_scale,
description, metadata, allowed_routes, status, version, created_at, updated_at, expires_at)
VALUES ($1,$2,$3,$4,NULLIF($5,''),$6::numeric,$7,$8,$9,$10::jsonb,$11::jsonb,'awaiting_route_selection',1,$12,$12,$13)`,
		ids.IntentID, cmd.Principal.TenantID, cmd.Principal.MerchantID, cmd.MerchantOrderID, cmd.CustomerReference, cmd.AmountMinor.String(), cmd.Currency, cmd.CurrencyScale, cmd.Description, metadata, allowed, ids.Now, cmd.ExpiresAt.UTC())
	if err != nil {
		return domain.PaymentIntent{}, classify(err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO checkout_sessions(token_hash,tenant_id,merchant_id,intent_id,expires_at,created_at,audience,allowed_origin,allowed_actions,payment_link_id,session_nonce,version) VALUES($1,$2,$3,$4,$5,$6,$7,NULLIF($8,''),$9,NULLIF($10,'')::uuid,$11,1)`, ids.Checkout.TokenHash[:], cmd.Principal.TenantID, cmd.Principal.MerchantID, ids.IntentID, cmd.ExpiresAt.UTC(), ids.Now, ids.Checkout.Audience, ids.Checkout.AllowedOrigin, ids.Checkout.AllowedActions, ids.Checkout.PaymentLinkID, ids.Checkout.SessionNonce)
	if err != nil {
		return domain.PaymentIntent{}, classify(err)
	}
	intent := domain.PaymentIntent{ID: ids.IntentID, TenantID: cmd.Principal.TenantID, MerchantID: cmd.Principal.MerchantID, MerchantOrderID: cmd.MerchantOrderID, CustomerReference: cmd.CustomerReference, AmountMinor: cmd.AmountMinor, Currency: cmd.Currency, CurrencyScale: cmd.CurrencyScale, Description: cmd.Description, Metadata: append(json.RawMessage(nil), cmd.Metadata...), AllowedRoutes: append([]domain.RouteSelector(nil), cmd.AllowedRoutes...), Status: domain.IntentAwaitingRouteSelection, Version: 1, CreatedAt: ids.Now, UpdatedAt: ids.Now, ExpiresAt: cmd.ExpiresAt.UTC(), Routes: []domain.PaymentRoute{}, CheckoutToken: ids.Checkout.Token}
	if err = insertPaymentStateCallbackWithID(ctx, tx, cmd.Principal, intent, "payment.intent.created", cmd.CorrelationID, ids.EventID, ids.Now); err != nil {
		return domain.PaymentIntent{}, err
	}
	return intent, nil
}
