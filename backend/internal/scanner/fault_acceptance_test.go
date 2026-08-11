package scanner

import (
	"errors"
	"testing"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/domain"
)

func TestScannerFaultAcceptanceIgnoresDuplicateAndStaleHeads(t *testing.T) {
	now := time.Now().UTC()
	heads := []ProviderHead{
		{Provider: "provider-a", ChainID: "chain", GenesisHash: "genesis", SafeHeight: 20, ObservedAt: now},
		{Provider: "provider-a", ChainID: "chain", GenesisHash: "genesis", SafeHeight: 999, ObservedAt: now}, // duplicate identity
		{Provider: "provider-b", ChainID: "chain", GenesisHash: "genesis", SafeHeight: 19, ObservedAt: now},
		{Provider: "provider-c", ChainID: "chain", GenesisHash: "genesis", SafeHeight: 500, ObservedAt: now.Add(-10 * time.Minute)},
	}
	safe, err := quorumSafeHeightAt(heads, "chain", "genesis", 2, now, time.Minute)
	if err != nil || safe != 19 {
		t.Fatalf("duplicate/stale provider influenced quorum: safe=%d err=%v", safe, err)
	}
	if _, err := quorumSafeHeightAt([]ProviderHead{heads[0], heads[1], heads[3]}, "chain", "genesis", 2, now, time.Minute); err == nil {
		t.Fatal("one fresh provider plus its duplicate and a stale head satisfied quorum")
	}
}

func TestScannerFaultAcceptanceReorgReplaceAndReincludeIdentity(t *testing.T) {
	now := time.Now().UTC()
	replacement := RangeBatch{From: 2, To: 4, Blocks: []Block{
		{Height: 2, Hash: "block-2", ParentHash: "block-1", Time: now},
		{Height: 3, Hash: "block-3-new", ParentHash: "block-2", Time: now},
		{Height: 4, Hash: "block-4-new", ParentHash: "block-3-new", Time: now},
	}}
	err := validateRange(replacement, 2, 4, Lease{Height: 3, Hash: "block-3-old"}, 2)
	var reorg *ReorgError
	if !errors.As(err, &reorg) || reorg.Height != 3 || reorg.CommittedHash != "block-3-old" || reorg.NewHash != "block-3-new" {
		t.Fatalf("replacement was not identified as exact cursor reorg: %#v", err)
	}

	identity := domain.EventIdentity{ChainID: "chain", TransactionID: "transaction", EventIndex: "log:0", AssetID: "asset", ToAddress: "merchant"}
	reincluded := RangeBatch{From: 2, To: 4, Blocks: replacement.Blocks, Events: []domain.TransferEvent{{
		ID: "event", Identity: identity, BlockHeight: 3, BlockHash: "block-3-new",
	}}}
	if err := validateRange(reincluded, 2, 4, Lease{Height: 2, Hash: "block-2"}, 2); err != nil {
		t.Fatalf("canonical re-inclusion after common ancestor failed: %v", err)
	}
	oldKey, _ := identity.Key()
	distinct := identity
	distinct.EventIndex = "log:1"
	distinctKey, _ := distinct.Key()
	if oldKey == distinctKey {
		t.Fatal("new event index was confused with the same transfer re-included in a new block")
	}
}
