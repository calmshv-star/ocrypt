package legacycompat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/ids"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresRepository struct{ pool *pgxpool.Pool }

func NewPostgresRepository(pool *pgxpool.Pool) (*PostgresRepository, error) {
	if pool == nil {
		return nil, errors.New("legacy repository pool is nil")
	}
	return &PostgresRepository{pool: pool}, nil
}

func (repository *PostgresRepository) LookupCredential(ctx context.Context, protocol Protocol, pid string, checkedAt time.Time) (Credential, error) {
	row := repository.pool.QueryRow(ctx, `SELECT config_id::text,credential_version_id::text,credential_version,protocol,pid,tenant_id::text,merchant_id::text,legacy_secret_ref,callback_key_id,core_key_id,core_secret_ref,currency,currency_scale,chain_id,asset_id,legacy_token,legacy_network,legacy_payment_type,array_to_json(ip_allowlist)::text,approved,enabled,sunset_at FROM legacy_lookup_credential($1,$2,$3)`, protocol, pid, checkedAt)
	return scanCredential(row)
}

func (repository *PostgresRepository) LookupCredentialVersion(ctx context.Context, id string) (Credential, error) {
	row := repository.pool.QueryRow(ctx, `SELECT config_id::text,credential_version_id::text,credential_version,protocol,pid,tenant_id::text,merchant_id::text,legacy_secret_ref,callback_key_id,core_key_id,core_secret_ref,currency,currency_scale,chain_id,asset_id,legacy_token,legacy_network,legacy_payment_type,array_to_json(ip_allowlist)::text,approved,enabled,sunset_at FROM legacy_lookup_credential_version($1::uuid)`, id)
	return scanCredential(row)
}

type scanner interface{ Scan(...any) error }

func scanCredential(row scanner) (Credential, error) {
	var result Credential
	var protocol string
	var scale int16
	var networksJSON string
	err := row.Scan(&result.ConfigID, &result.CredentialVersionID, &result.CredentialVersion, &protocol, &result.PID, &result.TenantID, &result.MerchantID, &result.LegacySecretRef, &result.CallbackKeyID, &result.CoreKeyID, &result.CoreSecretRef, &result.Currency, &scale, &result.ChainID, &result.AssetID, &result.LegacyToken, &result.LegacyNetwork, &result.LegacyPaymentType, &networksJSON, &result.Approved, &result.Enabled, &result.SunsetAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Credential{}, ErrNotFound
	}
	if err != nil {
		return Credential{}, err
	}
	result.Protocol = Protocol(protocol)
	result.CurrencyScale = uint8(scale)
	var cidrs []string
	if err = json.Unmarshal([]byte(networksJSON), &cidrs); err != nil {
		return Credential{}, err
	}
	for _, cidr := range cidrs {
		_, network, err := net.ParseCIDR(cidr)
		if err != nil {
			return Credential{}, err
		}
		result.IPAllowlist = append(result.IPAllowlist, network)
	}
	return result, nil
}

func (repository *PostgresRepository) RecordMapping(ctx context.Context, mapping Mapping) (Mapping, bool, error) {
	row := repository.pool.QueryRow(ctx, `SELECT trade_id,config_id::text,credential_version_id::text,protocol,legacy_order_id,request_hash,intent_id::text,route_id::text,notify_url,return_url,order_name,form_md5_type,fiat_amount,currency,legacy_token,legacy_network,created_at FROM legacy_record_mapping($1,$2::uuid,$3::uuid,$4,$5,$6,$7::uuid,$8::uuid,$9,$10,$11,$12,$13,$14,$15,$16,$17)`, mapping.TradeID, mapping.ConfigID, mapping.CredentialVersionID, mapping.Protocol, mapping.OrderID, mapping.RequestHash[:], mapping.IntentID, mapping.RouteID, mapping.NotifyURL, mapping.ReturnURL, mapping.Name, mapping.PaymentType, mapping.Amount, mapping.Currency, mapping.Token, mapping.Network, mapping.CreatedAt)
	stored, err := scanMapping(row)
	if err != nil {
		return Mapping{}, false, err
	}
	return stored, stored.TradeID != mapping.TradeID, nil
}

func (repository *PostgresRepository) LookupMapping(ctx context.Context, tradeID string) (Mapping, error) {
	return scanMapping(repository.pool.QueryRow(ctx, `SELECT trade_id,config_id::text,credential_version_id::text,protocol,legacy_order_id,request_hash,intent_id::text,route_id::text,notify_url,return_url,order_name,form_md5_type,fiat_amount,currency,legacy_token,legacy_network,created_at FROM legacy_lookup_mapping($1)`, tradeID))
}
func (repository *PostgresRepository) LookupMappingByIntent(ctx context.Context, configID, intentID string) (Mapping, error) {
	return scanMapping(repository.pool.QueryRow(ctx, `SELECT trade_id,config_id::text,credential_version_id::text,protocol,legacy_order_id,request_hash,intent_id::text,route_id::text,notify_url,return_url,order_name,form_md5_type,fiat_amount,currency,legacy_token,legacy_network,created_at FROM legacy_lookup_mapping_by_intent($1::uuid,$2::uuid)`, configID, intentID))
}

