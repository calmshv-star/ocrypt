package management

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"time"

	corepostgres "github.com/calmshv-star/ocrypt/backend/internal/adapters/postgres"
	"github.com/calmshv-star/ocrypt/backend/internal/application"
	"github.com/calmshv-star/ocrypt/backend/internal/chains"
	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/ids"
	"github.com/calmshv-star/ocrypt/backend/internal/money"
	"github.com/jackc/pgx/v5"
)

// RedeemPaymentLink keeps the use counter, real core intent, checkout
// capability, and either an on-chain route or a durable hosted preparation job
// in one serializable transaction. Provider I/O is never performed in it.
func (s *PostgresRepository) RedeemPaymentLink(ctx context.Context, linkHash [32]byte, checkoutToken, checkoutURL string, checkoutHash [32]byte, input RedeemPaymentLinkInput, idem Idempotency) (result PaymentLinkRedemption, replay bool, err error) {
	var tenantID, merchantID, linkID string
	err = s.pool.QueryRow(ctx, `SELECT tenant_id::text,merchant_id::text,payment_link_id::text FROM lookup_payment_link($1)`, linkHash[:]).Scan(&tenantID, &merchantID, &linkID)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, false, ErrNotFound
	}
	if err != nil {
		return
	}
	p := Principal{TenantID: tenantID, MerchantID: merchantID}
	err = s.withinTenant(ctx, tenantID, func(tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `SELECT set_config('app.merchant_id',$1,true)`, merchantID); e != nil {
			return e
		}
		operation := "payment_link.redeem:" + linkID
		if found, e := s.replay(ctx, tx, p, operation, idem, &result); e != nil || found {
			replay = found
			return e
		}
		var name, amountRaw, currency, description, successURL, cancelURL, status string
		var scale uint8
		var routesRaw, linkMetadata []byte
		var maxUses, useCount, linkVersion int64
		var linkExpiry *time.Time
		e := tx.QueryRow(ctx, `SELECT name,amount_minor::text,currency,currency_scale,description,allowed_routes,metadata,success_url,cancel_url,status,max_uses,use_count,expires_at,version FROM payment_links WHERE id=$1 AND tenant_id=$2 AND merchant_id=$3 FOR UPDATE`, linkID, tenantID, merchantID).Scan(&name, &amountRaw, &currency, &scale, &description, &routesRaw, &linkMetadata, &successURL, &cancelURL, &status, &maxUses, &useCount, &linkExpiry, &linkVersion)
		if errors.Is(e, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if e != nil {
			return e
		}
		now := s.now()
		if status != "active" || useCount >= maxUses || linkExpiry != nil && !linkExpiry.After(now) {
			return ErrConflict
		}
		expires := now.Add(30 * time.Minute)
		if linkExpiry != nil && linkExpiry.Before(expires) {
			expires = *linkExpiry
		}
		if expires.Before(now.Add(time.Minute)) {
			return ErrConflict
		}
		amount, e := money.Parse(amountRaw)
		if e != nil {
			return e
		}
		var selectors []domain.RouteSelector
		if e = json.Unmarshal(routesRaw, &selectors); e != nil || len(selectors) != 1 {
			return ErrDependency
		}
		metadata, e := mergeRedemptionMetadata(linkMetadata, input.Metadata, linkID)
		if e != nil {
			return e
		}
		redemptionID, e := ids.New()
		if e != nil {
			return e
		}
		intentID, e := ids.New()
		if e != nil {
			return e
		}
		intentEventID, e := ids.New()
		if e != nil {
			return e
		}
		sessionNonce, e := ids.New()
		if e != nil {
			return e
		}
		principal := application.Principal{TenantID: tenantID, MerchantID: merchantID, Scopes: map[string]bool{"payments:write": true}}
		intentCommand := application.CreateIntent{Principal: principal, MerchantOrderID: "plink:" + linkID + ":" + redemptionID, CustomerReference: input.CustomerReference, AmountMinor: amount, Currency: currency, CurrencyScale: scale, Description: description, Metadata: metadata, AllowedRoutes: selectors, ExpiresAt: expires, CorrelationID: redemptionID}
		checkoutParsed, _ := url.Parse(checkoutURL)
		platformOrigin := checkoutParsed.Scheme + "://" + checkoutParsed.Host
		intent, e := s.core.CreateIntentInTx(ctx, tx, intentCommand, corepostgres.IntentInsertIDs{IntentID: intentID, EventID: intentEventID, Now: now, Checkout: corepostgres.CheckoutSessionInsert{Token: checkoutToken, TokenHash: checkoutHash, Audience: "payment_link", AllowedOrigin: platformOrigin, AllowedActions: []string{"read"}, PaymentLinkID: linkID, SessionNonce: sessionNonce}})
		if e != nil {
			return classifyCore(e)
		}
		var route domain.PaymentRoute
		hostedPreparing := selectors[0].Provider == domain.RouteProviderHostedGateway
		if hostedPreparing {
			var admitted bool
			if e = tx.QueryRow(ctx, `SELECT admit_hosted_provider_operation($1::uuid,$2::uuid,$3,'create',clock_timestamp())`, tenantID, merchantID, selectors[0].ProviderID).Scan(&admitted); e != nil || !admitted {
				if e != nil {
					return e
				}
				return ErrConflict
			}
			stableDigest := sha256.Sum256([]byte("hosted-payment-link-v1\x00" + linkID + "\x00" + idem.Key))
			providerKey := "hosted-plink-" + hex.EncodeToString(stableDigest[:])
			requestBody, _ := json.Marshal(struct {
				IntentID, ProviderID, AssetID, AmountMinor, Currency, IdempotencyKey string
				CurrencyScale                                                        uint8
				ExpiresAt                                                            time.Time
			}{intentID, selectors[0].ProviderID, selectors[0].AssetID, amount.String(), currency, providerKey, scale, expires.UTC()})
			providerRequestHash := sha256.Sum256(requestBody)
			attemptID, idErr := ids.New()
			if idErr != nil {
				return idErr
			}
			jobID, idErr := ids.New()
			if idErr != nil {
				return idErr
			}
			_, e = tx.Exec(ctx, `INSERT INTO hosted_provider_create_attempts(id,tenant_id,merchant_id,provider_id,intent_id,idempotency_key,request_hash,state,asset_id,fiat_amount_minor,currency,currency_scale,expires_at,recovery_status,next_recovery_at,created_at,updated_at,version) VALUES($1,$2,$3,$4,$5,$6,$7,'retry',$8,$9::numeric,$10,$11,$12,'pending',$13,$13,$13,1)`, attemptID, tenantID, merchantID, selectors[0].ProviderID, intentID, providerKey, providerRequestHash[:], selectors[0].AssetID, amount.String(), currency, scale, expires.UTC(), now)
			if e != nil {
				return classifyCore(e)
			}
			_, e = tx.Exec(ctx, `INSERT INTO hosted_payment_link_jobs(id,tenant_id,merchant_id,payment_link_id,redemption_id,intent_id,create_attempt_id,provider_id,asset_id,provider_idempotency_key,provider_request_hash,state,expires_at,created_at,updated_at,version) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,'preparing',$12,$13,$13,1)`, jobID, tenantID, merchantID, linkID, redemptionID, intentID, attemptID, selectors[0].ProviderID, selectors[0].AssetID, providerKey, providerRequestHash[:], expires.UTC(), now)
			if e != nil {
				return classifyManagement(e)
			}
		} else {
			planned, planErr := s.core.AllocateRouteInTx(ctx, tx, principal, intent, selectors[0].ChainID, selectors[0].AssetID, "", nil, expires)
			if planErr != nil {
				return classifyCore(planErr)
			}
			planned.CorrelationID = redemptionID
			route, e = s.core.CreateRouteInTx(ctx, tx, planned)
			if e != nil {
				return classifyCore(e)
			}
			command, updateErr := tx.Exec(ctx, `UPDATE checkout_sessions SET selected_route_id=$1,version=version+1 WHERE token_hash=$2 AND intent_id=$3 AND tenant_id=$4 AND merchant_id=$5 AND payment_link_id=$6`, route.ID, checkoutHash[:], intentID, tenantID, merchantID, linkID)
			if updateErr != nil {
				return updateErr
			}
			if command.RowsAffected() != 1 {
				return ErrConflict
			}
		}
		command, e := tx.Exec(ctx, `UPDATE payment_links SET use_count=use_count+1,updated_at=$1,version=version+1 WHERE id=$2 AND tenant_id=$3 AND merchant_id=$4 AND status='active' AND use_count<max_uses AND(expires_at IS NULL OR expires_at>$1)`, now, linkID, tenantID, merchantID)
		if e != nil {
			return e
		}
		if command.RowsAffected() != 1 {
			return ErrConflict
		}
		linkVersion++
		_, e = tx.Exec(ctx, `INSERT INTO payment_link_redemptions(id,tenant_id,merchant_id,payment_link_id,intent_id,checkout_token_hash,idempotency_key,request_hash,status,created_at)VALUES($1,$2,$3,$4,$5,$6,$7,$8,'bound',$9)`, redemptionID, tenantID, merchantID, linkID, intentID, checkoutHash[:], idem.Key, idem.Fingerprint[:], now)
		if e != nil {
			return classifyManagement(e)
		}
		session := CheckoutSession{IntentID: intentID, OrderID: intentCommand.MerchantOrderID, AmountMinor: amount.String(), Currency: currency, CurrencyScale: int16(scale), Description: description, Status: "preparing_payment_route", ExpiresAt: expires, Version: 1}
		if e = tx.QueryRow(ctx, `SELECT display_name FROM merchants WHERE id=$1 AND tenant_id=$2`, merchantID, tenantID).Scan(&session.MerchantName); e != nil {
			return e
		}
		if !hostedPreparing {
			session.Status = "pending"
			session.SelectedRouteID = route.ID
			session.Routes = []CheckoutRoute{{ID: route.ID, Provider: route.Provider, ProviderID: route.ProviderID, Network: route.ChainID, Asset: route.AssetID, Amount: route.DisplayAmount, Address: chains.DisplayAddress(route.ChainID, route.Address), PaymentURL: route.PaymentURL}}
			session.Version = 2
		}
		result = PaymentLinkRedemption{IntentID: intentID, Checkout: CheckoutIssue{Token: checkoutToken, URL: checkoutURL, ExpiresAt: expires}, Session: session, SuccessURL: successURL, CancelURL: cancelURL}
		if e = insertPaymentLinkRedeemedEvent(ctx, tx, tenantID, merchantID, linkID, intentID, redemptionID, linkVersion, now); e != nil {
			return e
		}
		return s.remember(ctx, tx, p, operation, idem, "payment_link_redemption", redemptionID, 201, result)
	})
	return
}

