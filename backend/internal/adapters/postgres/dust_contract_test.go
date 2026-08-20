package postgres

import (
	"os"
	"strings"
	"testing"
)

func TestUnmatchedDustRemainsAuditableButStaysOutOfOperatorQueues(t *testing.T) {
	settlement, err := os.ReadFile("settlement.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(settlement)
	for _, fragment := range []string{
		"if len(candidates) == 0",
		"belowUnmatchedDustThreshold(ctx, tx, event)",
		"SELECT a.dust_threshold::text",
		"payment_route_policy_bindings",
		"underpayment_tolerance_bps",
		"!plausiblePayment",
		"'dust_below_asset_threshold','ignored'",
		"SettlementIgnored",
	} {
		if !strings.Contains(source, fragment) {
			t.Fatalf("unmatched dust contract is missing %q", fragment)
		}
	}
	exact := strings.Index(source, "findSettlementCandidates(ctx, tx, event)")
	dust := strings.Index(source, "belowUnmatchedDustThreshold(ctx, tx, event)")
	if exact < 0 || dust < 0 || exact >= dust {
		t.Fatal("dust filtering must happen only after exact-route settlement")
	}

	admin, err := os.ReadFile("../../admin/postgres.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(admin), "u.status NOT IN ('resolved','ignored','invalid','reorged')") {
		t.Fatal("terminal dust records can leak into the operator unmatched queue")
	}
}

func TestDustExpansionCoversEveryConfiguredAssetAndPreservesPlausiblePayments(t *testing.T) {
	raw, err := os.ReadFile("../../../migrations/000043_expand_unmatched_asset_dust.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(raw)
	assets := []string{
		"eth-ethereum", "usdc-ethereum", "usdt-ethereum",
		"sol-solana", "usdc-solana", "usdt-solana",
		"ton-ton", "usdt-ton", "trx-tron", "usdt-tron",
		"eth-base", "usdc-base", "eth-arbitrum", "usdc-arbitrum",
		"eth-optimism", "usdc-optimism", "avax-avalanche", "usdc-avalanche",
		"pol-polygon", "usdc-polygon", "bnb-bsc",
	}
	for _, asset := range assets {
		if !strings.Contains(sql, "'"+asset+"'") {
			t.Errorf("dust expansion is missing %s", asset)
		}
	}
	for _, fragment := range []string{
		"e.amount_atomic<=a.dust_threshold",
		"payment_matches pm",
		"payment_route_policy_bindings b",
		"underpayment_tolerance_bps",
		"accept_late_within_grace",
		"e.amount_atomic*10000 >= r.expected_amount_atomic",
		"status='ignored'",
		"assigned_operator_id=NULL",
	} {
		if !strings.Contains(sql, fragment) {
			t.Errorf("dust expansion is missing %q", fragment)
		}
	}
}

func TestStablecoinDustFloorHidesSubTenthTransfersButPreservesPlausiblePayments(t *testing.T) {
	raw, err := os.ReadFile("../../../migrations/000050_raise_stablecoin_unmatched_dust_floor.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(raw)
	for _, fragment := range []string{
		"('usdc-solana','solana:mainnet',100000::numeric)",
		"('usdt-tron','tron:mainnet',100000::numeric)",
		"('usdt-bsc','eip155:56',100000000000000000::numeric)",
		"e.amount_atomic<=a.dust_threshold",
		"payment_matches pm",
		"payment_route_policy_bindings b",
		"underpayment_tolerance_bps",
		"accept_late_within_grace",
		"e.amount_atomic*10000 >= r.expected_amount_atomic",
		"status='ignored'",
	} {
		if !strings.Contains(sql, fragment) {
			t.Errorf("stablecoin dust floor is missing %q", fragment)
		}
	}
	if !strings.Contains(sql, "u.status NOT IN ('resolved','ignored','invalid','reorged')") {
		t.Fatal("stablecoin dust backfill can rewrite terminal records")
	}
}

func TestTONDustMigrationIsAssetScopedAndPreservesExactMatches(t *testing.T) {
	raw, err := os.ReadFile("../../../migrations/000038_unmatched_asset_dust.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(raw)
	for _, fragment := range []string{
		"id='ton-ton'",
		"chain_id='ton:mainnet'",
		"dust_threshold=1000000",
		"e.amount_atomic<=a.dust_threshold",
		"version=u.version+1",
		"NOT EXISTS (",
		"payment_matches pm",
		"pm.state<>'reversed'",
		"FROM payment_routes r",
		"r.expected_amount_atomic=e.amount_atomic",
		"e.on_chain_time BETWEEN r.starts_at AND r.expires_at",
		"GRANT SELECT ON assets TO merchant_settlement_worker",
	} {
		if !strings.Contains(sql, fragment) {
			t.Fatalf("TON dust migration is missing %q", fragment)
		}
	}
}
