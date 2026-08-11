package scanner

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/domain"
)

type ProviderHead struct {
	Provider    string    `json:"provider"`
	ChainID     string    `json:"chain_id"`
	GenesisHash string    `json:"genesis_hash"`
	SafeHeight  uint64    `json:"safe_height"`
	ObservedAt  time.Time `json:"observed_at"`
}
type Block struct {
	Height     uint64    `json:"height"`
	Hash       string    `json:"hash"`
	ParentHash string    `json:"parent_hash"`
	Time       time.Time `json:"time"`
}
type RangeBatch struct {
	From            uint64                 `json:"from"`
	To              uint64                 `json:"to"`
	Blocks          []Block                `json:"blocks"`
	Events          []domain.TransferEvent `json:"events"`
	RuntimeEvidence []ConfigEvidence       `json:"-"`
}
type ConfigEvidence struct {
	Kind        string `json:"kind"`
	LogicalKey  string `json:"logical_key"`
	SnapshotID  string `json:"snapshot_id"`
	PayloadHash string `json:"payload_hash"`
	Version     int64  `json:"version"`
	FenceToken  int64  `json:"fence_token"`
}
type Source interface {
	Heads(context.Context) ([]ProviderHead, error)
	ScanRange(context.Context, uint64, uint64) (RangeBatch, error)
}
type Lease struct {
	ChainID, Shard, Owner string
	Height                uint64
	Hash                  string
	Version               int64
	Until                 time.Time
}
type CursorStore interface {
	Acquire(context.Context, string, string, string, time.Duration) (Lease, error)
	Commit(context.Context, Lease, RangeBatch) error
	RewindReorg(context.Context, Lease, RangeBatch, ReorgError) error
	Release(context.Context, Lease) error
	RecordGap(context.Context, string, uint64, uint64, string) error
}
type RuntimeObserver interface {
	SetScannerHeadLag(uint64)
	IncScannerGap(string)
	IncScannerReorg()
}
type Worker struct {
	ChainID, GenesisHash, Shard, Owner string
	Source                             Source
	Store                              CursorStore
	Quorum                             int
	Overlap, RangeSize                 uint64
	LeaseDuration, MaxHeadAge          time.Duration
	FinalityDepth                      uint64
	RuntimeEvidence                    []ConfigEvidence
	Now                                func() time.Time
	Observer                           RuntimeObserver
}

type ReorgError struct {
	Height                 uint64
	CommittedHash, NewHash string
}

func (e *ReorgError) Error() string {
	return fmt.Sprintf("reorg detected at height %d: committed %s, observed %s", e.Height, e.CommittedHash, e.NewHash)
}

