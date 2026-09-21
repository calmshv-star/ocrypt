package postgres

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/application"
	"github.com/calmshv-star/ocrypt/backend/internal/money"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestNativeETHFallbacksAreLimitedToEquivalentNativeAssets(t *testing.T) {
	want := map[string]string{
		"eip155:1":     "eth-ethereum",
		"eip155:10":    "eth-optimism",
		"eip155:42161": "eth-arbitrum",
		"eip155:8453":  "eth-base",
	}
	if len(nativeETHNetworks) != len(want) || len(nativeETHAssetIDs) != len(want) {
		t.Fatal("native ETH network and asset lists must cover the same four networks")
	}
	for _, network := range nativeETHNetworks {
		if want[network.chain] != network.asset || !equivalentNativeETH(network.asset) {
			t.Fatalf("invalid native ETH equivalence: %s %s", network.chain, network.asset)
		}
		delete(want, network.chain)
	}
	if len(want) != 0 {
		t.Fatal("native ETH network missing")
	}
	for _, asset := range []string{"weth-arbitrum", "usdt-ethereum", "bnb-bsc", "matic-polygon", "eth-linea"} {
		if equivalentNativeETH(asset) {
			t.Fatalf("non-equivalent asset %s accepted", asset)
		}
	}
}

type nativeETHFallbackRow struct{ value any }

func (r nativeETHFallbackRow) Scan(dest ...any) error {
	switch pointer := dest[0].(type) {
	case *uint64:
		*pointer = r.value.(uint64)
	case *bool:
		*pointer = r.value.(bool)
	}
	return nil
}

type nativeETHFallbackTx struct {
	pgx.Tx
	queries  []string
	chains   []string
	occupied bool
}

func (tx *nativeETHFallbackTx) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	tx.queries = append(tx.queries, sql)
	if strings.Contains(sql, "INSERT INTO payment_routes") {
		tx.chains = append(tx.chains, args[4].(string))
	}
	return pgconn.NewCommandTag("INSERT 0 1"), nil
}

func (tx *nativeETHFallbackTx) QueryRow(_ context.Context, sql string, _ ...any) pgx.Row {
	if strings.Contains(sql, "platform_route_runtime_admission") {
		return nativeETHFallbackRow{value: uint64(1)}
	}
	return nativeETHFallbackRow{value: tx.occupied}
}

func TestNativeETHFallbackRoutesRequireQuotedSameAddressAndNoCollision(t *testing.T) {
	amount, err := money.Parse("2174000000000000")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 21, 16, 30, 0, 0, time.UTC)
	cmd := application.CreateRoute{
		Principal: application.Principal{TenantID: "11111111-1111-4111-8111-111111111111", MerchantID: "22222222-2222-4222-8222-222222222222"},
		IntentID:  "33333333-3333-4333-8333-333333333333", QuoteID: "44444444-4444-4444-8444-444444444444",
		ChainID: "eip155:1", AssetID: "eth-ethereum", ExpectedAmount: amount, AssetDecimals: 18,
		DisplayAmount: "0.002174", Address: "0x2222222222222222222222222222222222222222",
		ExpiresAt: now.Add(30 * time.Minute), GraceEndsAt: now.Add(24 * time.Hour),
	}
	tx := &nativeETHFallbackTx{}
	if err := createNativeETHFallbackRoutes(context.Background(), tx, cmd, now); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(tx.chains, ","); got != "eip155:10,eip155:42161,eip155:8453" {
		t.Fatalf("expected exact peer networks, got %s", got)
	}
	tx = &nativeETHFallbackTx{occupied: true}
	if err := createNativeETHFallbackRoutes(context.Background(), tx, cmd, now); err != nil || len(tx.chains) != 0 {
		t.Fatal("occupied amount must never create a competing exact route", err)
	}
	cmd.QuoteID = ""
	tx = &nativeETHFallbackTx{}
	if err := createNativeETHFallbackRoutes(context.Background(), tx, cmd, now); err != nil || len(tx.queries) != 0 {
		t.Fatal("unquoted route must not create fallback routes", err)
	}
}
