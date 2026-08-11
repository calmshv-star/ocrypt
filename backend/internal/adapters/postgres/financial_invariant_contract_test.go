package postgres

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readInvariantContractFile(t *testing.T, path ...string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(path...))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func requireInvariantClauses(t *testing.T, sourceName, source string, clauses ...string) {
	t.Helper()
	for _, clause := range clauses {
		if !strings.Contains(source, clause) {
			t.Errorf("%s lost financial invariant clause %q", sourceName, clause)
		}
	}
}

func TestFinancialInvariantSchemaContractsRemainFailClosed(t *testing.T) {
	platform := readInvariantContractFile(t, "..", "..", "..", "migrations", "000001_platform.up.sql")
	automation := readInvariantContractFile(t, "..", "..", "..", "migrations", "000006_automated_matching.up.sql")
	requireInvariantClauses(t, "000001_platform.up.sql", platform,
		"UNIQUE (chain_id, transaction_id, event_identity, asset_id, to_address)",
		"UNIQUE (event_id, provider_endpoint_id, observed_block_hash)",
		"UNIQUE (chain_id, identity_key)",
		"CREATE UNIQUE INDEX payment_matches_event_active_idx ON payment_matches (event_id) WHERE state <> 'reversed'",
		"UNIQUE (tenant_id, business_type, business_reference)",
		"reversal_of uuid REFERENCES ledger_transactions(id)",
		"PRIMARY KEY (transaction_id, sequence)",
		"UNIQUE (callback_event_id, endpoint_id)",
		"UNIQUE (delivery_id, attempt_number)",
		"UNIQUE (aggregate_type, aggregate_id, aggregate_sequence)",
		"event_id uuid PRIMARY KEY",
		"PRIMARY KEY (consumer_name, event_id)",
	)
	requireInvariantClauses(t, "000006_automated_matching.up.sql", automation,
		"CHECK (credited_atomic<=received_atomic)",
		"CREATE UNIQUE INDEX payment_match_aggregates_route_active_idx",
		"ON payment_match_aggregates(tenant_id,route_id) WHERE state<>'reversed'",
		"CHECK (allocation_role IN ('payment','gasfree_fee'))",
	)
}

func TestFinancialInvariantMutationContractsRemainFenced(t *testing.T) {
	settlement := readInvariantContractFile(t, "settlement.go")
	reorg := readInvariantContractFile(t, "reorg.go")
	outbox := readInvariantContractFile(t, "outbox_store.go")
	callback := readInvariantContractFile(t, "callback_store.go")
	matching := readInvariantContractFile(t, "matching_automation.go")
	refund := readInvariantContractFile(t, "refund_store.go")
	requireInvariantClauses(t, "settlement.go", settlement,
		"ON CONFLICT (chain_id,transaction_id,event_identity,asset_id,to_address) DO NOTHING",
		"duplicate transfer identity has different canonical facts",
		"duplicate transfer identity has different canonical inclusion facts",
		"WHERE id=$7 AND status='reorged'",
		"pgx.TxOptions{IsoLevel: pgx.Serializable}",
	)
	requireInvariantClauses(t, "reorg.go", reorg,
		"'payment_settlement.reversal'",
		"reversal_of",
		"CASE direction WHEN 'debit' THEN 'credit'::ledger_direction ELSE 'debit'::ledger_direction END",
		"UPDATE payment_matches SET state='reversed'",
	)
	requireInvariantClauses(t, "outbox_store.go", outbox,
		"WHERE id=$1 AND published_at IS NULL AND lease_token=$2 AND locked_until>clock_timestamp()",
		"ON CONFLICT (event_id) DO NOTHING",
		"outbox lease lost or history already advanced",
	)
	requireInvariantClauses(t, "callback_store.go", callback,
		"WHERE id=$1 AND status='leased' AND lease_token=$2 RETURNING attempt_count",
		"INSERT INTO callback_attempts",
		"callback delivery state conflict",
	)
	requireInvariantClauses(t, "refund_store.go", refund,
		"pm.allocation_role='payment'",
	)
	if strings.Contains(matching, "matching_processing_fee_expense") {
		t.Error("GasFree evidence fee must not create a merchant expense ledger leg")
	}
	for name, source := range map[string]string{"outbox_store.go": outbox, "callback_store.go": callback} {
		if strings.Contains(source, "INSERT INTO ledger_") || strings.Contains(source, "UPDATE ledger_") {
			t.Errorf("%s must not mutate ledger state during at-least-once delivery", name)
		}
	}
}