func scanMapping(row scanner) (Mapping, error) {
	var result Mapping
	var protocol string
	var hash []byte
	err := row.Scan(&result.TradeID, &result.ConfigID, &result.CredentialVersionID, &protocol, &result.OrderID, &hash, &result.IntentID, &result.RouteID, &result.NotifyURL, &result.ReturnURL, &result.Name, &result.PaymentType, &result.Amount, &result.Currency, &result.Token, &result.Network, &result.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Mapping{}, ErrNotFound
	}
	if err != nil {
		return Mapping{}, err
	}
	if len(hash) != 32 {
		return Mapping{}, errors.New("invalid mapping hash")
	}
	copy(result.RequestHash[:], hash)
	result.Protocol = Protocol(protocol)
	return result, nil
}

func (repository *PostgresRepository) ListEventSources(ctx context.Context, checkedAt time.Time) ([]EventSource, error) {
	rows, err := repository.pool.Query(ctx, `SELECT config_id::text,protocol,pid,core_key_id,core_secret_ref,after_sequence FROM legacy_list_event_sources($1)`, checkedAt)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []EventSource
	for rows.Next() {
		var source EventSource
		var protocol string
		if err = rows.Scan(&source.ConfigID, &protocol, &source.PID, &source.CoreKeyID, &source.CoreSecretRef, &source.AfterSequence); err != nil {
			return nil, err
		}
		source.Protocol = Protocol(protocol)
		result = append(result, source)
	}
	return result, rows.Err()
}

func (repository *PostgresRepository) ClassifyEvent(ctx context.Context, configID string, sequence int64, eventID, classification string, at time.Time) error {
	var ok bool
	if err := repository.pool.QueryRow(ctx, `SELECT legacy_classify_event($1::uuid,$2,$3::uuid,$4,$5)`, configID, sequence, eventID, classification, at).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return ErrConflict
	}
	return nil
}

func (repository *PostgresRepository) EnqueueCallbackAndAdvance(ctx context.Context, source EventSource, event CoreEvent, mapping Mapping, frozen FrozenCallback, at time.Time) error {
	id, err := ids.New()
	if err != nil {
		return err
	}
	var ok bool
	err = repository.pool.QueryRow(ctx, `SELECT legacy_enqueue_callback($1::uuid,$2::uuid,$3,$4::uuid,$5,$6::uuid,$7,$8,$9,$10,$11,$12)`, id, source.ConfigID, event.Sequence, event.EventID, mapping.TradeID, frozen.CredentialVersionID, frozen.CallbackKeyID, frozen.TargetURL, frozen.HTTPMethod, frozen.ContentType, frozen.Body, at).Scan(&ok)
	if err != nil {
		return err
	}
	if !ok {
		return ErrConflict
	}
	return nil
}

func (repository *PostgresRepository) ClaimCallbacks(ctx context.Context, workerID string, limit int, lease time.Duration, at time.Time) ([]CallbackJob, error) {
	if limit < 1 || limit > 100 || lease < 5*time.Second || lease > 5*time.Minute {
		return nil, ErrInvalid
	}
	rows, err := repository.pool.Query(ctx, `SELECT delivery_id::text,lease_token::text,fence,protocol,event_id::text,target_url,http_method,content_type,frozen_body,credential_version_id::text,callback_key_id,attempt_count FROM legacy_claim_callbacks($1,$2,$3,$4)`, workerID, limit, int(lease/time.Second), at)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []CallbackJob
	for rows.Next() {
		var job CallbackJob
		var protocol string
		if err = rows.Scan(&job.DeliveryID, &job.LeaseToken, &job.Fence, &protocol, &job.EventID, &job.TargetURL, &job.HTTPMethod, &job.ContentType, &job.FrozenBody, &job.CredentialVersionID, &job.CallbackKeyID, &job.AttemptCount); err != nil {
			return nil, err
		}
		job.Protocol = Protocol(protocol)
		result = append(result, job)
	}
	return result, rows.Err()
}

func (repository *PostgresRepository) AcknowledgeCallback(ctx context.Context, id, token string, fence int64, status int, digest [32]byte, at time.Time) (bool, error) {
	var ok bool
	err := repository.pool.QueryRow(ctx, `SELECT legacy_ack_callback($1::uuid,$2::uuid,$3,$4,$5,$6)`, id, token, fence, status, digest[:], at).Scan(&ok)
	return ok, err
}
func (repository *PostgresRepository) FailCallback(ctx context.Context, id, token string, fence int64, code string, status int, next time.Time) (bool, error) {
	var ok bool
	err := repository.pool.QueryRow(ctx, `SELECT legacy_fail_callback($1::uuid,$2::uuid,$3,$4,$5,$6)`, id, token, fence, code, status, next).Scan(&ok)
	return ok, err
}
func (repository *PostgresRepository) Ready(ctx context.Context, at time.Time) error {
	var ok bool
	if err := repository.pool.QueryRow(ctx, `SELECT legacy_compat_ready($1)`, at).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: database admission", ErrUnavailable)
	}
	return nil
}

func (repository *PostgresRepository) Ping(ctx context.Context) error {
	return repository.pool.Ping(ctx)
}

var _ Repository = (*PostgresRepository)(nil)
