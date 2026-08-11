package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/application"
	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/hostedproviders"
	"github.com/calmshv-star/ocrypt/backend/internal/ids"
	"github.com/calmshv-star/ocrypt/backend/internal/money"
	"github.com/jackc/pgx/v5"
)

func (s *Store) ClaimHostedRecoveries(ctx context.Context, workerID string, now time.Time, lease time.Duration, limit int) (jobs []hostedproviders.RecoveryJob, err error) {
	if workerID == "" || lease <= 0 || limit < 1 || limit > 100 {
		return nil, fmt.Errorf("%w: invalid hosted recovery claim", domain.ErrValidation)
	}
	err = pgx.BeginTxFunc(ctx, s.db.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text,claim_token::text,attempt,tenant_id::text,merchant_id::text,provider_id,intent_id::text,idempotency_key,request_hash,asset_id,fiat_amount_minor,currency,currency_scale,expires_at,create_state,provider_order_id,provider_reference,payment_url,amount_atomic,asset_decimals,quote_id,rate_numerator,rate_denominator,quote_issued_at,create_response_body,create_response_digest,create_response_received_at FROM claim_hosted_create_recoveries($1,$2,$3)`, now.UTC(), now.Add(lease).UTC(), limit)
		if err != nil {
			return err
		}
		for rows.Next() {
			job, err := scanHostedCreateRecovery(rows)
			if err != nil {
				rows.Close()
				return err
			}
			jobs = append(jobs, job)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()

		remaining := limit - len(jobs)
		if remaining > 0 {
			rows, err = tx.Query(ctx, `SELECT id::text,claim_token::text,attempt,tenant_id::text,merchant_id::text,provider_id,route_id,provider_event_id,provider_reference,provider_status,asset_id,amount_atomic,asset_decimals,raw_body,raw_body_digest,signature_scheme,signature_key_id,signature_digest,config_manifest_id::text,config_version,provider_paused_at_receipt,provider_occurred_at,received_at FROM claim_hosted_prebind_recoveries($1,$2,$3)`, now.UTC(), now.Add(lease).UTC(), remaining)
			if err != nil {
				return err
			}
			for rows.Next() {
				var job hostedproviders.RecoveryJob
				var amount string
				var rawDigest, signatureDigest []byte
				job.Kind = hostedproviders.RecoveryPrebind
				if err := rows.Scan(&job.ID, &job.ClaimToken, &job.Attempt, &job.Payment.TenantID, &job.Payment.MerchantID, &job.Payment.ProviderID, &job.RouteID, &job.Payment.ProviderEventID, &job.Payment.ProviderReference, &job.Payment.ProviderStatus, &job.Payment.AssetID, &amount, &job.Payment.AssetDecimals, &job.Payment.RawBody, &rawDigest, &job.Payment.SignatureScheme, &job.Payment.SignatureKeyID, &signatureDigest, &job.Payment.ConfigManifestID, &job.Payment.ConfigVersion, &job.Payment.ProviderPaused, &job.Payment.OccurredAt, &job.Payment.ReceivedAt); err != nil {
					rows.Close()
					return err
				}
				job.Config.ID, job.Config.TenantID, job.Config.MerchantID = job.Payment.ProviderID, job.Payment.TenantID, job.Payment.MerchantID
				job.Payment.Amount, err = money.Parse(amount)
				if err != nil {
					rows.Close()
					return err
				}
				copy(job.Payment.RawDigest[:], rawDigest)
				copy(job.Payment.SignatureDigest[:], signatureDigest)
				jobs = append(jobs, job)
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				return err
			}
			rows.Close()
		}

		remaining = limit - len(jobs)
		if remaining > 0 {
			rows, err = tx.Query(ctx, `SELECT id::text,claim_token::text,attempt,tenant_id::text,merchant_id::text,route_id::text,provider_id,provider_reference,asset_id,amount_atomic,asset_decimals FROM claim_hosted_order_recoveries($1,$2,$3)`, now.UTC(), now.Add(lease).UTC(), remaining)
			if err != nil {
				return err
			}
			for rows.Next() {
				var job hostedproviders.RecoveryJob
				var tenantID, merchantID, amount string
				job.Kind = hostedproviders.RecoveryStatus
				if err := rows.Scan(&job.ID, &job.ClaimToken, &job.Attempt, &tenantID, &merchantID, &job.RouteID, &job.Config.ID, &job.CreateResult.ProviderReference, &job.CreateResult.AssetID, &amount, &job.CreateResult.AssetDecimals); err != nil {
					rows.Close()
					return err
				}
				job.Config.TenantID, job.Config.MerchantID = tenantID, merchantID
				job.CreateResult.ProviderOrderID = job.ID
				job.CreateResult.Amount, err = money.Parse(amount)
				if err != nil {
					rows.Close()
					return err
				}
				jobs = append(jobs, job)
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				return err
			}
			rows.Close()
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for i := range jobs {
		if jobs[i].Kind == hostedproviders.RecoveryPrebind {
			continue
		}
		principal := application.Principal{TenantID: jobs[i].Config.TenantID, MerchantID: jobs[i].Config.MerchantID}
		cfg := domain.HostedProviderConfig{}
		operation := "create"
		if jobs[i].Kind == hostedproviders.RecoveryStatus {
			operation = "reconciliation"
		}
		if err := s.withinMerchant(ctx, principal, func(tx pgx.Tx) error {
			return scanHostedConfig(tx.QueryRow(ctx, `SELECT id,tenant_id::text,merchant_id::text,adapter_kind,api_origin,create_path,cancel_path,status_path,refund_path,reconcile_path,payment_url_origins,api_credential_ref,api_key_id,callback_secret_ref,callback_key_id,signature_scheme,asset_id,asset_decimals,currency,status,config_manifest_id::text,config_version FROM hosted_provider_outbound_config_admitted($1,$2,$3,$4)`, jobs[i].Config.TenantID, jobs[i].Config.MerchantID, jobs[i].Config.ID, operation), &cfg)
		}); err != nil {
			return nil, err
		}
		jobs[i].Config = cfg
	}
	return jobs, nil
}

func (s *Store) AdmitHostedOperation(ctx context.Context, job hostedproviders.RecoveryJob, operation string) error {
	if operation != "create" && operation != "cancel" && operation != "reconciliation" {
		return domain.ErrValidation
	}
	var admitted bool
	if err := s.db.pool.QueryRow(ctx, `SELECT admit_hosted_provider_operation($1::uuid,$2::uuid,$3,$4,$5)`, job.Config.TenantID, job.Config.MerchantID, job.Config.ID, operation, s.now()).Scan(&admitted); err != nil {
		return err
	}
	if !admitted {
		return fmt.Errorf("%w: hosted provider operation is paused", domain.ErrStateConflict)
	}
	return nil
}

func (s *Store) HostedRecoveryReady(ctx context.Context) error {
	var ready bool
	if err := s.db.pool.QueryRow(ctx, `SELECT
  to_regclass('public.hosted_provider_create_attempts') IS NOT NULL
  AND to_regclass('public.provider_orders') IS NOT NULL
  AND to_regclass('public.provider_prebind_inbox') IS NOT NULL
  AND to_regclass('public.provider_reconcile_observations') IS NOT NULL
  AND to_regclass('public.hosted_payment_link_jobs') IS NOT NULL
  AND to_regclass('public.hosted_payment_link_incidents') IS NOT NULL
  AND to_regprocedure('public.admit_hosted_provider_operation(uuid,uuid,text,text,timestamp with time zone)') IS NOT NULL
  AND to_regprocedure('public.hosted_provider_outbound_config_admitted(uuid,uuid,text,text)') IS NOT NULL
  AND to_regprocedure('public.claim_hosted_create_recoveries(timestamp with time zone,timestamp with time zone,integer)') IS NOT NULL
  AND to_regprocedure('public.claim_hosted_prebind_recoveries(timestamp with time zone,timestamp with time zone,integer)') IS NOT NULL
  AND to_regprocedure('public.claim_hosted_order_recoveries(timestamp with time zone,timestamp with time zone,integer)') IS NOT NULL
  AND has_function_privilege(current_user,'public.admit_hosted_provider_operation(uuid,uuid,text,text,timestamp with time zone)','EXECUTE')
  AND has_function_privilege(current_user,'public.hosted_provider_outbound_config_admitted(uuid,uuid,text,text)','EXECUTE')
  AND has_function_privilege(current_user,'public.claim_hosted_create_recoveries(timestamp with time zone,timestamp with time zone,integer)','EXECUTE')
  AND has_function_privilege(current_user,'public.claim_hosted_prebind_recoveries(timestamp with time zone,timestamp with time zone,integer)','EXECUTE')
  AND has_function_privilege(current_user,'public.claim_hosted_order_recoveries(timestamp with time zone,timestamp with time zone,integer)','EXECUTE')
  AND has_table_privilege(current_user,'public.hosted_payment_link_jobs','SELECT,UPDATE')
  AND has_table_privilege(current_user,'public.hosted_payment_link_incidents','SELECT,INSERT')`).Scan(&ready); err != nil {
		return err
	}
	if !ready {
		return errors.New("hosted provider migrations are not ready")
	}
	return nil
}

func scanHostedCreateRecovery(row rowScanner) (job hostedproviders.RecoveryJob, err error) {
	var tenantID, merchantID, state, fiatAmount, atomicAmount, numerator, denominator string
	var quoteIssuedAt, responseReceivedAt *time.Time
	var responseDigest []byte
	job.Kind = hostedproviders.RecoveryCreate
	err = row.Scan(&job.ID, &job.ClaimToken, &job.Attempt, &tenantID, &merchantID, &job.Config.ID, &job.CreateRequest.IntentID, &job.CreateRequest.IdempotencyKey, &job.CreateRequest.RequestHash, &job.CreateRequest.AssetID, &fiatAmount, &job.CreateRequest.Currency, &job.CreateRequest.CurrencyScale, &job.CreateRequest.ExpiresAt, &state, &job.CreateResult.ProviderOrderID, &job.CreateResult.ProviderReference, &job.CreateResult.PaymentURL, &atomicAmount, &job.CreateResult.AssetDecimals, &job.CreateResult.QuoteID, &numerator, &denominator, &quoteIssuedAt, &job.CreateResult.RawResponse, &responseDigest, &responseReceivedAt)
	if err != nil {
		return job, err
	}
	job.Config.TenantID, job.Config.MerchantID = tenantID, merchantID
	job.CreateRequest.ProviderID = job.Config.ID
	job.CreateRequest.FiatAmountMinor, err = money.Parse(fiatAmount)
	if err != nil {
		return job, err
	}
	if state != "completed" {
		return job, nil
	}
	job.CreateResult.AssetID = job.CreateRequest.AssetID
	job.CreateResult.ExpiresAt = job.CreateRequest.ExpiresAt
	job.CreateResult.Amount, err = money.Parse(atomicAmount)
	if err != nil {
		return job, err
	}
	job.CreateResult.RateNumerator, err = money.Parse(numerator)
	if err != nil {
		return job, err
	}
	job.CreateResult.RateDenominator, err = money.Parse(denominator)
	if err != nil {
		return job, err
	}
	if quoteIssuedAt != nil {
		job.CreateResult.QuoteIssuedAt = quoteIssuedAt.UTC()
	}
	if responseReceivedAt != nil {
		job.CreateResult.ResponseReceivedAt = responseReceivedAt.UTC()
	}
	copy(job.CreateResult.ResponseDigest[:], responseDigest)
	return job, nil
}

func (s *Store) CompleteHostedCreateRecovery(ctx context.Context, job hostedproviders.RecoveryJob, result domain.HostedCreateResult) (hostedproviders.RecoveryJob, error) {
	expected, err := job.CreateRequest.FiatAmountMinor.MulDivCeil(result.RateNumerator, result.RateDenominator)
	digest := sha256.Sum256(result.RawResponse)
	if err != nil || result.AssetID != job.CreateRequest.AssetID || result.Amount.Cmp(expected) != 0 || result.ProviderReference == "" || result.PaymentURL == "" || len(result.RawResponse) < 1 || len(result.RawResponse) > 256<<10 || !bytes.Equal(digest[:], result.ResponseDigest[:]) || result.ResponseReceivedAt.IsZero() {
		return job, domain.ErrIdempotencyConflict
	}
	orderID, err := ids.New()
	if err != nil {
		return job, err
	}
	principal := application.Principal{TenantID: job.Config.TenantID, MerchantID: job.Config.MerchantID}
	err = s.withinMerchant(ctx, principal, func(tx pgx.Tx) error {
		now := s.now()
		command, err := tx.Exec(ctx, `UPDATE hosted_provider_create_attempts SET state='completed',claim_token=NULL,claim_until=NULL,provider_order_id=$1,provider_reference=$2,payment_url=$3,amount_atomic=$4::numeric,asset_decimals=$5,quote_id=$6,rate_numerator=$7::numeric,rate_denominator=$8::numeric,quote_issued_at=$9,create_response_body=$10,create_response_digest=$11,create_response_received_at=$12,last_error_code=NULL,updated_at=$13,version=version+1 WHERE id=$14 AND tenant_id=$15 AND merchant_id=$16 AND recovery_status='claimed' AND recovery_claim_token=$17 AND recovery_claim_until>=clock_timestamp() AND provider_id=$18 AND idempotency_key=$19 AND state IN('retry','claimed')`, orderID, result.ProviderReference, result.PaymentURL, result.Amount.String(), result.AssetDecimals, result.QuoteID, result.RateNumerator.String(), result.RateDenominator.String(), result.QuoteIssuedAt.UTC(), result.RawResponse, result.ResponseDigest[:], result.ResponseReceivedAt.UTC(), now, job.ID, job.Config.TenantID, job.Config.MerchantID, job.ClaimToken, job.Config.ID, job.CreateRequest.IdempotencyKey)
		if err != nil {
			return classify(err)
		}
		if command.RowsAffected() != 1 {
			return domain.ErrVersionConflict
		}
		return nil
	})
	if err != nil {
		return job, err
	}
	result.ProviderOrderID = orderID
	job.CreateResult = result
	return job, nil
}

func (s *Store) BindHostedCreateRecovery(ctx context.Context, job hostedproviders.RecoveryJob) error {
	principal := application.Principal{TenantID: job.Config.TenantID, MerchantID: job.Config.MerchantID, Scopes: map[string]bool{"payments:write": true}}
	return s.db.WithinTenant(ctx, principal.TenantID, func(tx pgx.Tx) error {
		if err := setMerchantContext(ctx, tx, principal.MerchantID); err != nil {
			return err
		}
		var state string
		var intentExpiry time.Time
		if err := tx.QueryRow(ctx, `SELECT i.status::text,i.expires_at FROM hosted_provider_create_attempts a JOIN payment_intents i ON i.id=a.intent_id AND i.tenant_id=a.tenant_id AND i.merchant_id=a.merchant_id WHERE a.id=$1 AND a.tenant_id=$2 AND a.merchant_id=$3 AND a.recovery_status='claimed' AND a.recovery_claim_token=$4 AND a.recovery_claim_until>=clock_timestamp() AND a.state='completed' FOR UPDATE OF a,i`, job.ID, job.Config.TenantID, job.Config.MerchantID, job.ClaimToken).Scan(&state, &intentExpiry); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return domain.ErrVersionConflict
			}
			return err
		}
		if state != string(domain.IntentAwaitingRouteSelection) && state != string(domain.IntentPending) || !intentExpiry.After(s.now()) || !job.CreateResult.ExpiresAt.After(s.now()) {
			return hostedproviders.ErrRecoveryIntentIneligible
		}
		if err := lockIdempotency(ctx, tx, principal.MerchantID, "create_route", job.CreateRequest.IdempotencyKey); err != nil {
			return err
		}
		if record, found, err := findIdempotency(ctx, tx, principal.MerchantID, "create_route", job.CreateRequest.IdempotencyKey); err != nil {
			return err
		} else if found {
			if !hashMatches(record.RequestHash, job.CreateRequest.RequestHash) {
				return domain.ErrIdempotencyConflict
			}
			command, err := tx.Exec(ctx, `UPDATE hosted_provider_create_attempts SET recovery_status='complete',recovery_claim_token=NULL,recovery_claim_until=NULL,last_recovery_error_code=NULL,updated_at=$1,version=version+1 WHERE id=$2 AND tenant_id=$3 AND merchant_id=$4 AND recovery_claim_token=$5 AND recovery_claim_until>=clock_timestamp()`, s.now(), job.ID, job.Config.TenantID, job.Config.MerchantID, job.ClaimToken)
			if err != nil {
				return err
			}
			if command.RowsAffected() != 1 {
				return domain.ErrVersionConflict
			}
			return nil
		}
		cmd := application.CreateRoute{Principal: principal, IntentID: job.CreateRequest.IntentID, IdempotencyKey: job.CreateRequest.IdempotencyKey, Provider: domain.RouteProviderHostedGateway, ProviderID: job.Config.ID, ProviderOrderID: job.CreateResult.ProviderOrderID, ProviderReference: job.CreateResult.ProviderReference, PaymentURL: job.CreateResult.PaymentURL, AssetID: job.CreateResult.AssetID, ExpectedAmount: job.CreateResult.Amount, AssetDecimals: job.CreateResult.AssetDecimals, DisplayAmount: hostedDisplayAmount(job.CreateResult.Amount.String(), job.CreateResult.AssetDecimals), ExpiresAt: job.CreateResult.ExpiresAt, GraceEndsAt: job.CreateResult.ExpiresAt, RequestHash: job.CreateRequest.RequestHash, CorrelationID: "hosted-recovery-" + job.ID}
		route, err := s.CreateRouteInTx(ctx, tx, cmd)
		if err != nil {
			if errors.Is(err, domain.ErrStateConflict) {
				return hostedproviders.ErrRecoveryIntentIneligible
			}
			return err
		}
		body, _ := json.Marshal(route)
		return insertIdempotency(ctx, tx, principal, "create_route", job.CreateRequest.IdempotencyKey, job.CreateRequest.RequestHash, "payment_route", route.ID, httpCreated, body, s.now().Add(30*24*time.Hour))
	})
}

func hostedDisplayAmount(value string, decimals uint8) string {
	if decimals == 0 {
		return value
	}
	for len(value) <= int(decimals) {
		value = "0" + value
	}
	cut := len(value) - int(decimals)
	return value[:cut] + "." + value[cut:]
}

func (s *Store) MarkHostedCreateExpired(ctx context.Context, job hostedproviders.RecoveryJob) error {
	principal := application.Principal{TenantID: job.Config.TenantID, MerchantID: job.Config.MerchantID}
	return s.withinMerchant(ctx, principal, func(tx pgx.Tx) error {
		command, err := tx.Exec(ctx, `UPDATE hosted_provider_create_attempts SET recovery_status='incident',recovery_claim_token=NULL,recovery_claim_until=NULL,last_recovery_error_code='intent_expired',updated_at=$1,version=version+1 WHERE id=$2 AND tenant_id=$3 AND merchant_id=$4 AND recovery_status='claimed' AND recovery_claim_token=$5 AND recovery_claim_until>=clock_timestamp() AND state IN('retry','claimed') AND expires_at<=clock_timestamp()`, s.now(), job.ID, job.Config.TenantID, job.Config.MerchantID, job.ClaimToken)
		if err != nil {
			return err
		}
		if command.RowsAffected() != 1 {
			return domain.ErrVersionConflict
		}
		if err := s.markHostedPaymentLinkTerminal(ctx, tx, job, "expired_before_provider_create", "intent_expired"); err != nil {
			return err
		}
		return s.insertHostedRuntimeIncident(ctx, tx, job, "recovery_exhausted", nil)
	})
}

func (s *Store) MarkHostedCreateCancelled(ctx context.Context, job hostedproviders.RecoveryJob, _ string) error {
	principal := application.Principal{TenantID: job.Config.TenantID, MerchantID: job.Config.MerchantID}
	return s.withinMerchant(ctx, principal, func(tx pgx.Tx) error {
		command, err := tx.Exec(ctx, `UPDATE hosted_provider_create_attempts SET recovery_status='incident',recovery_claim_token=NULL,recovery_claim_until=NULL,last_recovery_error_code='intent_ineligible',updated_at=$1,version=version+1 WHERE id=$2 AND tenant_id=$3 AND merchant_id=$4 AND recovery_status='claimed' AND recovery_claim_token=$5 AND recovery_claim_until>=clock_timestamp()`, s.now(), job.ID, job.Config.TenantID, job.Config.MerchantID, job.ClaimToken)
		if err != nil || command.RowsAffected() != 1 {
			if err != nil {
				return err
			}
			return domain.ErrVersionConflict
		}
		if err := s.markHostedPaymentLinkTerminal(ctx, tx, job, "route_bind_exhausted", "intent_ineligible"); err != nil {
			return err
		}
		return s.insertHostedRuntimeIncident(ctx, tx, job, "orphan_cancelled", nil)
	})
}

func (s *Store) RecordHostedReconcileObservation(ctx context.Context, job hostedproviders.RecoveryJob, state hostedproviders.ProviderState) error {
	digest := sha256.Sum256(state.RawResponse)
	if state.ProviderReference != job.CreateResult.ProviderReference || state.AssetID != job.CreateResult.AssetID || state.Amount.Cmp(job.CreateResult.Amount) != 0 || state.AssetDecimals != job.CreateResult.AssetDecimals || len(state.RawResponse) < 1 || len(state.RawResponse) > 256<<10 || !bytes.Equal(digest[:], state.ResponseDigest[:]) || state.ResponseReceivedAt.IsZero() {
		return domain.ErrIdempotencyConflict
	}
	principal := application.Principal{TenantID: job.Config.TenantID, MerchantID: job.Config.MerchantID}
	return s.withinMerchant(ctx, principal, func(tx pgx.Tx) error {
		observationID, err := ids.New()
		if err != nil {
			return err
		}
		command, err := tx.Exec(ctx, `INSERT INTO provider_reconcile_observations(id,tenant_id,merchant_id,provider_id,provider_order_id,provider_reference,provider_status,asset_id,amount_atomic,asset_decimals,provider_occurred_at,response_body,response_digest,response_received_at,created_at)
SELECT $1,o.tenant_id,o.merchant_id,o.provider_id,o.id,o.provider_reference,$2,o.asset_id,$3::numeric,$4,$5,$6,$7,$8,$8 FROM provider_orders o WHERE o.id=$9 AND o.tenant_id=$10 AND o.merchant_id=$11 AND o.reconcile_claim_token=$12 AND o.reconcile_claim_until>=clock_timestamp() AND o.provider_id=$13 ON CONFLICT(provider_order_id,response_digest) DO NOTHING`, observationID, state.Status, state.Amount.String(), state.AssetDecimals, state.OccurredAt.UTC(), state.RawResponse, state.ResponseDigest[:], state.ResponseReceivedAt.UTC(), job.ID, job.Config.TenantID, job.Config.MerchantID, job.ClaimToken, job.Config.ID)
		if err != nil {
			return classify(err)
		}
		leaseUpdate, err := tx.Exec(ctx, `UPDATE provider_orders SET reconcile_claim_token=NULL,reconcile_claim_until=NULL,next_reconcile_at=$1,last_reconcile_error_code=NULL,updated_at=$2,version=version+1 WHERE id=$3 AND tenant_id=$4 AND merchant_id=$5 AND reconcile_claim_token=$6 AND reconcile_claim_until>=clock_timestamp()`, s.now().Add(time.Minute), s.now(), job.ID, job.Config.TenantID, job.Config.MerchantID, job.ClaimToken)
		if err != nil {
			return err
		}
		if leaseUpdate.RowsAffected() != 1 {
			return domain.ErrVersionConflict
		}
		if command.RowsAffected() == 0 {
			return nil
		}
		if state.Status != "paid" && state.Status != "refunded" {
			return nil
		}
		var callbackExists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM provider_inbox WHERE provider_order_id=$1 AND provider_status=$2)`, job.ID, state.Status).Scan(&callbackExists); err != nil || callbackExists {
			return err
		}
		kind := "provider_paid_without_callback"
		if state.Status == "refunded" {
			kind = "provider_refunded_without_callback"
		}
		return s.insertHostedRuntimeIncident(ctx, tx, job, kind, state.ResponseDigest[:])
	})
}

