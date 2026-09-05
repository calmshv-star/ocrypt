package main

import (
	"strings"
	"testing"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/money"
	"github.com/calmshv-star/ocrypt/backend/internal/scanner"
)

func fixture() (config, scanner.RangeBatch) {
	c := config{endpoint: "https://toncenter.com", chain: "ton:mainnet", asset: "ton", wallet: "0:" + strings.Repeat("1", 64), transaction: strings.Repeat("a", 64), amount: "4087000000", intent: "01a07261-6962-7aff-9eca-cfb1c9bfc188", route: "01a07261-699c-7bb3-8807-6b550a7ff408", from: 100, to: 120}
	e := domain.TransferEvent{Identity: domain.EventIdentity{ChainID: c.chain, TransactionID: c.transaction, EventIndex: "message:0", AssetID: c.asset, ToAddress: c.wallet}, Kind: "native_message", Amount: money.MustParse(c.amount), AssetDecimals: 9, BlockHeight: 110, BlockHash: strings.Repeat("b", 64), OnChainTime: time.Unix(1700000000, 0), Status: domain.TransferFinalized}
	b := scanner.RangeBatch{From: 100, To: 120, Events: []domain.TransferEvent{e}, Blocks: []scanner.Block{{Height: 110, Hash: e.BlockHash, Time: e.OnChainTime}}}
	return c, b
}

func TestRecoveryDryRunDefaultAndExplicitInputs(t *testing.T) {
	c, _ := fixture()
	if c.apply {
		t.Fatal("recovery must default to dry-run")
	}
	if err := c.validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*config){func(c *config) { c.from = 0 }, func(c *config) { c.to = c.from + 200 }, func(c *config) { c.amount = "0" }, func(c *config) { c.transaction = "receipt-prefix" }, func(c *config) { c.intent = "" }, func(c *config) { c.route = "" }, func(c *config) { c.chain = "solana:mainnet" }} {
		bad := c
		mutate(&bad)
		if bad.validate() == nil {
			t.Fatalf("accepted unsafe config: %+v", bad)
		}
	}
}

func TestRecoverySelectsOnlyOneExactFinalizedBoundEvent(t *testing.T) {
	c, b := fixture()
	other := b.Events[0]
	other.Identity.TransactionID = strings.Repeat("c", 64)
	b.Events = append(b.Events, other)
	if _, err := selectEvent(c, b); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*scanner.RangeBatch){func(b *scanner.RangeBatch) { b.Events = append(b.Events, b.Events[0]) }, func(b *scanner.RangeBatch) { b.Events[0].Status = domain.TransferObserved }, func(b *scanner.RangeBatch) { b.Events[0].Identity.ToAddress = "other" }, func(b *scanner.RangeBatch) { b.Events[0].Amount = money.MustParse("1") }, func(b *scanner.RangeBatch) { b.Blocks = nil }, func(b *scanner.RangeBatch) { b.From = 99 }, func(b *scanner.RangeBatch) { b.Events[0].AssetDecimals = 6 }, func(b *scanner.RangeBatch) { b.Events[0].Kind = "jetton_transfer" }} {
		_, bad := fixture()
		mutate(&bad)
		if _, err := selectEvent(c, bad); err == nil {
			t.Fatal("accepted unsafe event or range")
		}
	}
}
