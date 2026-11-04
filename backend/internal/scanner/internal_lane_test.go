package scanner

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/domain"
)

type laneStore struct {
	progress InternalProgress
	started  []uint64
	commits  []InternalCommit
	fail     error
}

func (s *laneStore) InternalProgress(context.Context, string, string) (InternalProgress, error) {
	return s.progress, nil
}

func (s *laneStore) StartInternal(_ context.Context, _, _ string, height uint64) error {
	s.started = append(s.started, height)
	return nil
}

func (s *laneStore) CommitInternal(ctx context.Context, commit InternalCommit) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if s.fail != nil {
		return 0, s.fail
	}
	s.commits = append(s.commits, commit)
	s.progress.Cursor = commit.To
	return len(commit.Unattributed), nil
}

type laneSource struct {
	batch RangeBatch
	err   error
	calls [][2]uint64
}

func (s *laneSource) Heads(context.Context) ([]ProviderHead, error) { return nil, nil }

func (s *laneSource) ScanRange(_ context.Context, from, to uint64) (RangeBatch, error) {
	s.calls = append(s.calls, [2]uint64{from, to})
	return s.batch, s.err
}

type laneObserver struct {
	lag          uint64
	unattributed int
}

func (o *laneObserver) SetScannerInternalLag(lag uint64) { o.lag = lag }
func (o *laneObserver) AddScannerUnattributed(count int) { o.unattributed += count }

func newLane(store InternalStore) *InternalLane {
	return &InternalLane{ChainID: "eip155:1", Shard: "default", Store: store, RangeSize: 32, GiveUpAfter: 30 * time.Minute}
}

func internalEvent(kind string, height uint64) domain.TransferEvent {
	return domain.TransferEvent{ID: kind, Kind: kind, BlockHeight: height, Identity: domain.EventIdentity{ChainID: "eip155:1"}}
}

// A lane that starts does not replay history, and follows the ordinary cursor.
func TestInternalLaneStartsWhereTheScanStands(t *testing.T) {
	store := &laneStore{progress: InternalProgress{Scanned: 1000, HasScanned: true}}
	lane := newLane(store)
	lane.Hold = 64
	if _, err := lane.RunOnce(t.Context(), InternalPlan{Source: &laneSource{}}); err != nil {
		t.Fatal(err)
	}
	if len(store.started) != 1 || store.started[0] != 936 || len(store.commits) != 0 {
		t.Fatalf("started=%v commits=%d", store.started, len(store.commits))
	}
	store = &laneStore{}
	if _, err := newLane(store).RunOnce(t.Context(), InternalPlan{Source: &laneSource{}}); err != nil || len(store.started) != 0 {
		t.Fatalf("a chain with no ordinary cursor must wait: %v %v", store.started, err)
	}
}

func TestInternalLaneNeverPassesTheOrdinaryScan(t *testing.T) {
	store := &laneStore{progress: InternalProgress{Cursor: 100, Scanned: 110, Started: true, HasScanned: true}}
	source := &laneSource{batch: RangeBatch{Events: []domain.TransferEvent{internalEvent("native_top_level", 105), internalEvent("native_internal", 105)}}}
	observer := &laneObserver{}
	lane := newLane(store)
	lane.Observer = observer
	more, err := lane.RunOnce(t.Context(), InternalPlan{Source: source})
	if err != nil || more {
		t.Fatalf("more=%v err=%v", more, err)
	}
	if len(source.calls) != 1 || source.calls[0] != [2]uint64{101, 110} {
		t.Fatalf("scanned %v", source.calls)
	}
	// The ordinary scan already queued the top-level transfer.
	if len(store.commits) != 1 || len(store.commits[0].Events) != 1 || store.commits[0].Events[0].Kind != "native_internal" {
		t.Fatalf("commits=%+v", store.commits)
	}
	if observer.lag != 0 {
		t.Fatalf("lag=%d", observer.lag)
	}
	source.calls = nil
	if more, err := lane.RunOnce(t.Context(), InternalPlan{Source: source}); err != nil || more || len(source.calls) != 0 {
		t.Fatalf("a caught-up lane must rest: more=%v err=%v calls=%v", more, err, source.calls)
	}
}

func TestInternalLaneTakesOneRangeAndSaysMoreIsWaiting(t *testing.T) {
	store := &laneStore{progress: InternalProgress{Cursor: 100, Scanned: 200, Started: true, HasScanned: true}}
	source := &laneSource{}
	more, err := newLane(store).RunOnce(t.Context(), InternalPlan{Source: source})
	if err != nil || !more || source.calls[0] != [2]uint64{101, 132} {
		t.Fatalf("more=%v err=%v calls=%v", more, err, source.calls)
	}
}

