package main

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/scanner"
)

// internalLaneRunner runs the internal lane beside the ordinary scan loop, so a
// slow trace provider cannot hold up a scanner cycle.
type internalLaneRunner struct {
	plan atomic.Pointer[scanner.InternalPlan]
	once sync.Once
}

func (r *internalLaneRunner) setPlan(plan scanner.InternalPlan) {
	r.plan.Store(&plan)
}

func (r *internalLaneRunner) start(ctx context.Context, store scanner.InternalStore, observer scanner.InternalObserver, config scannerConfig, chainID string, hold uint64) {
	r.once.Do(func() {
		lane := &scanner.InternalLane{ChainID: chainID, Shard: config.shard, Store: store, RangeSize: scanner.DefaultInternalRange,
			Hold: hold, GiveUpAfter: scanner.DefaultInternalGiveUpAfter, Observer: observer}
		go r.run(ctx, lane, config.pollInterval)
	})
}

func (r *internalLaneRunner) run(ctx context.Context, lane *scanner.InternalLane, pollInterval time.Duration) {
	for {
		more, err := r.step(ctx, lane)
		delay := pollInterval
		switch {
		case err != nil:
			delay = max(pollInterval, 30*time.Second)
			slog.Warn("internal transfer lane will retry; ordinary payments are not affected", "chain_id", lane.ChainID, "error", err)
		case more:
			delay = 0
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (r *internalLaneRunner) step(ctx context.Context, lane *scanner.InternalLane) (bool, error) {
	plan := r.plan.Load()
	if plan == nil {
		return false, nil
	}
	stepCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	return lane.RunOnce(stepCtx, *plan)
}
