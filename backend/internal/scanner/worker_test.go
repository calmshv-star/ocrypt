package scanner

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/domain"
)

type sourceFixture struct {
	heads []ProviderHead
	batch RangeBatch
	err   error
}

type retryableFixtureError struct{}

func (retryableFixtureError) Error() string   { return "retry later" }
func (retryableFixtureError) Retryable() bool { return true }

func (s sourceFixture) Heads(context.Context) ([]ProviderHead, error) { return s.heads, nil }
func (s sourceFixture) ScanRange(context.Context, uint64, uint64) (RangeBatch, error) {
	return s.batch, s.err
}

type storeFixture struct {
	lease                            Lease
	commits, rewinds, gaps, releases int
	lastBatch                        RangeBatch
}

type observerFixture struct {
	lag    uint64
	gaps   map[string]int
	reorgs int
}

func (observer *observerFixture) SetScannerHeadLag(value uint64) { observer.lag = value }
func (observer *observerFixture) IncScannerGap(reason string) {
	if observer.gaps == nil {
		observer.gaps = map[string]int{}
	}
	observer.gaps[reason]++
}
func (observer *observerFixture) IncScannerReorg() { observer.reorgs++ }

func (s *storeFixture) Acquire(context.Context, string, string, string, time.Duration) (Lease, error) {
	return s.lease, nil
}
func (s *storeFixture) Commit(_ context.Context, _ Lease, batch RangeBatch) error {
	s.commits++
	s.lastBatch = batch
	return nil
}

