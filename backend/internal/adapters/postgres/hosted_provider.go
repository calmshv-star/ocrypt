package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/application"
	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/ids"
	"github.com/calmshv-star/ocrypt/backend/internal/money"
	"github.com/jackc/pgx/v5"
)

func (s *Store) GetConfig(ctx context.Context, principal application.Principal, providerID string) (cfg domain.HostedProviderConfig, err error) {
	err = s.withinMerchant(ctx, principal, func(tx pgx.Tx) error {
		return scanHostedConfig(tx.QueryRow(ctx, `SELECT id,tenant_id::text,merchant_id::text,adapter_kind,api_origin,create_path,cancel_path,status_path,refund_path,reconcile_path,payment_url_origins,api_credential_ref,api_key_id,callback_secret_ref,callback_key_id,signature_scheme,asset_id,asset_decimals,currency,status,config_manifest_id::text,config_version FROM hosted_provider_outbound_config_admitted($1,$2,$3,'create')`, principal.TenantID, principal.MerchantID, providerID), &cfg)
	})
	return cfg, err
}

func (s *Store) AdmitMerchantHostedOperation(ctx context.Context, principal application.Principal, providerID, operation string) (err error) {
	if operation != "create" && operation != "status" && operation != "cancel" && operation != "refund" && operation != "reconciliation" {
		return domain.ErrValidation
	}
	err = s.withinMerchant(ctx, principal, func(tx pgx.Tx) error {
		var admitted bool
		if err := tx.QueryRow(ctx, `SELECT admit_hosted_provider_operation($1::uuid,$2::uuid,$3,$4,$5)`, principal.TenantID, principal.MerchantID, providerID, operation, s.now()).Scan(&admitted); err != nil {
			return err
		}
		if !admitted {
			return fmt.Errorf("%w: hosted provider operation is not admitted", domain.ErrStateConflict)
		}
		return nil
	})
	return err
}

func (s *Store) GetCallbackConfig(ctx context.Context, providerID, keyID string) (cfg domain.HostedProviderConfig, err error) {
	err = scanHostedConfig(s.db.pool.QueryRow(ctx, `SELECT id,tenant_id::text,merchant_id::text,adapter_kind,api_origin,create_path,cancel_path,status_path,refund_path,reconcile_path,payment_url_origins,api_credential_ref,api_key_id,callback_secret_ref,callback_key_id,signature_scheme,asset_id,asset_decimals,currency,status,config_manifest_id::text,config_version FROM hosted_provider_callback_config_admitted($1,$2)`, providerID, keyID), &cfg)
	return cfg, err
}

// HostedProviderReady verifies the schema and the exact API-role capabilities
// used by the hosted route/callback runtime. A healthy database connection is
// not sufficient: a missing migration or a revoked grant must keep /readyz
// closed before the API advertises hosted providers.
func (s *Store) HostedProviderReady(ctx context.Context) error {
	var ready bool
	err := s.db.pool.QueryRow(ctx, `SELECT
  to_regclass('public.hosted_provider_configs') IS NOT NULL
  AND to_regclass('public.hosted_provider_create_attempts') IS NOT NULL
  AND to_regclass('public.provider_orders') IS NOT NULL
  AND to_regclass('public.provider_inbox') IS NOT NULL
  AND to_regclass('public.provider_prebind_inbox') IS NOT NULL
  AND to_regclass('public.hosted_provider_runtime_incidents') IS NOT NULL
	  AND to_regclass('public.hosted_provider_config_heads') IS NOT NULL
	  AND to_regprocedure('public.hosted_provider_callback_config_admitted(text,text)') IS NOT NULL
	  AND to_regprocedure('public.hosted_provider_outbound_config_admitted(uuid,uuid,text,text)') IS NOT NULL
  AND to_regprocedure('public.admit_hosted_provider_operation(uuid,uuid,text,text,timestamp with time zone)') IS NOT NULL
	  AND has_function_privilege(current_user,'public.hosted_provider_callback_config_admitted(text,text)','EXECUTE')
	  AND has_function_privilege(current_user,'public.hosted_provider_outbound_config_admitted(uuid,uuid,text,text)','EXECUTE')
  AND has_function_privilege(current_user,'public.admit_hosted_provider_operation(uuid,uuid,text,text,timestamp with time zone)','EXECUTE')
  AND has_table_privilege(current_user,'public.hosted_provider_configs','SELECT')
  AND has_table_privilege(current_user,'public.hosted_provider_create_attempts','SELECT,INSERT,UPDATE')
  AND has_table_privilege(current_user,'public.provider_orders','SELECT,INSERT,UPDATE')
  AND has_table_privilege(current_user,'public.provider_inbox','SELECT,INSERT')
  AND has_table_privilege(current_user,'public.provider_prebind_inbox','SELECT,INSERT,UPDATE')
  AND has_table_privilege(current_user,'public.provider_reconciliation_incidents','SELECT,INSERT')
  AND has_table_privilege(current_user,'public.provider_reconcile_observations','SELECT,INSERT')
  AND has_table_privilege(current_user,'public.hosted_provider_runtime_incidents','SELECT,INSERT')`).Scan(&ready)
	if err != nil {
		return fmt.Errorf("check hosted provider readiness: %w", err)
	}
	if !ready {
		return errors.New("hosted provider schema or API grants are not ready")
	}
	return nil
}

