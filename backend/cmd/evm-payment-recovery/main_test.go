package main

import (
	"strings"
	"testing"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/money"
	"github.com/calmshv-star/ocrypt/backend/internal/scanner"
)

func fixture() (config, domain.TransferEvent, []scanner.ProviderHead) {
	c := config{endpoint: "https://ethereum-rpc.publicnode.com", chain: "eip155:1", asset: "eth-ethereum", wallet: "0x" + strings.Repeat("1", 40), transaction: "0x" + strings.Repeat("a", 64), amount: "2349000000000000", intent: "00000000-0000-7000-8000-000000000001", route: "00000000-0000-7000-8000-000000000002"}
	e := domain.TransferEvent{ID: "event-fixture", Identity: domain.EventIdentity{ChainID: c.chain, TransactionID: c.transaction, EventIndex: "native:0", AssetID: c.asset, ToAddress: c.wallet}, Kind: "native_top_level", FromAddress: "0x" + strings.Repeat("2", 40), Amount: money.MustParse(c.amount), AssetDecimals: 18, BlockHeight: 100, BlockHash: "0x" + strings.Repeat("b", 64), OnChainTime: time.Unix(1700000000, 0), Status: domain.TransferFinalized, Confirmations: 10, ParserVersion: "evm-v1", EvidenceHash: strings.Repeat("c", 64)}
	head := []scanner.ProviderHead{{Provider: "rpc", ChainID: c.chain, GenesisHash: "0x" + strings.Repeat("d", 64), SafeHeight: 109, ObservedAt: time.Now().UTC()}}
	return c, e, head
}

func TestRecoveryRequiresExplicitCanonicalInputsAndDefaultsDryRun(t *testing.T) {
	c, _, _ := fixture()
	if c.apply {
		t.Fatal("must default to dry-run")
	}
	if err := c.validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*config){func(c *config) { c.chain = "" }, func(c *config) { c.chain = "eip155:01" }, func(c *config) { c.chain = "eip155:0" }, func(c *config) { c.chain = "ton:mainnet" }, func(c *config) { c.amount = "0" }, func(c *config) { c.amount = "1.2" }, func(c *config) { c.transaction = "0x123" }, func(c *config) { c.transaction = strings.ToUpper(c.transaction) }, func(c *config) { c.wallet = "0x123" }, func(c *config) { c.intent = "" }, func(c *config) { c.route = "" }} {
		bad := c
		mutate(&bad)
		if bad.validate() == nil {
			t.Fatalf("accepted unsafe config: %+v", bad)
		}
	}
}

func TestRecoveryAcceptsOnlyOneExactFinalizedNativeEvent(t *testing.T) {
	c, e, heads := fixture()
	if _, err := selectEvent(c, []domain.TransferEvent{e}, heads); err != nil {
		t.Fatal(err)
	}
	if _, err := selectEvent(c, []domain.TransferEvent{e, e}, heads); err == nil {
		t.Fatal("accepted duplicate events")
	}
	for _, mutate := range []func(*domain.TransferEvent){func(e *domain.TransferEvent) { e.Status = domain.TransferObserved }, func(e *domain.TransferEvent) { e.Identity.ChainID = "eip155:56" }, func(e *domain.TransferEvent) { e.Identity.ToAddress = "other" }, func(e *domain.TransferEvent) { e.Identity.TransactionID = "other" }, func(e *domain.TransferEvent) { e.Identity.AssetID = "usdt" }, func(e *domain.TransferEvent) { e.Amount = money.MustParse("1") }, func(e *domain.TransferEvent) { e.BlockHeight = 110 }, func(e *domain.TransferEvent) { e.Kind = "native_internal" }, func(e *domain.TransferEvent) { e.Identity.EventIndex = "log:0" }, func(e *domain.TransferEvent) { e.AssetDecimals = 6 }, func(e *domain.TransferEvent) { e.EvidenceHash = "" }, func(e *domain.TransferEvent) { e.BlockHash = "" }} {
		bad := e
		mutate(&bad)
		if _, err := selectEvent(c, []domain.TransferEvent{bad}, heads); err == nil {
			t.Fatal("accepted unsafe event")
		}
	}
	if _, err := selectEvent(c, []domain.TransferEvent{e}, nil); err == nil {
		t.Fatal("accepted missing chain identity")
	}
	heads[0].ChainID = "eip155:56"
	if _, err := selectEvent(c, []domain.TransferEvent{e}, heads); err == nil {
		t.Fatal("accepted wrong chain identity")
	}
}