func TestScannerAppliesFinalityDepthAndCarriesRuntimeEvidence(t *testing.T) {
	now := time.Now().UTC()
	store := &storeFixture{lease: Lease{Height: 0}}
	source := sourceFixture{heads: []ProviderHead{{Provider: "a", ChainID: "chain", GenesisHash: "g", SafeHeight: 5, ObservedAt: now}}, batch: RangeBatch{From: 1, To: 3, Blocks: []Block{{Height: 1, Hash: "h1", Time: now}, {Height: 2, Hash: "h2", ParentHash: "h1", Time: now}, {Height: 3, Hash: "h3", ParentHash: "h2", Time: now}}}}
	evidence := []ConfigEvidence{{Kind: "chain", LogicalKey: "chain", SnapshotID: "018f0f65-7a34-7cc4-9f36-7a86496ee463", PayloadHash: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", Version: 1, FenceToken: 2}}
	batch, err := (Worker{ChainID: "chain", GenesisHash: "g", Source: source, Store: store, Quorum: 1, Overlap: 1, RangeSize: 3, FinalityDepth: 2, RuntimeEvidence: evidence, Now: func() time.Time { return now }}).RunOnce(context.Background())
	if err != nil || batch.To != 3 || len(store.lastBatch.RuntimeEvidence) != 1 || store.lastBatch.RuntimeEvidence[0].FenceToken != 2 {
		t.Fatalf("batch=%+v stored=%+v err=%v", batch, store.lastBatch, err)
	}
}

func TestSparseRangeBindsOverlapCursorWithoutInventingIntermediateParents(t *testing.T) {
	lease := Lease{Height: 11, Hash: "cursor-hash"}
	batch := RangeBatch{
		From: 10, To: 100, SparseBlocks: true, IdleCheckpoint: true,
		Blocks: []Block{
			{Height: 10, Hash: "from-hash", ParentHash: "parent-9", Time: time.Unix(10, 0)},
			{Height: 11, Hash: "cursor-hash", ParentHash: "from-hash", Time: time.Unix(11, 0)},
			{Height: 100, Hash: "head-hash", ParentHash: "parent-99", Time: time.Unix(100, 0)},
		},
	}
	if err := validateRange(batch, 10, 100, lease, 2); err != nil {
		t.Fatalf("valid sparse cursor evidence rejected: %v", err)
	}
}
func (s *storeFixture) RewindReorg(_ context.Context, _ Lease, _ RangeBatch, _ ReorgError) error {
	s.rewinds++
	return nil
}
func (s *storeFixture) Release(context.Context, Lease) error { s.releases++; return nil }
func (s *storeFixture) RecordGap(context.Context, string, uint64, uint64, string) error {
	s.gaps++
	return nil
}
func TestScannerUsesQuorumSafeHeadAndCommitsContiguousRange(t *testing.T) {
	now := time.Now().UTC()
	store := &storeFixture{lease: Lease{Height: 9, Hash: "h9"}}
	source := sourceFixture{heads: []ProviderHead{{Provider: "a", ChainID: "chain", GenesisHash: "g", SafeHeight: 15, ObservedAt: now}, {Provider: "b", ChainID: "chain", GenesisHash: "g", SafeHeight: 14, ObservedAt: now}, {Provider: "c", ChainID: "chain", GenesisHash: "g", SafeHeight: 99, ObservedAt: now}}, batch: RangeBatch{From: 9, To: 12, Blocks: []Block{{Height: 9, Hash: "h9", ParentHash: "h8", Time: now}, {Height: 10, Hash: "h10", ParentHash: "h9", Time: now}, {Height: 11, Hash: "h11", ParentHash: "h10", Time: now}, {Height: 12, Hash: "h12", ParentHash: "h11", Time: now}}}}
	batch, err := (Worker{ChainID: "chain", GenesisHash: "g", Source: source, Store: store, Quorum: 2, Overlap: 1, RangeSize: 4, Now: func() time.Time { return now }}).RunOnce(context.Background())
	if err != nil || batch.To != 12 || store.commits != 1 {
		t.Fatalf("batch=%+v commits=%d err=%v", batch, store.commits, err)
	}
}
func TestScannerRecordsGapAndDoesNotAdvanceOnProviderFailure(t *testing.T) {
	now := time.Now().UTC()
	store := &storeFixture{lease: Lease{Height: 1}}
	source := sourceFixture{heads: []ProviderHead{{Provider: "a", ChainID: "chain", GenesisHash: "g", SafeHeight: 2, ObservedAt: now}}, err: errors.New("timeout")}
	observer := &observerFixture{}
	_, err := (Worker{ChainID: "chain", GenesisHash: "g", Source: source, Store: store, Quorum: 1, Overlap: 1, RangeSize: 2, Now: func() time.Time { return now }, Observer: observer}).RunOnce(context.Background())
	if err == nil || store.gaps != 1 || store.commits != 0 || observer.gaps["provider_error"] != 1 || observer.lag != 1 {
		t.Fatalf("gaps=%d observed=%v lag=%d commits=%d err=%v", store.gaps, observer.gaps, observer.lag, store.commits, err)
	}
}

func TestScannerRetriesTemporaryProviderFailureWithoutOpeningGap(t *testing.T) {
	now := time.Now().UTC()
	store := &storeFixture{lease: Lease{Height: 1}}
	source := sourceFixture{heads: []ProviderHead{{Provider: "a", ChainID: "chain", GenesisHash: "g", SafeHeight: 2, ObservedAt: now}}, err: retryableFixtureError{}}
	observer := &observerFixture{}
	_, err := (Worker{ChainID: "chain", GenesisHash: "g", Source: source, Store: store, Quorum: 1, Overlap: 1, RangeSize: 2, Now: func() time.Time { return now }, Observer: observer}).RunOnce(context.Background())
	if err == nil || store.gaps != 0 || store.commits != 0 || len(observer.gaps) != 0 {
		t.Fatalf("gaps=%d observed=%v commits=%d err=%v", store.gaps, observer.gaps, store.commits, err)
	}
}
func TestScannerFailsClosedOnWrongGenesisOrBrokenParent(t *testing.T) {
	if _, err := quorumSafeHeight([]ProviderHead{{Provider: "a", ChainID: "chain", GenesisHash: "wrong", SafeHeight: 2, ObservedAt: time.Now()}}, "chain", "g", 1); err == nil {
		t.Fatal("wrong genesis passed quorum")
	}
	now := time.Now().UTC()
	batch := RangeBatch{From: 1, To: 2, Blocks: []Block{{Height: 1, Hash: "h1", ParentHash: "h0", Time: now}, {Height: 2, Hash: "h2", ParentHash: "wrong", Time: now}}}
	if err := validateRange(batch, 1, 2, Lease{}, 0); err == nil {
		t.Fatal("broken parent chain passed")
	}
}

func TestScannerAcceptsExplicitParentLinkedSparseSlots(t *testing.T) {
	now := time.Now().UTC()
	batch := RangeBatch{From: 10, To: 13, SparseBlocks: true, Blocks: []Block{
		{Height: 10, Hash: "h10", ParentHash: "h9", Time: now},
		{Height: 13, Hash: "h13", ParentHash: "h10", Time: now},
	}}
	if err := validateRange(batch, 10, 14, Lease{Height: 10, Hash: "h10"}, 1); err != nil {
		t.Fatalf("valid skipped slots rejected: %v", err)
	}
	batch.Blocks[1].ParentHash = "wrong"
	if err := validateRange(batch, 10, 14, Lease{Height: 10, Hash: "h10"}, 1); err == nil {
		t.Fatal("sparse range without canonical parent linkage passed")
	}
}

func TestScannerAcceptsFinalizedIndexedCheckpointWithEventEvidence(t *testing.T) {
	now := time.Now().UTC()
	event := domain.TransferEvent{ID: "event-1", BlockHeight: 12, BlockHash: "h12"}
	batch := RangeBatch{From: 10, To: 20, SparseBlocks: true, IndexedCheckpoint: true, Events: []domain.TransferEvent{event}, Blocks: []Block{
		{Height: 10, Hash: "h10", ParentHash: "h9", Time: now},
		{Height: 12, Hash: "h12", ParentHash: "h11", Time: now},
		{Height: 20, Hash: "h20", ParentHash: "h19", Time: now},
	}}
	if err := validateRange(batch, 10, 20, Lease{Height: 10, Hash: "h10"}, 1); err != nil {
		t.Fatalf("valid indexed checkpoint rejected: %v", err)
	}
	batch.Events[0].BlockHash = "wrong"
	if err := validateRange(batch, 10, 20, Lease{Height: 10, Hash: "h10"}, 1); err == nil {
		t.Fatal("indexed checkpoint accepted an event without block evidence")
	}
}

func TestScannerRejectsStaleQuorumAndInsufficientOverlap(t *testing.T) {
	now := time.Now().UTC()
	heads := []ProviderHead{
		{Provider: "fresh", ChainID: "chain", GenesisHash: "g", SafeHeight: 20, ObservedAt: now},
		{Provider: "stale", ChainID: "chain", GenesisHash: "g", SafeHeight: 99, ObservedAt: now.Add(-10 * time.Minute)},
	}
	if _, err := quorumSafeHeightAt(heads, "chain", "g", 2, now, time.Minute); err == nil {
		t.Fatal("stale provider satisfied quorum")
	}
	worker := Worker{ChainID: "chain", GenesisHash: "g", Source: sourceFixture{}, Store: &storeFixture{}, Quorum: 1, Overlap: 4, RangeSize: 4}
	if _, err := worker.RunOnce(context.Background()); err == nil {
		t.Fatal("range that cannot cover overlap was accepted")
	}
	batch := RangeBatch{From: 10, To: 11, Blocks: []Block{{Height: 10, Hash: "h10", ParentHash: "h9", Time: now}, {Height: 11, Hash: "h11", ParentHash: "h10", Time: now}}}
	if err := validateRange(batch, 10, 11, Lease{Height: 12, Hash: "h12"}, 1); err == nil {
		t.Fatal("overlap without committed cursor was accepted")
	}
}

func TestReorgErrorCarriesBothHashes(t *testing.T) {
	now := time.Now().UTC()
	batch := RangeBatch{From: 7, To: 8, Blocks: []Block{{Height: 7, Hash: "new-7", ParentHash: "h6", Time: now}, {Height: 8, Hash: "new-8", ParentHash: "new-7", Time: now}}}
	err := validateRange(batch, 7, 8, Lease{Height: 7, Hash: "old-7"}, 1)
	var reorg *ReorgError
	if !errors.As(err, &reorg) || reorg.Height != 7 || reorg.CommittedHash != "old-7" || reorg.NewHash != "new-7" {
		t.Fatalf("unexpected reorg error: %#v", err)
	}
}

func TestScannerPersistsReorgRewindBeforeReturningIncident(t *testing.T) {
	now := time.Now().UTC()
	store := &storeFixture{lease: Lease{ChainID: "chain", Height: 8, Hash: "old-8"}}
	source := sourceFixture{
		heads: []ProviderHead{{Provider: "a", ChainID: "chain", GenesisHash: "g", SafeHeight: 9, ObservedAt: now}},
		batch: RangeBatch{From: 7, To: 9, Blocks: []Block{
			{Height: 7, Hash: "new-7", ParentHash: "h6", Time: now},
			{Height: 8, Hash: "new-8", ParentHash: "new-7", Time: now},
			{Height: 9, Hash: "new-9", ParentHash: "new-8", Time: now},
		}},
	}
	observer := &observerFixture{}
	_, err := (Worker{ChainID: "chain", GenesisHash: "g", Source: source, Store: store, Quorum: 1, Overlap: 2, RangeSize: 3, Now: func() time.Time { return now }, Observer: observer}).RunOnce(context.Background())
	var reorg *ReorgError
	if !errors.As(err, &reorg) || store.rewinds != 1 || store.releases != 0 || store.commits != 0 || observer.reorgs != 1 {
		t.Fatalf("err=%v rewinds=%d releases=%d commits=%d reorgs=%d", err, store.rewinds, store.releases, store.commits, observer.reorgs)
	}
}
