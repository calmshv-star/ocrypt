package postgres

import (
	"context"
	"fmt"

	"github.com/calmshv-star/ocrypt/backend/internal/application"
	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/ids"
	"github.com/jackc/pgx/v5"
)

// CreateRouteInTx is the typed, non-committing route boundary shared by the
// merchant route API and atomic payment-link redemption.
func (s *Store) CreateRouteInTx(ctx context.Context, tx pgx.Tx, cmd application.CreateRoute) (route domain.PaymentRoute, err error) {
	if err = setCorrelation(ctx, tx, cmd.CorrelationID); err != nil {
		return route, err
	}
	intent, err := getIntent(ctx, tx, cmd.Principal, cmd.IntentID, true)
	if err != nil {
		return route, err
	}
	if intent.Status != domain.IntentAwaitingRouteSelection && intent.Status != domain.IntentPending {
		return route, domain.ErrStateConflict
	}
	if (cmd.QuoteID == "") != (cmd.AddressAssignmentID == "") {
		return route, fmt.Errorf("%w: quote and address assignment must be bound together", domain.ErrInvariantViolation)
	}
	if cmd.QuoteID != "" {
		var valid bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM rate_quotes q JOIN address_assignments aa ON aa.tenant_id=q.tenant_id AND aa.intent_id=q.payment_intent_id JOIN addresses a ON a.id=aa.address_id AND a.tenant_id=aa.tenant_id WHERE q.id=$1 AND aa.id=$2 AND q.tenant_id=$3 AND q.merchant_id=$4 AND q.payment_intent_id=$5 AND q.asset_id=$6 AND q.crypto_amount_atomic=$7::numeric AND q.expires_at>clock_timestamp() AND aa.chain_id=$8 AND aa.status='leased' AND aa.valid_until>clock_timestamp() AND a.canonical_address=$9)`, cmd.QuoteID, cmd.AddressAssignmentID, cmd.Principal.TenantID, cmd.Principal.MerchantID, cmd.IntentID, cmd.AssetID, cmd.ExpectedAmount.String(), cmd.ChainID, cmd.Address).Scan(&valid)
		if err != nil {
			return route, err
		}
		if !valid {
			return route, fmt.Errorf("%w: persisted quote or address lease is invalid", domain.ErrStateConflict)
		}
	}
	routeID, err := ids.New()
	if err != nil {
		return route, err
	}
	provider := cmd.Provider
	if provider == "" {
		provider = domain.RouteProviderOnChain
	}
	eventID, err := ids.New()
	if err != nil {
		return route, err
	}
	now := s.now()
	route = domain.PaymentRoute{ID: routeID, IntentID: intent.ID, QuoteID: cmd.QuoteID, AddressAssignmentID: cmd.AddressAssignmentID, ChainID: cmd.ChainID, AssetID: cmd.AssetID, Provider: provider, ProviderID: cmd.ProviderID, ProviderOrderID: cmd.ProviderOrderID, ProviderReference: cmd.ProviderReference, PaymentURL: cmd.PaymentURL, ExpectedAmount: cmd.ExpectedAmount, AssetDecimals: cmd.AssetDecimals, DisplayAmount: cmd.DisplayAmount, Address: cmd.Address, Memo: cmd.Memo, RequiredFinality: cmd.RequiredFinality, Status: domain.RouteActive, Version: 1, StartsAt: now, ExpiresAt: cmd.ExpiresAt.UTC(), GraceEndsAt: cmd.GraceEndsAt.UTC()}
	if provider == domain.RouteProviderHostedGateway {
		requestDigest, err := decodeRequestHash(cmd.RequestHash)
		if err != nil {
			return route, err
		}
		command, err := tx.Exec(ctx, `INSERT INTO provider_orders(id,tenant_id,merchant_id,route_id,provider_id,provider_reference,provider_status,asset_id,fiat_amount_minor,currency,currency_scale,amount_atomic,asset_decimals,quote_id,rate_numerator,rate_denominator,quote_issued_at,create_response_body,create_response_digest,create_response_received_at,payment_url,provider_idempotency_key,created_at,updated_at,version)
SELECT a.provider_order_id,a.tenant_id,a.merchant_id,$1,a.provider_id,a.provider_reference,'pending',a.asset_id,a.fiat_amount_minor,a.currency,a.currency_scale,a.amount_atomic,a.asset_decimals,a.quote_id,a.rate_numerator,a.rate_denominator,a.quote_issued_at,a.create_response_body,a.create_response_digest,a.create_response_received_at,a.payment_url,a.idempotency_key,$2,$2,1
FROM hosted_provider_create_attempts a WHERE a.tenant_id=$3 AND a.merchant_id=$4 AND a.intent_id=$5 AND a.idempotency_key=$6 AND a.request_hash=$7 AND a.state='completed' AND a.provider_order_id=$8 AND a.provider_id=$9 AND a.provider_reference=$10 AND a.payment_url=$11 AND a.asset_id=$12 AND a.amount_atomic=$13::numeric AND(a.recovery_status<>'claimed' OR a.recovery_claim_until>=clock_timestamp())`, routeID, now, cmd.Principal.TenantID, cmd.Principal.MerchantID, intent.ID, cmd.IdempotencyKey, requestDigest, cmd.ProviderOrderID, cmd.ProviderID, cmd.ProviderReference, cmd.PaymentURL, cmd.AssetID, cmd.ExpectedAmount.String())
		if err != nil {
			return route, classify(err)
		}
		if command.RowsAffected() != 1 {
			return route, fmt.Errorf("%w: hosted provider create fence is missing or changed", domain.ErrStateConflict)
		}
		command, err = tx.Exec(ctx, `UPDATE hosted_provider_create_attempts SET recovery_status='complete',recovery_claim_token=NULL,recovery_claim_until=NULL,last_recovery_error_code=NULL,updated_at=$1,version=version+1 WHERE provider_order_id=$2 AND tenant_id=$3 AND merchant_id=$4 AND state='completed' AND(recovery_status<>'claimed' OR recovery_claim_until>=clock_timestamp())`, now, cmd.ProviderOrderID, cmd.Principal.TenantID, cmd.Principal.MerchantID)
		if err != nil {
			return route, err
		}
		if command.RowsAffected() != 1 {
			return route, domain.ErrVersionConflict
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO payment_routes(id,tenant_id,merchant_id,intent_id,quote_id,address_assignment_id,chain_id,asset_id,provider,provider_order_id,provider_id,provider_reference,payment_url,expected_amount_atomic,asset_decimals,display_amount,receiving_address,memo,required_finality,status,starts_at,expires_at,grace_ends_at,version,created_at,updated_at)VALUES($1,$2,$3,$4,NULLIF($5,'')::uuid,NULLIF($6,'')::uuid,NULLIF($7,''),$8,$9,NULLIF($10,'')::uuid,NULLIF($11,''),NULLIF($12,''),NULLIF($13,''),$14::numeric,$15,$16,NULLIF($17,''),NULLIF($18,''),$19,'active',$20,$21,$22,1,$20,$20)`, routeID, cmd.Principal.TenantID, cmd.Principal.MerchantID, intent.ID, cmd.QuoteID, cmd.AddressAssignmentID, cmd.ChainID, cmd.AssetID, provider, cmd.ProviderOrderID, cmd.ProviderID, cmd.ProviderReference, cmd.PaymentURL, cmd.ExpectedAmount.String(), cmd.AssetDecimals, cmd.DisplayAmount, cmd.Address, cmd.Memo, cmd.RequiredFinality, now, cmd.ExpiresAt.UTC(), cmd.GraceEndsAt.UTC())
	if err != nil {
		return route, classify(err)
	}
	if cmd.AddressAssignmentID != "" {
		command, e := tx.Exec(ctx, `UPDATE address_assignments SET status='bound',route_id=$1,updated_at=$2,version=version+1 WHERE id=$3 AND tenant_id=$4 AND intent_id=$5 AND status='leased'`, routeID, now, cmd.AddressAssignmentID, cmd.Principal.TenantID, intent.ID)
		if e != nil {
			return route, e
		}
		if command.RowsAffected() != 1 {
			return route, domain.ErrVersionConflict
		}
	}
	if provider == domain.RouteProviderOnChain {
		reservationID, err := ids.New()
		if err != nil {
			return route, err
		}
		_, err = tx.Exec(ctx, `INSERT INTO amount_reservations(id,tenant_id,route_id,chain_id,receiving_address,asset_id,exact_amount_atomic,active_window,state,created_at,updated_at)VALUES($1,$2,$3,$4,$5,$6,$7::numeric,tstzrange($8,$9,'[)'),'active',$10,$10)`, reservationID, cmd.Principal.TenantID, routeID, cmd.ChainID, cmd.Address, cmd.AssetID, cmd.ExpectedAmount.String(), now, cmd.GraceEndsAt.UTC(), now)
		if err != nil {
			return route, classify(err)
		}
	}
	newStatus := intent.Status
	if intent.Status == domain.IntentAwaitingRouteSelection {
		newStatus = domain.IntentPending
	}
	command, err := tx.Exec(ctx, `UPDATE payment_intents SET status=$1,status_reason='route_created',version=version+1,updated_at=$2 WHERE id=$3 AND tenant_id=$4 AND version=$5`, newStatus, now, intent.ID, cmd.Principal.TenantID, intent.Version)
	if err != nil {
		return route, err
	}
	if command.RowsAffected() != 1 {
		return route, domain.ErrVersionConflict
	}
	intent.Version++
	intent.Status = newStatus
	intent.StatusReason = "route_created"
	intent.UpdatedAt = now
	intent.Routes = append(intent.Routes, route)
	if err = insertPaymentStateCallbackWithID(ctx, tx, cmd.Principal, intent, "payment.route.created", cmd.CorrelationID, eventID, now); err != nil {
		return route, err
	}
	return route, nil
}