func TestInternalLaneGivesUpOnARangeAfterItsBudgetAndRecordsIt(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	store := &laneStore{progress: InternalProgress{Cursor: 100, Scanned: 110, Started: true, HasScanned: true}}
	source := &laneSource{err: errors.New("provider timed out")}
	observer := &laneObserver{}
	lane := newLane(store)
	lane.Now, lane.Observer = func() time.Time { return now }, observer
	for _, elapsed := range []time.Duration{0, 29 * time.Minute} {
		now = now.Add(elapsed)
		if _, err := lane.RunOnce(t.Context(), InternalPlan{Source: source}); !errors.Is(err, source.err) {
			t.Fatalf("a failing range must retry: %v", err)
		}
		if len(store.commits) != 0 {
			t.Fatal("the cursor moved before the budget ran out")
		}
	}
	now = now.Add(time.Minute)
	if _, err := lane.RunOnce(t.Context(), InternalPlan{Source: source}); err != nil {
		t.Fatal(err)
	}
	if len(store.commits) != 1 || len(store.commits[0].Events) != 0 || store.commits[0].To != 110 {
		t.Fatalf("commits=%+v", store.commits)
	}
	row := store.commits[0].Unattributed
	if len(row) != 1 || row[0].Reason != UnattributedGaveUp || row[0].FromHeight != 101 || row[0].ToHeight != 110 || observer.unattributed != 1 {
		t.Fatalf("rows=%+v observed=%d", row, observer.unattributed)
	}
	// The budget belongs to one range: the next failure starts a new one.
	store.progress.Scanned = 120
	if _, err := lane.RunOnce(t.Context(), InternalPlan{Source: source}); !errors.Is(err, source.err) {
		t.Fatalf("the next range must get its own budget: %v", err)
	}
}

func TestInternalLaneIdleAndPausedPlans(t *testing.T) {
	store := &laneStore{progress: InternalProgress{Cursor: 100, Scanned: 110, Started: true, HasScanned: true}}
	lane := newLane(store)
	if _, err := lane.RunOnce(t.Context(), InternalPlan{Paused: true, Source: &laneSource{}}); err != nil || len(store.commits) != 0 {
		t.Fatalf("a paused lane moved: %v %v", store.commits, err)
	}
	if _, err := lane.RunOnce(t.Context(), InternalPlan{}); err != nil || len(store.commits) != 0 {
		t.Fatalf("a lane without a source moved: %v %v", store.commits, err)
	}
	source := &laneSource{}
	if _, err := lane.RunOnce(t.Context(), InternalPlan{Idle: true, Source: source}); err != nil || len(store.commits) != 1 || len(source.calls) != 0 {
		t.Fatalf("nothing is watched, so the range needs no work: %v %v %v", store.commits, source.calls, err)
	}
}

func TestInternalLaneRefusesAnEventOutsideItsRange(t *testing.T) {
	store := &laneStore{progress: InternalProgress{Cursor: 100, Scanned: 110, Started: true, HasScanned: true}}
	for _, event := range []domain.TransferEvent{internalEvent("native_internal", 111), {Kind: "native_internal", BlockHeight: 105, Identity: domain.EventIdentity{ChainID: "eip155:56"}}} {
		source := &laneSource{batch: RangeBatch{Events: []domain.TransferEvent{event}}}
		if _, err := newLane(store).RunOnce(t.Context(), InternalPlan{Source: source}); err == nil || len(store.commits) != 0 {
			t.Fatalf("accepted %+v", event)
		}
	}
}

func TestInternalLaneKeepsItsCursorWhenTheCommitFails(t *testing.T) {
	store := &laneStore{progress: InternalProgress{Cursor: 100, Scanned: 110, Started: true, HasScanned: true}, fail: errors.New("database down")}
	if _, err := newLane(store).RunOnce(t.Context(), InternalPlan{Source: &laneSource{}}); !errors.Is(err, store.fail) {
		t.Fatalf("err=%v", err)
	}
}

func TestInternalLaneRejectsInvalidConfiguration(t *testing.T) {
	if _, err := (&InternalLane{}).RunOnce(t.Context(), InternalPlan{}); err == nil {
		t.Fatal("accepted an empty lane")
	}
}

// A step that ran out of time is exactly the range that needs giving up.
func TestInternalLaneRecordsARangeAfterItsStepTimedOut(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	store := &laneStore{progress: InternalProgress{Cursor: 100, Scanned: 110, Started: true, HasScanned: true}}
	source := &laneSource{err: context.DeadlineExceeded}
	lane := newLane(store)
	lane.Now = func() time.Time { return now }
	expired, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := lane.RunOnce(expired, InternalPlan{Source: source}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v", err)
	}
	now = now.Add(lane.GiveUpAfter)
	if _, err := lane.RunOnce(expired, InternalPlan{Source: source}); err != nil {
		t.Fatal(err)
	}
	if len(store.commits) != 1 || store.commits[0].Unattributed[0].Reason != UnattributedGaveUp {
		t.Fatalf("commits=%+v", store.commits)
	}
}

func TestInternalLaneBudgetBelongsToOneRange(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	store := &laneStore{progress: InternalProgress{Cursor: 100, Scanned: 300, Started: true, HasScanned: true}}
	source := &laneSource{err: errors.New("provider timed out")}
	lane := newLane(store)
	lane.Now = func() time.Time { return now }
	if _, err := lane.RunOnce(t.Context(), InternalPlan{Source: source}); err == nil {
		t.Fatal("the range must fail")
	}
	// A reorg or an operator's rewind moved the cursor to another range.
	store.progress.Cursor = 200
	now = now.Add(29 * time.Minute)
	if _, err := lane.RunOnce(t.Context(), InternalPlan{Source: source}); err == nil {
		t.Fatal("the new range must fail")
	}
	now = now.Add(2 * time.Minute)
	if _, err := lane.RunOnce(t.Context(), InternalPlan{Source: source}); err == nil || len(store.commits) != 0 {
		t.Fatalf("the new range used the old range's budget: %v %+v", err, store.commits)
	}
	now = now.Add(28 * time.Minute)
	if _, err := lane.RunOnce(t.Context(), InternalPlan{Source: source}); err != nil || len(store.commits) != 1 {
		t.Fatalf("err=%v commits=%+v", err, store.commits)
	}
}