func (s *Store) ReplayHostedPrebind(ctx context.Context, job hostedproviders.RecoveryJob) error {
	_, err := s.ingestVerifiedProviderPayment(ctx, job.Payment, job.ID, job.ClaimToken)
	return err
}

func (s *Store) ExpireHostedPrebind(ctx context.Context, job hostedproviders.RecoveryJob) error {
	principal := application.Principal{TenantID: job.Payment.TenantID, MerchantID: job.Payment.MerchantID}
	return s.withinMerchant(ctx, principal, func(tx pgx.Tx) error {
		command, err := tx.Exec(ctx, `UPDATE provider_prebind_inbox SET state='expired',claim_token=NULL,claim_until=NULL,last_error_code='unknown_provider_reference',updated_at=$1,version=version+1 WHERE id=$2 AND tenant_id=$3 AND merchant_id=$4 AND state='pending' AND claim_token=$5 AND claim_until>=clock_timestamp() AND expires_at<=clock_timestamp()`, s.now(), job.ID, job.Config.TenantID, job.Config.MerchantID, job.ClaimToken)
		if err != nil {
			return err
		}
		if command.RowsAffected() != 1 {
			return domain.ErrVersionConflict
		}
		return s.insertHostedRuntimeIncident(ctx, tx, job, "recovery_exhausted", job.Payment.RawDigest[:])
	})
}

