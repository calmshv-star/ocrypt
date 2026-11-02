//go:build noncustodial

package main

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeCustodialWallets struct {
	count int64
	err   error
}

func (f fakeCustodialWallets) CustodialWalletCount(context.Context) (int64, error) {
	return f.count, f.err
}

func TestNoncustodialEditionReadinessRejectsCustodialWallets(t *testing.T) {
	if err := (custodialWalletProbe{store: fakeCustodialWallets{}}).Ping(context.Background()); err != nil {
		t.Fatalf("no custodial wallets must be ready: %v", err)
	}
	if err := (custodialWalletProbe{store: fakeCustodialWallets{count: 2}}).Ping(context.Background()); err == nil || !strings.Contains(err.Error(), "2 active custodial wallets") {
		t.Fatalf("custodial wallets must close readiness, got %v", err)
	}
	if err := (custodialWalletProbe{store: fakeCustodialWallets{err: errors.New("permission denied")}}).Ping(context.Background()); err == nil {
		t.Fatal("an unavailable check must close readiness")
	}
	if err := (custodialWalletProbe{}).Ping(context.Background()); err == nil {
		t.Fatal("a missing store must close readiness")
	}
	if probes := editionReadiness(nil); len(probes) != 1 {
		t.Fatalf("noncustodial edition must add the custodial wallet probe, got %d", len(probes))
	}
}