func mergeRedemptionMetadata(link, customer []byte, linkID string) (json.RawMessage, error) {
	var base map[string]any
	if json.Unmarshal(link, &base) != nil || base == nil {
		base = map[string]any{}
	}
	base["payment_link_id"] = linkID
	if len(customer) > 0 {
		var value map[string]any
		if json.Unmarshal(customer, &value) != nil {
			return nil, ErrInvalid
		}
		base["redemption_metadata"] = value
	}
	encoded, err := json.Marshal(base)
	if err != nil || len(encoded) > 16_384 {
		return nil, ErrInvalid
	}
	return encoded, nil
}

func insertPaymentLinkRedeemedEvent(ctx context.Context, tx pgx.Tx, tenantID, merchantID, linkID, intentID, redemptionID string, version int64, now time.Time) error {
	id, e := ids.New()
	if e != nil {
		return e
	}
	payload, _ := json.Marshal(map[string]any{"event_id": id, "event_type": "payment_link.redeemed", "payment_link_id": linkID, "payment_intent_id": intentID, "redemption_id": redemptionID, "occurred_at": now})
	_, e = tx.Exec(ctx, `INSERT INTO outbox_events(id,tenant_id,merchant_id,aggregate_type,aggregate_id,aggregate_version,aggregate_sequence,event_type,schema_version,payload,correlation_id,occurred_at,recorded_at,available_at)VALUES($1,$2,$3,'payment_link',$4,$5,$5,'payment_link.redeemed','1',$6::jsonb,$7,$8,$8,$8)`, id, tenantID, merchantID, linkID, version, payload, redemptionID, now)
	return classifyManagement(e)
}

func classifyCore(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, domain.ErrNotFound) {
		return ErrNotFound
	}
	if errors.Is(err, domain.ErrValidation) {
		return ErrInvalid
	}
	if errors.Is(err, domain.ErrStateConflict) || errors.Is(err, domain.ErrVersionConflict) || errors.Is(err, domain.ErrIdempotencyConflict) {
		return ErrConflict
	}
	return fmt.Errorf("%w: payment core", ErrDependency)
}
