package scanner

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/domain"
)

// InternalProgress is where the internal lane stands against the ordinary scan.
type InternalProgress struct {
	Cursor, Scanned     uint64
	Started, HasScanned bool
}

// InternalCommit stores one range of the lane: its events enter the same queue
// and settlement as scanned transfers, and the lane's cursor moves with them.
type InternalCommit struct {
	ChainID, Shard string
	From, To       uint64
	Events         []domain.TransferEvent
	Unattributed   []UnattributedInflow
}

type InternalStore interface {
	InternalProgress(ctx context.Context, chainID, shard string) (InternalProgress, error)
	StartInternal(ctx context.Context, chainID, shard string, height uint64) error
	CommitInternal(ctx context.Context, commit InternalCommit) (int, error)
}

type InternalObserver interface {
	SetScannerInternalLag(uint64)
	AddScannerUnattributed(int)
}

// InternalPlan is what the lane may do right now. Source is nil until a scanner
// configuration exists. Idle means nothing is watched, so a block needs no work;
// Paused holds the lane during a maintenance window.
type InternalPlan struct {
	Source       Source
	Idle, Paused bool
}

// InternalLane looks for native transfers made through contracts, behind the
// ordinary scan and on its own cursor. Tracing depends on scarce providers and
// on what a stranger's transaction makes them return, so nothing here may touch
// the ordinary cursor: a failure delays internal transfers only.
type InternalLane struct {
	ChainID, Shard string
	Store          InternalStore
	RangeSize      uint64
	// Hold keeps the lane behind the ordinary cursor. A scanner that stops at a
	// finality depth is confirmed enough already; one that relies on overlap
	// rescans to raise confirmations must wait for the overlap to pass.
	Hold uint64
	// GiveUpAfter bounds one failing range: after it the range is recorded for
	// an operator and the lane moves on, so a transaction that makes providers
	// time out cannot stop detection for everyone else.
	GiveUpAfter time.Duration
	Now         func() time.Time
	Observer    InternalObserver

	failingFrom  uint64
	failingSince time.Time
}

const (
	// DefaultInternalRange is about one Ethereum finality epoch.
	DefaultInternalRange       = uint64(32)
	DefaultInternalGiveUpAfter = 30 * time.Minute
)

// RunOnce handles one range and says whether more work may be waiting.
func (l *InternalLane) RunOnce(ctx context.Context, plan InternalPlan) (bool, error) {
	if l.Store == nil || l.RangeSize == 0 || l.GiveUpAfter <= 0 {
		return false, errors.New("invalid internal lane configuration")
	}
	if plan.Paused || (plan.Source == nil && !plan.Idle) {
		return false, nil
	}
	progress, err := l.Store.InternalProgress(ctx, l.ChainID, l.Shard)
	if err != nil {
		return false, err
	}
	if !progress.HasScanned || progress.Scanned < l.Hold {
		return false, nil
	}
	upper := progress.Scanned - l.Hold
	// A lane that starts now does not replay old blocks; see the runbook for
	// the deliberate rewind.
	if !progress.Started {
		return false, l.Store.StartInternal(ctx, l.ChainID, l.Shard, upper)
	}
	if progress.Cursor >= upper {
		l.observeLag(0)
		return false, nil
	}
	l.observeLag(upper - progress.Cursor)
	from, to := progress.Cursor+1, min(progress.Cursor+l.RangeSize, upper)
	commit := InternalCommit{ChainID: l.ChainID, Shard: l.Shard, From: from, To: to}
	if !plan.Idle {
		batch, scanErr := plan.Source.ScanRange(ctx, from, to)
		if scanErr != nil {
			if !l.exhausted(from) {
				return false, scanErr
			}
			commit.Unattributed = []UnattributedInflow{{FromHeight: from, ToHeight: to, Reason: UnattributedGaveUp}}
		} else {
			for _, event := range batch.Events {
				if event.Kind != "native_internal" {
					continue
				}
				if event.Identity.ChainID != l.ChainID || event.BlockHeight < from || event.BlockHeight > to {
					return false, fmt.Errorf("internal transfer is outside the range it was scanned for")
				}
				commit.Events = append(commit.Events, event)
			}
			commit.Unattributed = batch.Unattributed
		}
	}
	recorded, err := l.Store.CommitInternal(ctx, commit)
	if err != nil {
		return false, err
	}
	l.failingSince = time.Time{}
	if l.Observer != nil && recorded > 0 {
		l.Observer.AddScannerUnattributed(recorded)
	}
	l.observeLag(upper - to)
	return to < upper, nil
}

func (l *InternalLane) exhausted(from uint64) bool {
	now := time.Now().UTC()
	if l.Now != nil {
		now = l.Now().UTC()
	}
	if l.failingSince.IsZero() || l.failingFrom != from {
		l.failingFrom, l.failingSince = from, now
	}
	return now.Sub(l.failingSince) >= l.GiveUpAfter
}

func (l *InternalLane) observeLag(lag uint64) {
	if l.Observer != nil {
		l.Observer.SetScannerInternalLag(lag)
	}
}
