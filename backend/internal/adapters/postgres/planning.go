package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/application"
	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/money"
	"github.com/jackc/pgx/v5"
)

// AllocateRoute is the production planning boundary. A fresh persisted rate
// tick, immutable quote, and tenant address lease are committed together. It
// fails closed when the price is stale, the asset is disabled, or the pool is
// exhausted; there is no 1:1 or static-address fallback.
func (s *Store) AllocateRoute(ctx context.Context, p application.Principal, intent domain.PaymentIntent, chainID, assetID, idempotencyKey, requestHash string, expiresAt time.Time) (planned application.CreateRoute, err error) {
	requestDigest, decodeErr := hex.DecodeString(requestHash)
	if p.TenantID == "" || p.MerchantID == "" || intent.ID == "" || chainID == "" || assetID == "" || len(idempotencyKey) < 8 || decodeErr != nil || len(requestDigest) != sha256.Size || !expiresAt.After(s.now()) {
		return planned, fmt.Errorf("%w: invalid persisted route allocation request", domain.ErrValidation)
	}
	err = s.db.WithinTenant(ctx, p.TenantID, func(tx pgx.Tx) error {
		if err := lockIdempotency(ctx, tx, p.MerchantID, "create_route_plan", idempotencyKey); err != nil {
			return err
		}
		var existingHash []byte
		var quoteID, assignmentID, amountRaw, storedAddress, assignmentStatus string
		var storedDecimals uint8
		var storedFinality uint64
		var existingExpiry time.Time
		err := tx.QueryRow(ctx, `SELECT q.planning_request_hash,q.id::text,aa.id::text,q.crypto_amount_atomic::text,a.canonical_address,aa.status,ast.decimals,q.runtime_required_finality,q.expires_at FROM rate_quotes q JOIN address_assignments aa ON aa.quote_id=q.id AND aa.tenant_id=q.tenant_id JOIN addresses a ON a.id=aa.address_id AND a.tenant_id=aa.tenant_id JOIN assets ast ON ast.id=q.asset_id WHERE q.tenant_id=$1 AND q.merchant_id=$2 AND q.planning_idempotency_key=$3 AND q.platform_environment_snapshot_id IS NOT NULL AND aa.platform_wallet_pool_snapshot_id IS NOT NULL`, p.TenantID, p.MerchantID, idempotencyKey).Scan(&existingHash, &quoteID, &assignmentID, &amountRaw, &storedAddress, &assignmentStatus, &storedDecimals, &storedFinality, &existingExpiry)
		if err == nil {
			if !bytes.Equal(existingHash, requestDigest) {
				return domain.ErrIdempotencyConflict
			}
			if existingExpiry.Before(s.now()) || (assignmentStatus != "leased" && assignmentStatus != "bound") {
				return fmt.Errorf("%w: persisted route plan is no longer usable", domain.ErrStateConflict)
			}
			expected, err := money.Parse(amountRaw)
			if err != nil {
				return err
			}
			planned = application.CreateRoute{Principal: p, IntentID: intent.ID, QuoteID: quoteID, AddressAssignmentID: assignmentID, ChainID: chainID, AssetID: assetID, ExpectedAmount: expected, AssetDecimals: storedDecimals, DisplayAmount: displayAtomic(expected.String(), storedDecimals), Address: storedAddress, RequiredFinality: storedFinality, ExpiresAt: existingExpiry.UTC(), GraceEndsAt: existingExpiry.UTC().Add(24 * time.Hour)}
			return nil
		}
		if err != pgx.ErrNoRows {
			return err
		}
		planned, err = s.AllocateRouteInTx(ctx, tx, p, intent, chainID, assetID, idempotencyKey, requestDigest, expiresAt)
		return err
	})
	return planned, err
}

