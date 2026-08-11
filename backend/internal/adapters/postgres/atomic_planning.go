package postgres

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/application"
	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/ids"
	"github.com/calmshv-star/ocrypt/backend/internal/money"
	"github.com/jackc/pgx/v5"
)

// AllocateRouteInTx persists an immutable fresh quote and address lease without
// committing. An empty planning key is reserved for an enclosing atomic
// payment-link redemption transaction.
func (s *Store) AllocateRouteInTx(ctx context.Context, tx pgx.Tx, p application.Principal, intent domain.PaymentIntent, chainID, assetID, planningKey string, requestDigest []byte, expiresAt time.Time) (planned application.CreateRoute, err error) {
	if tx == nil || p.TenantID == "" || p.MerchantID == "" || intent.ID == "" || chainID == "" || assetID == "" || !expiresAt.After(s.now()) || (planningKey != "" && len(requestDigest) != sha256.Size) {
		return planned, fmt.Errorf("%w: invalid atomic route allocation", domain.ErrValidation)
	}
	var environmentSnapshotID, chainSnapshotID, assetSnapshotID, finalitySnapshotID string
	var environmentFence, chainFence, assetFence, finalityFence int64
	var admittedDecimals uint8
	var finality uint64
	err = tx.QueryRow(ctx, `SELECT environment_snapshot_id::text,environment_fence,chain_snapshot_id::text,chain_fence,asset_snapshot_id::text,asset_fence,finality_snapshot_id::text,finality_fence,asset_decimals,required_finality FROM platform_route_runtime_admission($1::uuid,$2::uuid,$3,$4)`, p.TenantID, p.MerchantID, chainID, assetID).Scan(&environmentSnapshotID, &environmentFence, &chainSnapshotID, &chainFence, &assetSnapshotID, &assetFence, &finalitySnapshotID, &finalityFence, &admittedDecimals, &finality)
	if err == pgx.ErrNoRows {
		return planned, fmt.Errorf("%w: activated platform configuration does not admit this route", domain.ErrStateConflict)
	}
	if err != nil {
		return planned, err
	}
	var decimals uint8
	var tickID, numeratorRaw, denominatorRaw, source string
	var spread uint32
	var policy int64
	var provenance []byte
	err = tx.QueryRow(ctx, `SELECT a.decimals,t.id::text,t.numerator::text,t.denominator::text,t.spread_bps,t.source,t.policy_version,t.provenance_hash FROM assets a JOIN chains c ON c.id=a.chain_id JOIN LATERAL(SELECT * FROM asset_rate_ticks rt WHERE rt.asset_id=a.id AND rt.fiat_currency=$1 AND rt.status='active' AND rt.observed_at+make_interval(secs=>rt.max_age_seconds)>clock_timestamp() ORDER BY rt.observed_at DESC,rt.id DESC LIMIT 1)t ON true WHERE a.id=$2 AND a.chain_id=$3 AND a.status='active' AND c.status='active'`, intent.Currency, assetID, chainID).Scan(&decimals, &tickID, &numeratorRaw, &denominatorRaw, &spread, &source, &policy, &provenance)
	if err == pgx.ErrNoRows {
		return planned, fmt.Errorf("%w: no fresh persisted rate tick for route", domain.ErrStateConflict)
	}
	if err != nil {
		return planned, err
	}
	if decimals != admittedDecimals {
		return planned, fmt.Errorf("%w: operational asset decimals differ from activated snapshot", domain.ErrInvariantViolation)
	}
	numerator, err := money.Parse(numeratorRaw)
	if err != nil {
		return planned, err
	}
	denominator, err := money.Parse(denominatorRaw)
	if err != nil {
		return planned, err
	}
	expected, err := money.FiatMinorToAssetAtomic(intent.AmountMinor, intent.CurrencyScale, decimals, numerator, denominator, spread)
	if err != nil || expected.IsZero() {
		return planned, fmt.Errorf("%w: persisted rate produced invalid amount", domain.ErrInvariantViolation)
	}
	var addressID, address, walletSnapshotID string
	var walletFence int64
	err = tx.QueryRow(ctx, `SELECT a.id::text,a.canonical_address,admission.wallet_pool_snapshot_id::text,admission.wallet_pool_fence FROM addresses a JOIN wallets w ON w.id=a.wallet_id AND w.tenant_id=a.tenant_id JOIN LATERAL platform_wallet_runtime_admission(a.tenant_id,w.id,a.chain_id) admission ON true WHERE a.tenant_id=$1 AND a.chain_id=$2 AND(a.status='available' OR(w.custody_mode='watch_only' AND a.status='assigned'))AND w.status='active' AND(w.merchant_id IS NULL OR w.merchant_id=$3)ORDER BY(w.merchant_id=$3)DESC,a.derivation_index NULLS LAST,a.id LIMIT 1 FOR UPDATE OF a SKIP LOCKED`, p.TenantID, chainID, p.MerchantID).Scan(&addressID, &address, &walletSnapshotID, &walletFence)
	if err == pgx.ErrNoRows {
		return planned, fmt.Errorf("%w: address pool exhausted", domain.ErrStateConflict)
	}
	if err != nil {
		return planned, err
	}
	expected, err = selectUnreservedAmount(ctx, tx, p.TenantID, addressID, chainID, assetID, address, expected, s.now(), expiresAt.UTC().Add(24*time.Hour))
	if err != nil {
		return planned, err
	}
	quoteID, err := ids.New()
	if err != nil {
		return planned, err
	}
	assignmentID, err := ids.New()
	if err != nil {
		return planned, err
	}
	token := make([]byte, 32)
	if _, err = rand.Read(token); err != nil {
		return planned, err
	}
	tokenText := base64.RawURLEncoding.EncodeToString(token)
	tokenHash := sha256.Sum256([]byte(tokenText))
	now := s.now()
	var digest any
	if len(requestDigest) > 0 {
		digest = requestDigest
	}
	_, err = tx.Exec(ctx, `INSERT INTO rate_quotes(id,tenant_id,merchant_id,payment_intent_id,planning_idempotency_key,planning_request_hash,fiat_amount_minor,fiat_currency,fiat_scale,asset_id,crypto_amount_atomic,reference_price,spread_bps,source_tick_ids,policy_version,issued_at,expires_at,raw_provenance_hash,platform_environment_snapshot_id,platform_environment_fence,platform_chain_snapshot_id,platform_chain_fence,platform_asset_snapshot_id,platform_asset_fence,platform_finality_snapshot_id,platform_finality_fence,runtime_required_finality)VALUES($1,$2,$3,$4,NULLIF($5,''),$6,$7::numeric,$8,$9,$10,$11::numeric,$12,$13,ARRAY[$14::uuid],$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27)`, quoteID, p.TenantID, p.MerchantID, intent.ID, planningKey, digest, intent.AmountMinor.String(), intent.Currency, intent.CurrencyScale, assetID, expected.String(), numeratorRaw+"/"+denominatorRaw, spread, tickID, policy, now, expiresAt.UTC(), provenance, environmentSnapshotID, environmentFence, chainSnapshotID, chainFence, assetSnapshotID, assetFence, finalitySnapshotID, finalityFence, finality)
	if err != nil {
		return planned, classify(err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO address_assignments(id,tenant_id,intent_id,address_id,chain_id,lease_token_hash,status,valid_from,valid_until,quote_id,created_at,updated_at,version,platform_wallet_pool_snapshot_id,platform_wallet_pool_fence)VALUES($1,$2,$3,$4,$5,$6,'leased',$7,$8,$9,$7,$7,1,$10,$11)`, assignmentID, p.TenantID, intent.ID, addressID, chainID, tokenHash[:], now, expiresAt.UTC(), quoteID, walletSnapshotID, walletFence)
	if err != nil {
		return planned, classify(err)
	}
	command, err := tx.Exec(ctx, `UPDATE addresses SET status='assigned',first_used_at=COALESCE(first_used_at,$1),last_used_at=$1,updated_at=$1,version=version+1 WHERE id=$2 AND tenant_id=$3 AND status IN('available','assigned')`, now, addressID, p.TenantID)
	if err != nil {
		return planned, err
	}
	if command.RowsAffected() != 1 {
		return planned, domain.ErrVersionConflict
	}
	planned = application.CreateRoute{Principal: p, IntentID: intent.ID, QuoteID: quoteID, AddressAssignmentID: assignmentID, ChainID: chainID, AssetID: assetID, ExpectedAmount: expected, AssetDecimals: decimals, DisplayAmount: displayAtomic(expected.String(), decimals), Address: address, RequiredFinality: finality, ExpiresAt: expiresAt.UTC(), GraceEndsAt: expiresAt.UTC().Add(24 * time.Hour)}
	_ = source
	return planned, nil
}

// selectUnreservedAmount serializes on the already locked address row and
// chooses the smallest atomic-unit suffix that does not collide with either a
// leased quote or an active exact-amount reservation in the requested window.
// The suffix is bounded to 10,000 atomic units (0.01 for a 6-decimal asset).
func selectUnreservedAmount(ctx context.Context, tx pgx.Tx, tenantID, addressID, chainID, assetID, address string, base money.Amount, startsAt, graceEndsAt time.Time) (money.Amount, error) {
	rows, err := tx.Query(ctx, `SELECT q.crypto_amount_atomic::text
FROM address_assignments aa JOIN rate_quotes q ON q.id=aa.quote_id AND q.tenant_id=aa.tenant_id
WHERE aa.tenant_id=$1 AND aa.address_id=$2 AND aa.status='leased' AND aa.valid_until>$3 AND q.asset_id=$4
UNION
SELECT ar.exact_amount_atomic::text FROM amount_reservations ar
WHERE ar.tenant_id=$1 AND ar.chain_id=$5 AND ar.receiving_address=$6 AND ar.asset_id=$4 AND ar.state='active'
  AND ar.active_window&&tstzrange($3,$7,'[)')`, tenantID, addressID, startsAt, assetID, chainID, address, graceEndsAt)
	if err != nil {
		return money.Amount{}, err
	}
	defer rows.Close()
	occupied := make(map[string]struct{})
	for rows.Next() {
		var value string
		if err = rows.Scan(&value); err != nil {
			return money.Amount{}, err
		}
		occupied[value] = struct{}{}
	}
	if err = rows.Err(); err != nil {
		return money.Amount{}, err
	}
	for offset := 0; offset < 10000; offset++ {
		candidate, addErr := base.Add(money.MustParse(fmt.Sprintf("%d", offset)))
		if addErr != nil {
			return money.Amount{}, addErr
		}
		if _, exists := occupied[candidate.String()]; !exists {
			return candidate, nil
		}
	}
	return money.Amount{}, fmt.Errorf("%w: exact-amount reservation space exhausted", domain.ErrStateConflict)
}
