package providerconfig

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/ids"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresRepository struct{ pool *pgxpool.Pool }

func NewPostgresRepository(pool *pgxpool.Pool) (*PostgresRepository, error) {
	if pool == nil {
		return nil, ErrDependency
	}
	return &PostgresRepository{pool: pool}, nil
}

func (r *PostgresRepository) PingControl(ctx context.Context) error {
	if err := r.ping(ctx,
		"provider_config_public_rows(uuid,uuid,integer,uuid)",
		"request_hosted_provider_config(uuid,uuid,uuid,text,bigint,jsonb,text,uuid,text,timestamptz,timestamptz)",
		"decide_hosted_provider_config(uuid,uuid,bigint,boolean,text,uuid,text,timestamptz,timestamptz)",
	); err != nil {
		return err
	}
	var ready bool
	if err := r.pool.QueryRow(ctx, `SELECT to_regclass('public.hosted_provider_config_idempotency') IS NOT NULL AND has_table_privilege(current_user,'public.hosted_provider_config_idempotency','SELECT,INSERT')`).Scan(&ready); err != nil {
		return fmt.Errorf("provider configuration idempotency readiness: %w", err)
	}
	if !ready {
		return ErrDependency
	}
	return nil
}

func (r *PostgresRepository) PingWorker(ctx context.Context) error {
	return r.ping(ctx,
		"claim_hosted_provider_config_probes(text,integer,timestamptz)",
		"complete_hosted_provider_config_probe(uuid,text,bigint,boolean,text,bytea,bytea,timestamptz)",
	)
}

func (r *PostgresRepository) ping(ctx context.Context, functions ...string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	checks := `to_regclass('public.hosted_provider_config_manifests') IS NOT NULL AND to_regclass('public.hosted_provider_config_workflows') IS NOT NULL`
	args := make([]any, len(functions))
	for i, function := range functions {
		checks += fmt.Sprintf(" AND to_regprocedure($%d) IS NOT NULL AND has_function_privilege(current_user,$%d,'EXECUTE')", i+1, i+1)
		args[i] = function
	}
	var ready bool
	if err := r.pool.QueryRow(ctx, `SELECT `+checks, args...).Scan(&ready); err != nil || !ready {
		if err != nil {
			return fmt.Errorf("provider configuration PostgreSQL readiness: %w", err)
		}
		return ErrDependency
	}
	return nil
}

func (r *PostgresRepository) within(ctx context.Context, scope Scope, actor, session string, fn func(pgx.Tx) error) error {
	return pgx.BeginTxFunc(ctx, r.pool, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT set_config('app.platform_admin_global','false',true),set_config('app.platform_admin_tenants',$1,true),set_config('app.provider_config_actor_id',$2,true),set_config('app.provider_config_session_id',$3,true)`, scope.TenantID, actor, session); err != nil {
			return err
		}
		return fn(tx)
	})
}

type rowScanner interface{ Scan(...any) error }

func scanVersion(row rowScanner) (Version, error) {
	var value Version
	err := row.Scan(
		&value.ID, &value.ProviderID, &value.TenantID, &value.MerchantID, &value.ManifestVersion, &value.ChangeKind,
		&value.ExpectedHeadVersion, &value.Status, &value.AdapterKind, &value.AssetID, &value.AssetDecimals, &value.Currency,
		&value.APIKeyID, &value.CallbackKeyID, &value.CallbackOverlapSeconds, &value.PayloadHash, &value.Reason,
		&value.RequestedBy, &value.ApprovedBy, &value.RejectedBy, &value.DecisionReason, &value.CreatedAt, &value.ExpiresAt,
		&value.DecidedAt, &value.ActivatedAt, &value.CallbackAcceptUntil, &value.ProbeResponseDigest, &value.ProbeTLSSPKIDigest,
		&value.ProbeObservedAt, &value.HeadVersion, &value.RowVersion,
	)
	return value, classify(err)
}

func (r *PostgresRepository) List(ctx context.Context, scope Scope, cursor string, limit int) (Page, error) {
	var out Page
	err := r.within(ctx, scope, "", "", func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT * FROM provider_config_public_rows($1,NULLIF($2,'')::uuid,$3,NULL)`, scope.TenantID, cursor, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			value, scanErr := scanVersion(rows)
			if scanErr != nil {
				return scanErr
			}
			out.Items = append(out.Items, value)
		}
		if err = rows.Err(); err != nil {
			return err
		}
		if len(out.Items) > limit {
			out.NextCursor = out.Items[limit-1].ID
			out.Items = out.Items[:limit]
		}
		return nil
	})
	return out, classify(err)
}

func (r *PostgresRepository) Get(ctx context.Context, scope Scope, id string) (Version, error) {
	var out Version
	err := r.within(ctx, scope, "", "", func(tx pgx.Tx) error {
		var err error
		out, err = scanVersion(tx.QueryRow(ctx, `SELECT * FROM provider_config_public_rows($1,NULL,1,$2)`, scope.TenantID, id))
		return err
	})
	return out, classify(err)
}

func privatePayload(input ManifestInput) ([]byte, error) {
	return json.Marshal(map[string]any{
		"adapter_kind": input.AdapterKind, "api_credential_ref": input.APICredentialRef, "api_key_id": input.APIKeyID,
		"api_origin": input.APIOrigin, "asset_decimals": input.AssetDecimals, "asset_id": input.AssetID,
		"callback_key_id": input.CallbackKeyID, "callback_overlap_seconds": input.CallbackOverlapSeconds,
		"callback_secret_ref": input.CallbackSecretRef, "cancel_path": input.CancelPath, "change_kind": input.ChangeKind,
		"create_path": input.CreatePath, "currency": input.Currency, "payment_url_origins": input.PaymentURLOrigins,
		"probe_reference": input.ProbeReference, "reconcile_path": input.ReconcilePath, "refund_path": input.RefundPath,
		"signature_scheme": input.SignatureScheme, "status_path": input.StatusPath,
	})
}