func (w Worker) RunOnce(ctx context.Context) (RangeBatch, error) {
	if w.Source == nil || w.Store == nil || w.Quorum < 1 || w.Overlap < 1 || w.RangeSize <= w.Overlap {
		return RangeBatch{}, errors.New("invalid scanner configuration")
	}
	now := time.Now().UTC()
	if w.Now != nil {
		now = w.Now().UTC()
	}
	maxHeadAge := w.MaxHeadAge
	if maxHeadAge == 0 {
		maxHeadAge = 2 * time.Minute
	}
	if maxHeadAge < time.Second {
		return RangeBatch{}, errors.New("invalid scanner head freshness policy")
	}
	lease, err := w.Store.Acquire(ctx, w.ChainID, w.Shard, w.Owner, w.LeaseDuration)
	if err != nil {
		return RangeBatch{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = w.Store.Release(context.Background(), lease)
		}
	}()
	heads, err := w.Source.Heads(ctx)
	if err != nil {
		return RangeBatch{}, err
	}
	safe, err := quorumSafeHeightAt(heads, w.ChainID, w.GenesisHash, w.Quorum, now, maxHeadAge)
	if err != nil {
		return RangeBatch{}, err
	}
	if safe < w.FinalityDepth {
		safe = 0
	} else {
		safe -= w.FinalityDepth
	}
	if w.Observer != nil {
		lag := uint64(0)
		if safe > lease.Height {
			lag = safe - lease.Height
		}
		w.Observer.SetScannerHeadLag(lag)
	}
	from := lease.Height + 1
	if w.Overlap > 0 && lease.Hash != "" {
		if lease.Height+1 > w.Overlap {
			from = lease.Height - w.Overlap + 1
		} else {
			from = 0
		}
	}
	if safe < from {
		committed = true
		_ = w.Store.Release(ctx, lease)
		return RangeBatch{}, nil
	}
	to := from + w.RangeSize - 1
	if to > safe {
		to = safe
	}
	batch, err := w.Source.ScanRange(ctx, from, to)
	if err != nil {
		if gapErr := w.Store.RecordGap(ctx, w.ChainID, from, to, "provider_error"); gapErr == nil && w.Observer != nil {
			w.Observer.IncScannerGap("provider_error")
		}
		return RangeBatch{}, err
	}
	batch.RuntimeEvidence = append([]ConfigEvidence(nil), w.RuntimeEvidence...)
	for _, event := range batch.Events {
		if event.Identity.ChainID != w.ChainID {
			if gapErr := w.Store.RecordGap(ctx, w.ChainID, from, to, "cross_chain_event"); gapErr == nil && w.Observer != nil {
				w.Observer.IncScannerGap("cross_chain_event")
			}
			return RangeBatch{}, fmt.Errorf("normalized event belongs to another chain")
		}
	}
	if err := validateRange(batch, from, to, lease, w.Overlap); err != nil {
		var reorg *ReorgError
		if errors.As(err, &reorg) {
			if rewindErr := w.Store.RewindReorg(ctx, lease, batch, *reorg); rewindErr != nil {
				return RangeBatch{}, fmt.Errorf("persist reorg rewind: %w", rewindErr)
			}
			// RewindReorg consumes and releases the fenced lease. Returning the
			// typed error makes the incident observable while the next run can
			// resume from the durable common-ancestor cursor.
			committed = true
			if w.Observer != nil {
				w.Observer.IncScannerReorg()
			}
			return batch, reorg
		}
		if gapErr := w.Store.RecordGap(ctx, w.ChainID, from, to, "non_contiguous_range"); gapErr == nil && w.Observer != nil {
			w.Observer.IncScannerGap("non_contiguous_range")
		}
		return RangeBatch{}, err
	}
	if err := w.Store.Commit(ctx, lease, batch); err != nil {
		return RangeBatch{}, err
	}
	committed = true
	return batch, nil
}
func quorumSafeHeight(heads []ProviderHead, chainID, genesis string, quorum int) (uint64, error) {
	return quorumSafeHeightAt(heads, chainID, genesis, quorum, time.Now().UTC(), 2*time.Minute)
}

func quorumSafeHeightAt(heads []ProviderHead, chainID, genesis string, quorum int, now time.Time, maxAge time.Duration) (uint64, error) {
	var heights []uint64
	providers := map[string]bool{}
	for _, head := range heads {
		if head.ChainID != chainID || head.GenesisHash != genesis || head.Provider == "" || providers[head.Provider] {
			continue
		}
		if head.ObservedAt.IsZero() || now.Sub(head.ObservedAt) > maxAge || head.ObservedAt.After(now.Add(30*time.Second)) {
			continue
		}
		providers[head.Provider] = true
		heights = append(heights, head.SafeHeight)
	}
	if len(heights) < quorum {
		return 0, errors.New("provider quorum unavailable")
	}
	sort.Slice(heights, func(i, j int) bool { return heights[i] > heights[j] })
	return heights[quorum-1], nil
}
func validateRange(batch RangeBatch, from, to uint64, lease Lease, overlap uint64) error {
	if batch.From != from || batch.To != to || len(batch.Blocks) != int(to-from+1) {
		return fmt.Errorf("range coverage mismatch")
	}
	for i, block := range batch.Blocks {
		if block.Height != from+uint64(i) || block.Hash == "" || block.Time.IsZero() {
			return fmt.Errorf("block gap at offset %d", i)
		}
		if i > 0 && block.ParentHash != batch.Blocks[i-1].Hash {
			return fmt.Errorf("parent hash mismatch at %d", block.Height)
		}
	}
	blocks := make(map[uint64]string, len(batch.Blocks))
	for _, block := range batch.Blocks {
		blocks[block.Height] = block.Hash
	}
	for _, event := range batch.Events {
		if hash, ok := blocks[event.BlockHeight]; !ok || hash != event.BlockHash {
			return fmt.Errorf("event %s is not bound to a covered block", event.ID)
		}
	}
	if overlap > 0 && lease.Hash != "" {
		foundCursor := false
		for _, block := range batch.Blocks {
			if block.Height == lease.Height {
				foundCursor = true
				if block.Hash != lease.Hash {
					return &ReorgError{Height: block.Height, CommittedHash: lease.Hash, NewHash: block.Hash}
				}
			}
		}
		if !foundCursor {
			return fmt.Errorf("overlap does not include committed cursor")
		}
	}
	return nil
}