func (s *Store) RetryHostedRecovery(ctx context.Context, job hostedproviders.RecoveryJob, next time.Time, code string, dead bool) error {
	if len(code) > 64 {
		code = "recovery_failed"
	}
	principal := application.Principal{TenantID: job.Config.TenantID, MerchantID: job.Config.MerchantID}
	return s.withinMerchant(ctx, principal, func(tx pgx.Tx) error {
		status := "pending"
		if dead {
			status = "incident"
		}
		var command pgconnCommandTag
		var err error
		if job.Kind == hostedproviders.RecoveryCreate {
			command, err = tx.Exec(ctx, `UPDATE hosted_provider_create_attempts SET recovery_status=$1,recovery_claim_token=NULL,recovery_claim_until=NULL,next_recovery_at=$2,last_recovery_error_code=$3,updated_at=$4,version=version+1 WHERE id=$5 AND tenant_id=$6 AND merchant_id=$7 AND recovery_status='claimed' AND recovery_claim_token=$8 AND recovery_claim_until>=clock_timestamp()`, status, next.UTC(), code, s.now(), job.ID, job.Config.TenantID, job.Config.MerchantID, job.ClaimToken)
		} else if job.Kind == hostedproviders.RecoveryPrebind {
			prebindState := "pending"
			if dead {
				prebindState = "expired"
			}
			command, err = tx.Exec(ctx, `UPDATE provider_prebind_inbox SET state=$1,claim_token=NULL,claim_until=NULL,next_attempt_at=$2,last_error_code=$3,updated_at=$4,version=version+1 WHERE id=$5 AND tenant_id=$6 AND merchant_id=$7 AND state='pending' AND claim_token=$8 AND claim_until>=clock_timestamp()`, prebindState, next.UTC(), code, s.now(), job.ID, job.Config.TenantID, job.Config.MerchantID, job.ClaimToken)
		} else {
			command, err = tx.Exec(ctx, `UPDATE provider_orders SET reconcile_claim_token=NULL,reconcile_claim_until=NULL,next_reconcile_at=$1,last_reconcile_error_code=$2,updated_at=$3,version=version+1 WHERE id=$4 AND tenant_id=$5 AND merchant_id=$6 AND reconcile_claim_token=$7 AND reconcile_claim_until>=clock_timestamp()`, next.UTC(), code, s.now(), job.ID, job.Config.TenantID, job.Config.MerchantID, job.ClaimToken)
		}
		if err != nil {
			return err
		}
		if command.RowsAffected() != 1 {
			return domain.ErrVersionConflict
		}
		if dead {
			if job.Kind == hostedproviders.RecoveryCreate {
				if err := s.markHostedPaymentLinkTerminal(ctx, tx, job, "provider_create_exhausted", code); err != nil {
					return err
				}
			}
			return s.insertHostedRuntimeIncident(ctx, tx, job, "recovery_exhausted", nil)
		}
		return nil
	})
}