type rowScanner interface{ Scan(...any) error }

func scanHostedConfig(row rowScanner, cfg *domain.HostedProviderConfig) error {
	err := row.Scan(&cfg.ID, &cfg.TenantID, &cfg.MerchantID, &cfg.AdapterKind, &cfg.APIOrigin, &cfg.CreatePath, &cfg.CancelPath, &cfg.StatusPath, &cfg.RefundPath, &cfg.ReconcilePath, &cfg.PaymentURLOrigins, &cfg.CredentialRef, &cfg.APIKeyID, &cfg.CallbackSecretRef, &cfg.CallbackKeyID, &cfg.CallbackSignatureKind, &cfg.AssetID, &cfg.AssetDecimals, &cfg.Currency, &cfg.Status, &cfg.ConfigManifestID, &cfg.ConfigVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	return err
}

func (s *Store) ClaimCreate(ctx context.Context, principal application.Principal, request domain.HostedCreateRequest, lease time.Duration) (fence domain.HostedCreateFence, err error) {
	digest, err := decodeRequestHash(request.RequestHash)
	if err != nil {
		return fence, err
	}
	err = s.withinMerchant(ctx, principal, func(tx pgx.Tx) error {
		if err := lockIdempotency(ctx, tx, principal.MerchantID, "hosted_provider_create", request.IdempotencyKey); err != nil {
			return err
		}
		var state, providerID, intentID, assetID, fiatAmount, currency, reference, paymentURL, orderID string
		var storedDigest []byte
		var currencyScale uint8
		var expiresAt time.Time
		var claimUntil *time.Time
		err := tx.QueryRow(ctx, `SELECT state,provider_id,intent_id::text,request_hash,asset_id,fiat_amount_minor::text,currency::text,currency_scale,expires_at,COALESCE(provider_order_id::text,''),COALESCE(provider_reference,''),COALESCE(payment_url,''),claim_until FROM hosted_provider_create_attempts WHERE merchant_id=$1 AND idempotency_key=$2 FOR UPDATE`, principal.MerchantID, request.IdempotencyKey).Scan(&state, &providerID, &intentID, &storedDigest, &assetID, &fiatAmount, &currency, &currencyScale, &expiresAt, &orderID, &reference, &paymentURL, &claimUntil)
		if err == nil {
			if providerID != request.ProviderID || intentID != request.IntentID || !bytes.Equal(storedDigest, digest) || assetID != request.AssetID || fiatAmount != request.FiatAmountMinor.String() || currency != request.Currency || currencyScale != request.CurrencyScale || !expiresAt.Equal(request.ExpiresAt.UTC()) {
				return domain.ErrIdempotencyConflict
			}
			if state == "completed" {
				var amount, numerator, denominator, quoteID string
				var decimals uint8
				var quoteIssuedAt time.Time
				var responseDigest, rawResponse []byte
				var responseReceivedAt time.Time
				if err := tx.QueryRow(ctx, `SELECT amount_atomic::text,asset_decimals,quote_id,rate_numerator::text,rate_denominator::text,quote_issued_at,create_response_body,create_response_digest,create_response_received_at FROM hosted_provider_create_attempts WHERE merchant_id=$1 AND idempotency_key=$2`, principal.MerchantID, request.IdempotencyKey).Scan(&amount, &decimals, &quoteID, &numerator, &denominator, &quoteIssuedAt, &rawResponse, &responseDigest, &responseReceivedAt); err != nil {
					return err
				}
				parsedAmount, err := money.Parse(amount)
				if err != nil {
					return err
				}
				parsedNumerator, err := money.Parse(numerator)
				if err != nil {
					return err
				}
				parsedDenominator, err := money.Parse(denominator)
				if err != nil {
					return err
				}
				fence.Completed = true
				fence.Result = domain.HostedCreateResult{ProviderOrderID: orderID, ProviderReference: reference, PaymentURL: paymentURL, AssetID: assetID, Amount: parsedAmount, AssetDecimals: decimals, QuoteID: quoteID, RateNumerator: parsedNumerator, RateDenominator: parsedDenominator, QuoteIssuedAt: quoteIssuedAt, RawResponse: append([]byte(nil), rawResponse...), ResponseReceivedAt: responseReceivedAt, ExpiresAt: expiresAt}
				copy(fence.Result.ResponseDigest[:], responseDigest)
				return nil
			}
			now := s.now()
			if state == "claimed" && claimUntil != nil && claimUntil.After(now) {
				return nil
			}
			claimToken, err := ids.New()
			if err != nil {
				return err
			}
			_, err = tx.Exec(ctx, `UPDATE hosted_provider_create_attempts SET state='claimed',claim_token=$1,claim_until=$2,last_error_code=NULL,updated_at=$3,version=version+1 WHERE merchant_id=$4 AND idempotency_key=$5`, claimToken, now.Add(lease), now, principal.MerchantID, request.IdempotencyKey)
			fence.Claimed = err == nil
			return err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		attemptID, err := ids.New()
		if err != nil {
			return err
		}
		claimToken, err := ids.New()
		if err != nil {
			return err
		}
		now := s.now()
		_, err = tx.Exec(ctx, `INSERT INTO hosted_provider_create_attempts(id,tenant_id,merchant_id,provider_id,intent_id,idempotency_key,request_hash,state,claim_token,claim_until,asset_id,fiat_amount_minor,currency,currency_scale,expires_at,created_at,updated_at,version) VALUES($1,$2,$3,$4,$5,$6,$7,'claimed',$8,$9,$10,$11::numeric,$12,$13,$14,$15,$15,1)`, attemptID, principal.TenantID, principal.MerchantID, request.ProviderID, request.IntentID, request.IdempotencyKey, digest, claimToken, now.Add(lease), request.AssetID, request.FiatAmountMinor.String(), request.Currency, request.CurrencyScale, request.ExpiresAt.UTC(), now)
		fence.Claimed = err == nil
		return classify(err)
	})
	return fence, err
}

func (s *Store) CompleteCreate(ctx context.Context, principal application.Principal, request domain.HostedCreateRequest, result domain.HostedCreateResult) (completed domain.HostedCreateResult, err error) {
	digest, err := decodeRequestHash(request.RequestHash)
	if err != nil {
		return completed, err
	}
	err = s.withinMerchant(ctx, principal, func(tx pgx.Tx) error {
		if err := lockIdempotency(ctx, tx, principal.MerchantID, "hosted_provider_create", request.IdempotencyKey); err != nil {
			return err
		}
		var state, providerID, intentID, fiatAmount, currency string
		var storedDigest []byte
		var currencyScale uint8
		err := tx.QueryRow(ctx, `SELECT state,provider_id,intent_id::text,request_hash,fiat_amount_minor::text,currency::text,currency_scale FROM hosted_provider_create_attempts WHERE merchant_id=$1 AND idempotency_key=$2 FOR UPDATE`, principal.MerchantID, request.IdempotencyKey).Scan(&state, &providerID, &intentID, &storedDigest, &fiatAmount, &currency, &currencyScale)
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrStateConflict
		}
		if err != nil {
			return err
		}
		expected, ratioErr := request.FiatAmountMinor.MulDivCeil(result.RateNumerator, result.RateDenominator)
		responseDigest := sha256.Sum256(result.RawResponse)
		if providerID != request.ProviderID || intentID != request.IntentID || !bytes.Equal(storedDigest, digest) || fiatAmount != request.FiatAmountMinor.String() || currency != request.Currency || currencyScale != request.CurrencyScale || result.AssetID != request.AssetID || ratioErr != nil || result.Amount.Cmp(expected) != 0 || result.QuoteID == "" || result.RateNumerator.IsZero() || result.RateDenominator.IsZero() || len(result.RawResponse) == 0 || len(result.RawResponse) > 256<<10 || !bytes.Equal(responseDigest[:], result.ResponseDigest[:]) || result.ResponseReceivedAt.IsZero() {
			return domain.ErrIdempotencyConflict
		}
		if state == "completed" {
			var storedAmount string
			var numerator, denominator string
			var responseDigest []byte
			err := tx.QueryRow(ctx, `SELECT provider_order_id::text,provider_reference,payment_url,asset_id,amount_atomic::text,asset_decimals,quote_id,rate_numerator::text,rate_denominator::text,quote_issued_at,create_response_body,create_response_digest,create_response_received_at,expires_at FROM hosted_provider_create_attempts WHERE merchant_id=$1 AND idempotency_key=$2`, principal.MerchantID, request.IdempotencyKey).Scan(&completed.ProviderOrderID, &completed.ProviderReference, &completed.PaymentURL, &completed.AssetID, &storedAmount, &completed.AssetDecimals, &completed.QuoteID, &numerator, &denominator, &completed.QuoteIssuedAt, &completed.RawResponse, &responseDigest, &completed.ResponseReceivedAt, &completed.ExpiresAt)
			if err != nil {
				return err
			}
			completed.Amount, err = money.Parse(storedAmount)
			if err != nil {
				return err
			}
			completed.RateNumerator, err = money.Parse(numerator)
			if err != nil {
				return err
			}
			completed.RateDenominator, err = money.Parse(denominator)
			copy(completed.ResponseDigest[:], responseDigest)
			return err
		}
		orderID, err := ids.New()
		if err != nil {
			return err
		}
		now := s.now()
		command, err := tx.Exec(ctx, `UPDATE hosted_provider_create_attempts SET state='completed',claim_token=NULL,claim_until=NULL,provider_order_id=$1,provider_reference=$2,payment_url=$3,amount_atomic=$4::numeric,asset_decimals=$5,quote_id=$6,rate_numerator=$7::numeric,rate_denominator=$8::numeric,quote_issued_at=$9,create_response_body=$10,create_response_digest=$11,create_response_received_at=$12,updated_at=$13,version=version+1 WHERE merchant_id=$14 AND idempotency_key=$15 AND state='claimed' AND claim_until>=clock_timestamp()`, orderID, result.ProviderReference, result.PaymentURL, result.Amount.String(), result.AssetDecimals, result.QuoteID, result.RateNumerator.String(), result.RateDenominator.String(), result.QuoteIssuedAt.UTC(), result.RawResponse, result.ResponseDigest[:], result.ResponseReceivedAt.UTC(), now, principal.MerchantID, request.IdempotencyKey)
		if err != nil {
			return classify(err)
		}
		if command.RowsAffected() != 1 {
			return domain.ErrVersionConflict
		}
		completed = result
		completed.ProviderOrderID = orderID
		return nil
	})
	return completed, err
}

func (s *Store) ReleaseCreate(ctx context.Context, principal application.Principal, request domain.HostedCreateRequest, reason string) error {
	return s.withinMerchant(ctx, principal, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE hosted_provider_create_attempts SET state='retry',claim_token=NULL,claim_until=NULL,last_error_code=$1,updated_at=$2,version=version+1 WHERE merchant_id=$3 AND idempotency_key=$4 AND provider_id=$5 AND state='claimed' AND claim_until>=clock_timestamp()`, reason, s.now(), principal.MerchantID, request.IdempotencyKey, request.ProviderID)
		return err
	})
}

func (s *Store) withinMerchant(ctx context.Context, principal application.Principal, fn func(pgx.Tx) error) error {
	if principal.TenantID == "" || principal.MerchantID == "" {
		return fmt.Errorf("%w: tenant and merchant are required", domain.ErrValidation)
	}
	return s.db.WithinTenant(ctx, principal.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT set_config('app.merchant_id',$1,true)`, principal.MerchantID); err != nil {
			return fmt.Errorf("set merchant context: %w", err)
		}
		return fn(tx)
	})
}

func setMerchantContext(ctx context.Context, tx pgx.Tx, merchantID string) error {
	_, err := tx.Exec(ctx, `SELECT set_config('app.merchant_id',$1,true)`, merchantID)
	return err
}

func decodeRequestHash(value string) ([]byte, error) {
	digest, err := hex.DecodeString(value)
	if err != nil || len(digest) != 32 || hex.EncodeToString(digest) != value {
		return nil, fmt.Errorf("%w: request hash must be canonical SHA-256", domain.ErrValidation)
	}
	return digest, nil
}