// ReleaseRoutePlan is the compensating half of production route creation. It
// shares both plan and route idempotency locks, only releases an unbound lease
// with the exact request fingerprint, and preserves the quote as unclaimed
// audit history. A committed/bound route is never released.
func (s *Store) ReleaseRoutePlan(ctx context.Context, p application.Principal, intentID, idempotencyKey, requestHash string) error {
	requestDigest, err := hex.DecodeString(requestHash)
	if p.TenantID == "" || p.MerchantID == "" || intentID == "" || len(idempotencyKey) < 8 || err != nil || len(requestDigest) != sha256.Size {
		return fmt.Errorf("%w: invalid route plan release", domain.ErrValidation)
	}
	return s.db.WithinTenant(ctx, p.TenantID, func(tx pgx.Tx) error {
		if err := lockIdempotency(ctx, tx, p.MerchantID, "create_route_plan", idempotencyKey); err != nil {
			return err
		}
		if err := lockIdempotency(ctx, tx, p.MerchantID, "create_route", idempotencyKey); err != nil {
			return err
		}
		var quoteID, assignmentID, addressID, status string
		var storedHash []byte
		err := tx.QueryRow(ctx, `SELECT q.id::text,aa.id::text,aa.address_id::text,aa.status,q.planning_request_hash
FROM rate_quotes q JOIN address_assignments aa ON aa.quote_id=q.id AND aa.tenant_id=q.tenant_id
WHERE q.tenant_id=$1 AND q.merchant_id=$2 AND q.payment_intent_id=$3 AND q.planning_idempotency_key=$4
FOR UPDATE OF q,aa`, p.TenantID, p.MerchantID, intentID, idempotencyKey).Scan(&quoteID, &assignmentID, &addressID, &status, &storedHash)
		if err == pgx.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
		if !bytes.Equal(storedHash, requestDigest) {
			return domain.ErrIdempotencyConflict
		}
		if status == "bound" {
			return nil
		}
		if status != "leased" {
			return nil
		}
		var routeExists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM payment_routes WHERE tenant_id=$1 AND (address_assignment_id=$2 OR quote_id=$3))`, p.TenantID, assignmentID, quoteID).Scan(&routeExists); err != nil {
			return err
		}
		if routeExists {
			return fmt.Errorf("%w: route plan has a committed consumer", domain.ErrInvariantViolation)
		}
		now := s.now()
		command, err := tx.Exec(ctx, `UPDATE address_assignments SET status='released',updated_at=$1,version=version+1 WHERE id=$2 AND tenant_id=$3 AND status='leased'`, now, assignmentID, p.TenantID)
		if err != nil {
			return err
		}
		if command.RowsAffected() != 1 {
			return domain.ErrVersionConflict
		}
		_, err = tx.Exec(ctx, `UPDATE addresses SET status='available',updated_at=$1,version=version+1 WHERE id=$2 AND tenant_id=$3 AND status='assigned' AND NOT EXISTS (SELECT 1 FROM address_assignments active WHERE active.address_id=addresses.id AND active.status IN ('leased','bound'))`, now, addressID, p.TenantID)
		if err != nil {
			return err
		}
		command, err = tx.Exec(ctx, `UPDATE rate_quotes SET planning_idempotency_key=NULL,planning_request_hash=NULL WHERE id=$1 AND tenant_id=$2 AND planning_idempotency_key=$3`, quoteID, p.TenantID, idempotencyKey)
		if err != nil {
			return err
		}
		if command.RowsAffected() != 1 {
			return domain.ErrVersionConflict
		}
		return nil
	})
}

// ReleaseExpiredRoutePlans recovers capacity after a process crash between
// persisted planning and route creation. Only expired, still-unbound plans are
// touched; committed routes and bound assignments are excluded and locked.
func (s *Store) ReleaseExpiredRoutePlans(ctx context.Context, now time.Time, limit int) (released int, err error) {
	if now.IsZero() || limit < 1 || limit > 500 {
		return 0, fmt.Errorf("%w: invalid expired plan sweep", domain.ErrValidation)
	}
	err = pgx.BeginTxFunc(ctx, s.db.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT aa.id::text,aa.tenant_id::text,aa.address_id::text,q.id::text
FROM address_assignments aa JOIN rate_quotes q ON q.id=aa.quote_id AND q.tenant_id=aa.tenant_id
WHERE aa.status='leased' AND aa.valid_until<$1 AND q.planning_idempotency_key IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM payment_routes route WHERE route.tenant_id=aa.tenant_id AND (route.address_assignment_id=aa.id OR route.quote_id=q.id))
ORDER BY aa.valid_until,aa.id LIMIT $2 FOR UPDATE OF aa,q SKIP LOCKED`, now, limit)
		if err != nil {
			return err
		}
		type expired struct{ assignmentID, tenantID, addressID, quoteID string }
		var items []expired
		for rows.Next() {
			var item expired
			if err := rows.Scan(&item.assignmentID, &item.tenantID, &item.addressID, &item.quoteID); err != nil {
				rows.Close()
				return err
			}
			items = append(items, item)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		for _, item := range items {
			command, err := tx.Exec(ctx, `UPDATE address_assignments SET status='released',updated_at=$1,version=version+1 WHERE id=$2 AND tenant_id=$3 AND status='leased' AND valid_until<$1`, now, item.assignmentID, item.tenantID)
			if err != nil {
				return err
			}
			if command.RowsAffected() != 1 {
				return domain.ErrVersionConflict
			}
			_, err = tx.Exec(ctx, `UPDATE addresses SET status='available',updated_at=$1,version=version+1 WHERE id=$2 AND tenant_id=$3 AND status='assigned' AND NOT EXISTS (SELECT 1 FROM address_assignments active WHERE active.address_id=addresses.id AND active.status IN ('leased','bound'))`, now, item.addressID, item.tenantID)
			if err != nil {
				return err
			}
			command, err = tx.Exec(ctx, `UPDATE rate_quotes SET planning_idempotency_key=NULL,planning_request_hash=NULL WHERE id=$1 AND tenant_id=$2 AND planning_idempotency_key IS NOT NULL`, item.quoteID, item.tenantID)
			if err != nil {
				return err
			}
			if command.RowsAffected() != 1 {
				return domain.ErrVersionConflict
			}
			released++
		}
		return nil
	})
	return released, err
}

func displayAtomic(value string, decimals uint8) string {
	if decimals == 0 {
		return value
	}
	for len(value) <= int(decimals) {
		value = "0" + value
	}
	cut := len(value) - int(decimals)
	return value[:cut] + "." + value[cut:]
}