func (s *Store) markHostedPaymentLinkTerminal(ctx context.Context, tx pgx.Tx, job hostedproviders.RecoveryJob, kind, code string) error {
	command, err := tx.Exec(ctx, `UPDATE hosted_payment_link_jobs SET state='terminal',last_error_code=$1,updated_at=$2,version=version+1 WHERE create_attempt_id=$3 AND tenant_id=$4 AND merchant_id=$5 AND state='preparing'`, code, s.now(), job.ID, job.Config.TenantID, job.Config.MerchantID)
	if err != nil || command.RowsAffected() == 0 {
		return err
	}
	incidentID, err := ids.New()
	if err != nil {
		return err
	}
	var jobID string
	if err = tx.QueryRow(ctx, `INSERT INTO hosted_payment_link_incidents(id,tenant_id,merchant_id,job_id,incident_kind,status,created_at,version) SELECT $1,tenant_id,merchant_id,id,$2,'open',$3,1 FROM hosted_payment_link_jobs WHERE create_attempt_id=$4 AND tenant_id=$5 AND merchant_id=$6 ON CONFLICT(job_id,incident_kind) DO UPDATE SET incident_kind=EXCLUDED.incident_kind RETURNING job_id::text`, incidentID, kind, s.now(), job.ID, job.Config.TenantID, job.Config.MerchantID).Scan(&jobID); err != nil {
		return err
	}
	outboxID, err := ids.New()
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]string{"job_id": jobID, "payment_intent_id": job.CreateRequest.IntentID, "provider_id": job.Config.ID, "incident_kind": kind, "status": "open"})
	_, err = tx.Exec(ctx, `INSERT INTO outbox_events(id,tenant_id,merchant_id,aggregate_type,aggregate_id,aggregate_version,aggregate_sequence,event_type,schema_version,payload,occurred_at,recorded_at,available_at) VALUES($1,$2,$3,'hosted_payment_link_job',$4,1,1,'payment_link.hosted_route_failed','1',$5::jsonb,$6,$6,$6)`, outboxID, job.Config.TenantID, job.Config.MerchantID, jobID, payload, s.now())
	return err
}

