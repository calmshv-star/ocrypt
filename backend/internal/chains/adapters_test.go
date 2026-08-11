package chains

import (
	"context"
	"testing"
	"time"
)

type evmFixture EVMReceipt

func (s evmFixture) Receipt(context.Context, string) (EVMReceipt, error) {
	return EVMReceipt(s), nil
}

type tronFixture TRONTransaction

func (s tronFixture) Transaction(context.Context, string) (TRONTransaction, error) {
	return TRONTransaction(s), nil
}

type solFixture SolanaTransaction

func (s solFixture) Transaction(context.Context, string) (SolanaTransaction, error) {
	return SolanaTransaction(s), nil
}

type tonFixture TONTransaction

func (s tonFixture) Transaction(context.Context, string) (TONTransaction, error) {
	return TONTransaction(s), nil
}

type aptosFixture AptosTransaction

func (s aptosFixture) Transaction(context.Context, string) (AptosTransaction, error) {
	return AptosTransaction(s), nil
}

func TestEVMNormalizesNativeLogAndInternalWithoutTransactionCollision(t *testing.T) {
	now := time.Now()
	source := evmFixture{
		TransactionID: "0xtx",
		BlockHeight:   10,
		BlockHash:     "0xblock",
		BlockTime:     now,
		Success:       true,
		Finalized:     true,
		Native:        &EVMNative{From: "a", To: "b", Amount: "1", AssetID: "eth", Decimals: 18},
		Logs:          []EVMLog{{Index: 2, From: "a", To: "b", Amount: "2", AssetID: "usdc", Decimals: 6, Transfer: true}},
		Traces:        []EVMTrace{{TraceAddress: []uint32{0, 2, 1}, From: "a", To: "b", Amount: "3", AssetID: "eth", Decimals: 18, Success: true}},
	}
	events, err := (EVMAdapter{ChainID: "eip155:1", Source: source}).Normalize(context.Background(), "0xtx")
	if err != nil || len(events) != 3 {
		t.Fatalf("events=%d err=%v", len(events), err)
	}
	want := []string{"native:0", "log:2", "trace:0,2,1"}
	for i, event := range events {
		if event.Identity.EventIndex != want[i] || event.Status != "finalized" {
			t.Fatalf("event %d: %+v", i, event)
		}
	}
}

func TestDefaultEventIDsAreReplayStableAndDistinctAcrossTransactions(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	normalizeTx := func(tx string) []string {
		source := evmFixture{
			TransactionID: tx,
			BlockHeight:   10,
			BlockHash:     "0xblock",
			BlockTime:     now,
			Success:       true,
			Finalized:     true,
			Logs:          []EVMLog{{Index: 0, From: "a", To: "b", Amount: "2", AssetID: "usdc", Decimals: 6, Transfer: true}},
		}
		events, err := (EVMAdapter{ChainID: "eip155:1", Source: source}).Normalize(context.Background(), tx)
		if err != nil || len(events) != 1 {
			t.Fatalf("events=%d err=%v", len(events), err)
		}
		return []string{events[0].ID, events[0].EvidenceHash}
	}
	first := normalizeTx("0xtx-a")
	replay := normalizeTx("0xtx-a")
	second := normalizeTx("0xtx-b")
	if first[0] != replay[0] || first[1] != replay[1] {
		t.Fatalf("replay drift: first=%v replay=%v", first, replay)
	}
	if first[0] == second[0] {
		t.Fatalf("different transaction identities collided on %s", first[0])
	}
}

func TestTRONGasFreeKeepsReceivedAndFeeAsSeparateFacts(t *testing.T) {
	source := tronFixture{
		TransactionID: "tx",
		BlockHeight:   1,
		BlockHash:     "block",
		BlockTime:     time.Now(),
		Success:       true,
		Transfers: []TRONTransfer{{
			Index: 0, From: "a", To: "merchant", AssetID: "usdt", ReceivedAmount: "6390000", Decimals: 6,
			Mechanism: "gasfree", FeeDeductedAmount: "1500000", FeeCollector: "fee",
		}},
	}
	events, err := (TRONAdapter{ChainID: "tron:mainnet", Source: source}).Normalize(context.Background(), "tx")
	if err != nil || len(events) != 2 {
		t.Fatalf("events=%d err=%v", len(events), err)
	}
	if events[0].Amount.String() != "6390000" || events[1].Kind != "gasfree_fee" {
		t.Fatalf("unexpected facts: %+v", events)
	}
}

func TestOtherFamiliesHaveStableNestedEventIdentity(t *testing.T) {
	inner := uint32(3)
	now := time.Now()
	sol, err := (SolanaAdapter{ChainID: "solana:mainnet", Source: solFixture{Signature: "sig", Slot: 5, BlockHash: "block", BlockTime: now, Success: true, Transfers: []SolanaTransfer{{OuterIndex: 2, InnerIndex: &inner, Program: "spl-token-2022", From: "a", To: "b", AssetID: "token", Amount: "4", Decimals: 6}}}}).Normalize(context.Background(), "sig")
	if err != nil {
		t.Fatal(err)
	}
	ton, err := (TONAdapter{ChainID: "ton:mainnet", Source: tonFixture{Hash: "hash", LT: 6, BlockHash: "block", BlockTime: now, Success: true, Messages: []TONMessage{{Index: 1, From: "a", To: "b", AssetID: "jetton", Amount: "5", Decimals: 9, Jetton: true}}}}).Normalize(context.Background(), "hash")
	if err != nil {
		t.Fatal(err)
	}
	aptos, err := (AptosAdapter{ChainID: "aptos:1", Source: aptosFixture{Hash: "hash", Version: 7, BlockHash: "block", BlockTime: now, Success: true, Transfers: []AptosTransfer{{EventIndex: 4, From: "a", To: "b", AssetID: "fa", Amount: "6", Decimals: 8, FungibleAsset: true}}}}).Normalize(context.Background(), "hash")
	if err != nil {
		t.Fatal(err)
	}
	if sol[0].Identity.EventIndex != "instruction:2/inner:3" || sol[0].Kind != "token2022_transfer" || ton[0].Kind != "jetton_transfer" || aptos[0].Kind != "aptos_fungible_asset_transfer" {
		t.Fatalf("normalization drift: %+v %+v %+v", sol, ton, aptos)
	}
}
