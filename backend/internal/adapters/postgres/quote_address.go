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
	"github.com/jackc/pgx/v5"
)

func (s *Store) SaveQuote(ctx context.Context, q domain.RateQuote) error {
	return s.db.WithinTenant(ctx, q.TenantID, func(tx pgx.Tx) error {
		provenance := sha256.Sum256([]byte(q.Source + "\x1f" + q.RateNumerator.String() + "\x1f" + q.RateDenominator.String()))
		_, err := tx.Exec(ctx, `INSERT INTO rate_quotes (id,tenant_id,merchant_id,payment_intent_id,fiat_amount_minor,fiat_currency,fiat_scale,asset_id,crypto_amount_atomic,reference_price,spread_bps,policy_version,issued_at,expires_at,raw_provenance_hash) VALUES ($1,$2,$3,$4,$5::numeric,$6,$7,$8,$9::numeric,$10,$11,$12,$13,$14,$15)`, q.ID, q.TenantID, q.MerchantID, q.PaymentIntentID, q.FiatAmountMinor.String(), q.FiatCurrency, q.FiatScale, q.AssetID, q.CryptoAmountAtomic.String(), q.RateNumerator.String()+"/"+q.RateDenominator.String(), q.SpreadBPS, q.PolicyVersion, q.IssuedAt, q.ExpiresAt, provenance[:])
		return classify(err)
	})
}

func (s *Store) LeaseAddress(ctx context.Context, p application.Principal, intentID, chainID string, validUntil time.Time) (assignment domain.AddressAssignment, err error) {
	err = s.db.WithinTenant(ctx, p.TenantID, func(tx pgx.Tx) error {
		var addressID, canonical, display string
		err := tx.QueryRow(ctx, `SELECT a.id::text,a.canonical_address,a.display_address FROM addresses a JOIN wallets w ON w.id=a.wallet_id WHERE a.tenant_id=$1 AND a.chain_id=$2 AND a.status='available' AND w.status='active' AND (w.merchant_id IS NULL OR w.merchant_id=$3) ORDER BY (w.merchant_id=$3) DESC,a.derivation_index NULLS LAST,a.id LIMIT 1 FOR UPDATE OF a SKIP LOCKED`, p.TenantID, chainID, p.MerchantID).Scan(&addressID, &canonical, &display)
		if err == pgx.ErrNoRows {
			return fmt.Errorf("%w: address pool is exhausted", domain.ErrStateConflict)
		}
		if err != nil {
			return err
		}
		assignmentID, err := ids.New()
		if err != nil {
			return err
		}
		tokenBytes := make([]byte, 32)
		if _, err = rand.Read(tokenBytes); err != nil {
			return err
		}
		token := base64.RawURLEncoding.EncodeToString(tokenBytes)
		tokenHash := sha256.Sum256([]byte(token))
		now := s.now()
		_, err = tx.Exec(ctx, `INSERT INTO address_assignments (id,tenant_id,intent_id,address_id,chain_id,lease_token_hash,status,valid_from,valid_until,created_at,updated_at,version) VALUES ($1,$2,$3,$4,$5,$6,'leased',$7,$8,$7,$7,1)`, assignmentID, p.TenantID, intentID, addressID, chainID, tokenHash[:], now, validUntil.UTC())
		if err != nil {
			return classify(err)
		}
		command, err := tx.Exec(ctx, `UPDATE addresses SET status='assigned',updated_at=$1,version=version+1 WHERE id=$2 AND tenant_id=$3 AND status='available'`, now, addressID, p.TenantID)
		if err != nil {
			return err
		}
		if command.RowsAffected() != 1 {
			return domain.ErrVersionConflict
		}
		assignment = domain.AddressAssignment{ID: assignmentID, AddressID: addressID, ChainID: chainID, CanonicalAddress: canonical, DisplayAddress: display, AssignedUntil: validUntil.UTC(), LeaseToken: token}
		return nil
	})
	return assignment, err
}

func (s *Store) ReleaseAddress(ctx context.Context, p application.Principal, assignmentID, leaseToken string) error {
	return s.db.WithinTenant(ctx, p.TenantID, func(tx pgx.Tx) error {
		tokenHash := sha256.Sum256([]byte(leaseToken))
		var addressID string
		err := tx.QueryRow(ctx, `UPDATE address_assignments SET status='retired',updated_at=clock_timestamp(),version=version+1 WHERE id=$1 AND tenant_id=$2 AND lease_token_hash=$3 AND status='leased' RETURNING address_id::text`, assignmentID, p.TenantID, tokenHash[:]).Scan(&addressID)
		if err == pgx.ErrNoRows {
			return domain.ErrStateConflict
		}
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE addresses SET status='retired',updated_at=clock_timestamp(),version=version+1 WHERE id=$1 AND tenant_id=$2`, addressID, p.TenantID)
		return err
	})
}

var _ application.QuoteStore = (*Store)(nil)
var _ application.AddressPool = (*Store)(nil)
