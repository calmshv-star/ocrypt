package main

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/ids"
	"github.com/calmshv-star/ocrypt/backend/internal/scanner"
)

const internalStepTimeout = 5 * time.Minute

// internalLaneRunner runs the internal lane beside the ordinary scan loop, so a
// slow trace provider cannot hold up a scanner cycle.
type internalLaneRunner struct {
	plan atomic.Pointer[scanner.InternalPlan]
	once sync.Once
}

func (r *internalLaneRunner) setPlan(plan scanner.InternalPlan) {
	r.plan.Store(&plan)
}

// hold stops the lane after a failed platform read: its last plan may be stale.
func (r *internalLaneRunner) hold() {
	r.setPlan(scanner.InternalPlan{Paused: true})
}

func (r *internalLaneRunner) start(ctx context.Context, store scanner.InternalStore, observer scanner.InternalObserver, config scannerConfig, chainID string, hold uint64) {
	r.once.Do(func() {
		suffix, err := ids.New()
		if err != nil {
			slog.Warn("internal transfer lane cannot start", "chain_id", chainID, "error", err)
			return
		}
		lane := &scanner.InternalLane{ChainID: chainID, Shard: config.shard, Owner: config.workerID + "/" + suffix, Store: store, RangeSize: scanner.DefaultInternalRange,
			LeaseDuration: internalStepTimeout + time.Minute, Hold: hold, GiveUpAfter: scanner.DefaultInternalGiveUpAfter, Observer: observer}
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

// step bounds one range. A panic here must not take the ordinary scan with it,
// because both live in one process.
func (r *internalLaneRunner) step(ctx context.Context, lane *scanner.InternalLane) (more bool, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			more, err = false, fmt.Errorf("internal lane panic: %v", recovered)
		}
	}()
	plan := r.plan.Load()
	if plan == nil {
		return false, nil
	}
	stepCtx, cancel := context.WithTimeout(ctx, internalStepTimeout)
	defer cancel()
	return lane.RunOnce(stepCtx, *plan)
}
