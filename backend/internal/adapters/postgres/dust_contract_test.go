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
		"SELECT dust_threshold::text FROM assets",
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
