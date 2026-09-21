package postgres

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/application"
	"github.com/calmshv-star/ocrypt/backend/internal/ids"
	"github.com/jackc/pgx/v5"
)

// These are native ETH networks on which a payment to the same controlled EVM
// address represents the same asset, down to the wei. Other EVM assets and
// wrapped ETH are deliberately excluded.
var nativeETHNetworks = []struct{ chain, asset string }{
	{"eip155:1", "eth-ethereum"},
	{"eip155:10", "eth-optimism"},
	{"eip155:42161", "eth-arbitrum"},
	{"eip155:8453", "eth-base"},
}

var nativeETHAssetIDs = []string{"eth-ethereum", "eth-optimism", "eth-arbitrum", "eth-base"}

func equivalentNativeETH(assetID string) bool {
	for _, network := range nativeETHNetworks {
		if network.asset == assetID {
			return true
		}
	}
	return false
}

// A payer may mistakenly select another ETH network in an exchange. Hidden
// sibling routes preserve the actual chain/asset on the transfer and ledger;
// the exact-match settlement pipeline and webhook remain unchanged. A sibling
// is made only when this merchant controls the very same deposit address on
// an admitted, active network. The create-route response still returns only
// the payer-selected route.
func createNativeETHFallbackRoutes(ctx context.Context, tx pgx.Tx, cmd application.CreateRoute, now time.Time) error {
	if cmd.QuoteID == "" || !equivalentNativeETH(cmd.AssetID) || cmd.AssetDecimals != 18 {
		return nil
	}
	address := strings.ToLower(cmd.Address)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "native-eth:"+cmd.Principal.TenantID+":"+address); err != nil {
		return err
	}
	for _, network := range nativeETHNetworks {
		if network.chain == cmd.ChainID {
			continue
		}
		var finality uint64
		err := tx.QueryRow(ctx, `SELECT admission.required_finality
FROM addresses a
JOIN wallets w ON w.id=a.wallet_id AND w.tenant_id=a.tenant_id AND w.chain_id=a.chain_id
JOIN assets asset ON asset.id=$4 AND asset.chain_id=a.chain_id
JOIN chains c ON c.id=a.chain_id
JOIN LATERAL platform_wallet_runtime_admission(a.tenant_id,w.id,a.chain_id) wallet_admission ON true
JOIN LATERAL platform_route_runtime_admission(a.tenant_id,$2::uuid,a.chain_id,asset.id) admission ON true
WHERE a.tenant_id=$1::uuid AND a.chain_id=$3 AND lower(a.canonical_address)=$5
  AND a.purpose='deposit' AND a.status IN ('available','assigned')
  AND w.status='active' AND w.custody_mode='watch_only'
  AND (w.merchant_id=$2::uuid OR w.merchant_id IS NULL)
  AND c.status='active' AND asset.status='active' AND asset.kind='native' AND asset.decimals=18
LIMIT 1`, cmd.Principal.TenantID, cmd.Principal.MerchantID, network.chain, network.asset, address).Scan(&finality)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		var occupied bool
		err = tx.QueryRow(ctx, `SELECT
 EXISTS(SELECT 1 FROM address_assignments aa JOIN addresses a ON a.id=aa.address_id AND a.tenant_id=aa.tenant_id
   JOIN rate_quotes q ON q.id=aa.quote_id AND q.tenant_id=aa.tenant_id
   WHERE aa.tenant_id=$1::uuid AND aa.intent_id<>$2::uuid AND aa.status='leased'
     AND aa.valid_until>$6 AND lower(a.canonical_address)=$3
     AND q.asset_id=ANY($4::text[]) AND q.crypto_amount_atomic=$5::numeric)
 OR EXISTS(SELECT 1 FROM amount_reservations ar JOIN payment_routes r ON r.id=ar.route_id AND r.tenant_id=ar.tenant_id
   WHERE ar.tenant_id=$1::uuid AND r.intent_id<>$2::uuid AND ar.state='active'
     AND lower(ar.receiving_address)=$3 AND ar.asset_id=ANY($4::text[])
     AND ar.exact_amount_atomic=$5::numeric AND ar.active_window&&tstzrange($6,$7,'[)'))`,
			cmd.Principal.TenantID, cmd.IntentID, address, nativeETHAssetIDs, cmd.ExpectedAmount.String(), now, cmd.GraceEndsAt.UTC()).Scan(&occupied)
		if err != nil {
			return err
		}
		if occupied {
			continue // Never make one transfer payable to two different orders.
		}
		routeID, err := ids.New()
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO payment_routes
(id,tenant_id,merchant_id,intent_id,chain_id,asset_id,provider,expected_amount_atomic,asset_decimals,display_amount,receiving_address,required_finality,status,starts_at,expires_at,grace_ends_at,version,created_at,updated_at)
VALUES ($1,$2::uuid,$3::uuid,$4::uuid,$5,$6,'on_chain',$7::numeric,18,$8,$9,$10,'active',$11,$12,$13,1,$11,$11)`,
			routeID, cmd.Principal.TenantID, cmd.Principal.MerchantID, cmd.IntentID, network.chain, network.asset,
			cmd.ExpectedAmount.String(), cmd.DisplayAmount, address, finality, now, cmd.ExpiresAt.UTC(), cmd.GraceEndsAt.UTC())
		if err != nil {
			return err
		}
		reservationID, err := ids.New()
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO amount_reservations
(id,tenant_id,route_id,chain_id,receiving_address,asset_id,exact_amount_atomic,active_window,state,created_at,updated_at)
VALUES ($1,$2::uuid,$3::uuid,$4,$5,$6,$7::numeric,tstzrange($8,$9,'[)'),'active',$8,$8)`,
			reservationID, cmd.Principal.TenantID, routeID, network.chain, address, network.asset,
			cmd.ExpectedAmount.String(), now, cmd.GraceEndsAt.UTC())
		if err != nil {
			return err
		}
	}
	return nil
}
