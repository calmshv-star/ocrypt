package postgres

import (
	"strings"
	"testing"
)

// custodialWalletGuard proves the database-level check the noncustodial
// edition uses to keep /readyz closed: only active wallets that could let the
// operator hold or sign funds are counted, across every tenant, and only the
// API runtime may ask.
func (f *faultDatabase) custodialWalletGuard(t *testing.T) {
	p := f.seed(t)
	api := f.pool(t, "merchant_api_runtime")
	count := func() int64 {
		t.Helper()
		var n int64
		if err := api.QueryRow(f.ctx, `SELECT platform_custodial_wallet_count()`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	baseline := count()
	insert := `INSERT INTO wallets(id,tenant_id,merchant_id,chain_id,custody_mode,signer_key_reference,status,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,'active',$7,$7)`
	watch, hot, signed := faultID(t), faultID(t), faultID(t)
	faultExec(t, f.ctx, f.admin, insert, watch, p.tenant, p.merchant, p.chain, "watch_only", nil, p.now)
	if got := count(); got != baseline {
		t.Fatalf("watch-only wallet counted as custodial: %d -> %d", baseline, got)
	}
	faultExec(t, f.ctx, f.admin, insert, hot, p.tenant, p.merchant, p.chain, "hot", nil, p.now)
	// A platform (merchant-less) wallet: only one watch-only wallet per merchant
	// and chain is allowed, and a signer reference must be caught either way.
	faultExec(t, f.ctx, f.admin, insert, signed, p.tenant, nil, p.chain, "watch_only", "kms://synthetic-fixture", p.now)
	if got := count(); got != baseline+2 {
		t.Fatalf("hot and signer-bearing wallets must be counted across tenants: %d -> %d", baseline, got)
	}
	faultExec(t, f.ctx, f.admin, `UPDATE wallets SET status='disabled' WHERE id IN ($1::uuid,$2::uuid)`, hot, signed)
	if got := count(); got != baseline {
		t.Fatalf("disabled wallets must not keep readiness closed: %d -> %d", baseline, got)
	}
	var n int64
	err := f.pool(t, "merchant_scanner_worker").QueryRow(f.ctx, `SELECT platform_custodial_wallet_count()`).Scan(&n)
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("only the API runtime may run the custodial wallet check, got n=%d err=%v", n, err)
	}
}