func (r *PostgresRepository) mutate(ctx context.Context, principal Principal, scope Scope, operation string, idem Idempotency, out *Version, fn func(pgx.Tx) error) error {
	return r.within(ctx, scope, principal.ActorID, principal.SessionID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,20))`, scope.TenantID+"\x1f"+principal.ActorID+"\x1f"+operation+"\x1f"+idem.Key); err != nil {
			return err
		}
		var storedHash, body []byte
		err := tx.QueryRow(ctx, `SELECT request_hash,response_body FROM hosted_provider_config_idempotency WHERE scope_id=platform_scope_uuid($1::uuid) AND actor_id=$2 AND operation=$3 AND idempotency_key=$4 AND expires_at>clock_timestamp() FOR UPDATE`, scope.TenantID, principal.ActorID, operation, idem.Key).Scan(&storedHash, &body)
		if err == nil {
			if !bytes.Equal(storedHash, idem.Fingerprint[:]) {
				return ErrIdempotencyConflict
			}
			if json.Unmarshal(body, out) != nil {
				return ErrDependency
			}
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err = fn(tx); err != nil {
			return err
		}
		response, err := json.Marshal(out)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO hosted_provider_config_idempotency(scope_id,tenant_id,actor_id,operation,idempotency_key,request_hash,response_body,created_at,expires_at) VALUES(platform_scope_uuid($1::uuid),$1,$2,$3,$4,$5,$6::jsonb,clock_timestamp(),clock_timestamp()+interval '24 hours')`, scope.TenantID, principal.ActorID, operation, idem.Key, idem.Fingerprint[:], response)
		return err
	})
}

func (r *PostgresRepository) Request(ctx context.Context, principal Principal, input RequestInput, idem Idempotency) (Version, error) {
	var out Version
	payload, err := privatePayload(input.Manifest)
	if err != nil {
		return out, err
	}
	err = r.mutate(ctx, principal, Scope{TenantID: input.TenantID}, "provider_config.request", idem, &out, func(tx pgx.Tx) error {
		id, createErr := ids.New()
		if createErr != nil {
			return createErr
		}
		out, createErr = scanVersion(tx.QueryRow(ctx, `SELECT * FROM request_hosted_provider_config($1,$2,$3,$4,$5,$6::jsonb,$7,$8,$9,$10,$11)`, id, input.TenantID, input.MerchantID, input.ProviderID, input.ExpectedHeadVersion, payload, input.Reason, principal.ActorID, principal.SessionID, principal.StepUpAt, time.Now().UTC()))
		return createErr
	})
	return out, classify(err)
}

func (r *PostgresRepository) Decide(ctx context.Context, principal Principal, scope Scope, id string, approve bool, input DecideInput, idem Idempotency) (Version, error) {
	var out Version
	operation := "provider_config.reject"
	if approve {
		operation = "provider_config.approve"
	}
	err := r.mutate(ctx, principal, scope, operation, idem, &out, func(tx pgx.Tx) error {
		var decideErr error
		out, decideErr = scanVersion(tx.QueryRow(ctx, `SELECT * FROM decide_hosted_provider_config($1,$2,$3,$4,$5,$6,$7,$8,$9)`, id, scope.TenantID, input.ExpectedRowVersion, approve, input.Reason, principal.ActorID, principal.SessionID, principal.StepUpAt, time.Now().UTC()))
		return decideErr
	})
	return out, classify(err)
}

func (r *PostgresRepository) ClaimProbes(ctx context.Context, owner string, limit int) ([]ProbeTarget, error) {
	rows, err := r.pool.Query(ctx, `SELECT * FROM claim_hosted_provider_config_probes($1,$2,clock_timestamp())`, owner, limit)
	if err != nil {
		return nil, classify(err)
	}
	defer rows.Close()
	result := []ProbeTarget{}
	for rows.Next() {
		var target ProbeTarget
		if err = rows.Scan(&target.ManifestID, &target.TenantID, &target.MerchantID, &target.ProviderID, &target.ManifestVersion, &target.LeaseToken,
			&target.APIOrigin, &target.StatusPath, &target.APICredentialRef, &target.APIKeyID, &target.ProbeReference, &target.AdapterKind,
			&target.AssetID, &target.AssetDecimals); err != nil {
			return nil, err
		}
		result = append(result, target)
	}
	return result, rows.Err()
}

func (r *PostgresRepository) CompleteProbe(ctx context.Context, result ProbeResult) error {
	err := pgx.BeginTxFunc(ctx, r.pool, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx pgx.Tx) error {
		_, execErr := tx.Exec(ctx, `SELECT complete_hosted_provider_config_probe($1,$2,$3,$4,$5,$6,$7,$8)`, result.ManifestID, result.Owner, result.LeaseToken, result.Success, result.ErrorCategory, result.ResponseDigest[:], result.TLSSPKIDigest[:], result.ObservedAt)
		return execErr
	})
	return classify(err)
}

func classify(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "42501":
			return ErrForbidden
		case "22023", "23514", "22P02":
			return ErrInvalid
		case "40001", "23505":
			return ErrConflict
		}
	}
	return err
}
