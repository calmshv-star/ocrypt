package scanner

import (
	"context"
	"testing"
	"time"
)

func TestScannerReportsRemainingLagAfterCommittingEmptyPaymentRange(t *testing.T) {
	now := time.Now().UTC()
	store := &storeFixture{lease: Lease{Height: 9, Hash: "h9"}}
	source := sourceFixture{heads: []ProviderHead{{Provider: "a", ChainID: "chain", GenesisHash: "g", SafeHeight: 14, ObservedAt: now}}, batch: RangeBatch{From: 9, To: 12, Blocks: []Block{{Height: 9, Hash: "h9", ParentHash: "h8", Time: now}, {Height: 10, Hash: "h10", ParentHash: "h9", Time: now}, {Height: 11, Hash: "h11", ParentHash: "h10", Time: now}, {Height: 12, Hash: "h12", ParentHash: "h11", Time: now}}}}
	observer := &observerFixture{}
	batch, err := (Worker{ChainID: "chain", GenesisHash: "g", Source: source, Store: store, Quorum: 1, Overlap: 1, RangeSize: 4, Now: func() time.Time { return now }, Observer: observer}).RunOnce(context.Background())
	if err != nil || store.commits != 1 || len(batch.Events) != 0 || observer.lag != 2 {
		t.Fatalf("progress confused with caught-up: lag=%d commits=%d batch=%+v err=%v", observer.lag, store.commits, batch, err)
	}
}
