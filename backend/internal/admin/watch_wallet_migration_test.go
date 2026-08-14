package admin

import (
	"os"
	"strings"
	"testing"
)

func TestWatchWalletReplacementRetainsHistoricRoutes(t *testing.T) {
	raw, err := os.ReadFile("../../migrations/000044_admin_watch_wallet_management.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(raw)
	for _, required := range []string{
		"admin_replace_watch_wallet_address",
		"SET status='retired'",
		"status IN('available','assigned')",
		"platform_wallet_runtime_admission",
		"custody_mode='watch_only'",
		"signer_key_reference IS NULL",
		"app.admin_merchant_ids",
		"w.status='active'",
		"cursor.capability='normalized_transfers_v1'",
		"cursor.heartbeat_at>=clock_timestamp()-interval '2 minutes'",
		"existing.status='quarantined'",
		"quarantined receiving address cannot be reactivated",
		"watch wallet inventory must be normalized before migration",
		"wallet_current_deposit_address_idx",
		"merchant_watch_wallet_chain_idx",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("watch-wallet migration lost safety contract %q", required)
		}
	}
	for _, forbidden := range []string{"DELETE FROM public.addresses", "UPDATE public.payment_routes", "requested_private", "signer_key_reference=", "SET status='active'"} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("watch-wallet replacement contains unsafe operation %q", forbidden)
		}
	}
}

func TestWatchWalletRuntimeGrantAndReadinessAreFailClosed(t *testing.T) {
	grants, err := os.ReadFile("../../../deploy/postgres/runtime-grants.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"admin_watch_wallet_inventory(uuid,uuid)", "admin_replace_watch_wallet_address(uuid,uuid,uuid,uuid,uuid,text,text,text,bigint,text)"} {
		if !strings.Contains(string(grants), required) {
			t.Fatalf("runtime grants lost %q", required)
		}
	}
	repository, err := os.ReadFile("postgres.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"to_regprocedure('public.admin_watch_wallet_inventory(uuid,uuid)')", "to_regprocedure('public.admin_replace_watch_wallet_address(uuid,uuid,uuid,uuid,uuid,text,text,text,bigint,text)')"} {
		if !strings.Contains(string(repository), required) {
			t.Fatalf("admin readiness lost %q", required)
		}
	}
}

func TestWatchWalletMigrationHasReversibleSurface(t *testing.T) {
	raw, err := os.ReadFile("../../migrations/000044_admin_watch_wallet_management.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(raw)
	for _, required := range []string{"admin_replace_watch_wallet_address", "admin_watch_wallet_inventory", "wallet_current_deposit_address_idx", "merchant_watch_wallet_chain_idx"} {
		if !strings.Contains(sql, required) {
			t.Fatalf("watch-wallet rollback lost %q", required)
		}
	}
}