type pgconnCommandTag interface{ RowsAffected() int64 }

func (s *Store) insertHostedRuntimeIncident(ctx context.Context, tx pgx.Tx, job hostedproviders.RecoveryJob, kind string, evidence []byte) error {
	incidentID, err := ids.New()
	if err != nil {
		return err
	}
	var command pgconnCommandTag
	reference := job.CreateResult.ProviderReference
	if job.Kind == hostedproviders.RecoveryCreate {
		command, err = tx.Exec(ctx, `INSERT INTO hosted_provider_runtime_incidents(id,tenant_id,merchant_id,provider_id,create_attempt_id,provider_order_id,provider_reference,incident_kind,evidence_digest,status,created_at,version) VALUES($1,$2,$3,$4,$5,NULL,$6,$7,$8,'open',$9,1) ON CONFLICT(create_attempt_id,incident_kind) DO NOTHING`, incidentID, job.Config.TenantID, job.Config.MerchantID, job.Config.ID, job.ID, job.CreateResult.ProviderReference, kind, evidence, s.now())
	} else if job.Kind == hostedproviders.RecoveryPrebind {
		reference = job.Payment.ProviderReference
		command, err = tx.Exec(ctx, `INSERT INTO hosted_provider_runtime_incidents(id,tenant_id,merchant_id,provider_id,create_attempt_id,provider_order_id,provider_prebind_id,provider_reference,incident_kind,evidence_digest,status,created_at,version) VALUES($1,$2,$3,$4,NULL,NULL,$5,$6,$7,$8,'open',$9,1) ON CONFLICT(provider_prebind_id,incident_kind) DO NOTHING`, incidentID, job.Config.TenantID, job.Config.MerchantID, job.Config.ID, job.ID, job.Payment.ProviderReference, kind, evidence, s.now())
	} else {
		command, err = tx.Exec(ctx, `INSERT INTO hosted_provider_runtime_incidents(id,tenant_id,merchant_id,provider_id,create_attempt_id,provider_order_id,provider_reference,incident_kind,evidence_digest,status,created_at,version) VALUES($1,$2,$3,$4,NULL,$5,$6,$7,$8,'open',$9,1) ON CONFLICT(provider_order_id,incident_kind,evidence_digest) DO NOTHING`, incidentID, job.Config.TenantID, job.Config.MerchantID, job.Config.ID, job.ID, job.CreateResult.ProviderReference, kind, evidence, s.now())
	}
	if err != nil || command.RowsAffected() == 0 {
		return err
	}
	outboxID, err := ids.New()
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]string{"incident_id": incidentID, "incident_kind": kind, "provider_id": job.Config.ID, "provider_reference": reference})
	_, err = tx.Exec(ctx, `INSERT INTO outbox_events(id,tenant_id,merchant_id,aggregate_type,aggregate_id,aggregate_version,aggregate_sequence,event_type,schema_version,payload,occurred_at,recorded_at,available_at) VALUES($1,$2,$3,'hosted_provider_runtime_incident',$4,1,1,'provider.runtime_reconciliation_required','1',$5::jsonb,$6,$6,$6)`, outboxID, job.Config.TenantID, job.Config.MerchantID, incidentID, payload, s.now())
	return err
}
