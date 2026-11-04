package postgres

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/money"
	"github.com/calmshv-star/ocrypt/backend/internal/scanner"
	"github.com/jackc/pgx/v5"
)

// internalLane proves the lane's storage under the production scanner role: its
// cursor follows the ordinary one, events reach the shared queue once, a replay
// changes nothing, and findings are recorded once.
func (f *faultDatabase) internalLane(t *testing.T) {
	const chain, shard = "eip155:990099", "default"
	now := time.Now().UTC()
	faultExec(t, f.ctx, f.admin, `INSERT INTO chains(id,family,network_name,status,required_confirmations,maximum_reorg_depth,created_at,updated_at) VALUES($1,'evm','Internal lane chain','active',12,64,$2,$2)`, chain, now)
	faultExec(t, f.ctx, f.admin, `INSERT INTO assets(id,chain_id,symbol,name,kind,canonical_contract,decimals,status,created_at,updated_at) VALUES('eth-lane',$1,'ETH','Lane ether','native','native',18,'active',$2,$2)`, chain, now)
	store, err := NewScannerStore(f.pool(t, "merchant_scanner_worker"), "internal-lane-test")
	if err != nil {
		t.Fatal(err)
	}
	progress, err := store.InternalProgress(f.ctx, chain, shard)
	if err != nil || progress.HasScanned || progress.Started {
		t.Fatalf("a chain without a scan has no progress: %+v %v", progress, err)
	}
	faultExec(t, f.ctx, f.admin, `INSERT INTO scanner_cursors(chain_id,scanner_shard,capability,cursor_height,cursor_hash,version,updated_at) VALUES($1,$2,'internal-lane-test',1000,'0xhash',1,$3)`, chain, shard, now)
	if err := store.StartInternal(f.ctx, chain, shard, 900); err != nil {
		t.Fatal(err)
	}
	if err := store.StartInternal(f.ctx, chain, shard, 950); err != nil {
		t.Fatal(err)
	}
	progress, err = store.InternalProgress(f.ctx, chain, shard)
	if err != nil || !progress.HasScanned || !progress.Started || progress.Scanned != 1000 || progress.Cursor != 900 {
		t.Fatalf("progress=%+v err=%v", progress, err)
	}
	event := domain.TransferEvent{
		ID:            faultID(t),
		Identity:      domain.EventIdentity{ChainID: chain, TransactionID: "0xtx", EventIndex: "trace:0,11", AssetID: "eth-lane", ToAddress: "0xwallet"},
		Kind:          "native_internal",
		Amount:        money.MustParse("5"),
		AssetDecimals: 18,
		BlockHeight:   905,
		BlockHash:     "0xblock",
		Status:        domain.TransferFinalized,
	}
	inflow := scanner.UnattributedInflow{Address: "0xwallet", FromHeight: 901, ToHeight: 932, Amount: "7", Reason: scanner.UnattributedBalance}
	commit := scanner.InternalCommit{ChainID: chain, Shard: shard, From: 901, To: 932, Version: progress.Version, Events: []domain.TransferEvent{event}, Unattributed: []scanner.UnattributedInflow{inflow}}
	recorded, err := store.CommitInternal(f.ctx, commit)
	if err != nil || recorded != 1 {
		t.Fatalf("recorded=%d err=%v", recorded, err)
	}
	if n := f.count(t, `SELECT count(*) FROM scanner_transfer_queue WHERE chain_id=$1 AND status='pending' AND event_id=$2`, chain, event.ID); n != 1 {
		t.Fatalf("queued transfers %d", n)
	}
	if n := f.count(t, `SELECT count(*) FROM scanner_unattributed_inflows WHERE chain_id=$1 AND status='open' AND amount_atomic=7 AND from_height=901 AND to_height=932`, chain); n != 1 {
		t.Fatalf("recorded inflows %d", n)
	}
	// The range moved on, so a stale writer cannot move the cursor again.
	if _, err := store.CommitInternal(f.ctx, commit); !errors.Is(err, domain.ErrVersionConflict) {
		t.Fatalf("a replay of the same range must lose the cursor race: %v", err)
	}
	// A deliberate rewind replays the range: nothing is queued twice.
	faultExec(t, f.ctx, f.admin, `UPDATE scanner_internal_cursors SET cursor_height=900,version=version+1 WHERE chain_id=$1`, chain)
	current := func() int64 {
		t.Helper()
		progress, err := store.InternalProgress(f.ctx, chain, shard)
		if err != nil {
			t.Fatal(err)
		}
		return progress.Version
	}
	commit.Version = current()
	recorded, err = store.CommitInternal(f.ctx, commit)
	if err != nil || recorded != 0 {
		t.Fatalf("recorded=%d err=%v", recorded, err)
	}
	if n := f.count(t, `SELECT count(*) FROM scanner_transfer_queue WHERE chain_id=$1`, chain); n != 1 {
		t.Fatalf("a replay queued %d transfers", n)
	}
	if n := f.count(t, `SELECT count(*) FROM scanner_unattributed_inflows WHERE chain_id=$1`, chain); n != 1 {
		t.Fatalf("a replay wrote %d findings", n)
	}
	// A range given up with no address, transaction or amount.
	recorded, err = store.CommitInternal(f.ctx, scanner.InternalCommit{ChainID: chain, Shard: shard, From: 933, To: 964, Version: current(), Unattributed: []scanner.UnattributedInflow{{FromHeight: 933, ToHeight: 964, Reason: scanner.UnattributedGaveUp}}})
	if err != nil || recorded != 1 {
		t.Fatalf("recorded=%d err=%v", recorded, err)
	}
	if n := f.count(t, `SELECT count(*) FROM scanner_internal_cursors WHERE chain_id=$1 AND cursor_height=964`, chain); n != 1 {
		t.Fatal("the lane cursor did not follow the commit")
	}
	if _, err := store.CommitInternal(f.ctx, scanner.InternalCommit{ChainID: chain, Shard: shard, From: 965, To: 970, Version: current(), Events: []domain.TransferEvent{event}}); !errors.Is(err, domain.ErrInvariantViolation) {
		t.Fatalf("a transfer outside the range was accepted: %v", err)
	}
	if n := f.count(t, `SELECT count(*) FROM scanner_cursors WHERE chain_id=$1 AND cursor_height=1000`, chain); n != 1 {
		t.Fatal("the lane touched the ordinary cursor")
	}

	// A range ahead of the ordinary scan cannot be committed.
	if _, err := store.CommitInternal(f.ctx, scanner.InternalCommit{ChainID: chain, Shard: shard, From: 965, To: 1001, Version: current()}); !errors.Is(err, domain.ErrVersionConflict) {
		t.Fatalf("a range ahead of the ordinary scan was accepted: %v", err)
	}
	// A block the scan replaced gives no transfer, by its hash or by its height.
	faultExec(t, f.ctx, f.admin, `INSERT INTO chain_blocks(chain_id,height,block_hash,block_time,canonical_status,first_observed_at,last_observed_at) VALUES($1,966,'0xorphan',$2,'reorged',$2,$2),($1,967,'0xcanonical',$2,'safe',$2,$2)`, chain, now)
	for height, hash := range map[uint64]string{966: "0xorphan", 967: "0xother"} {
		orphaned := event
		orphaned.ID, orphaned.Identity.TransactionID, orphaned.BlockHeight, orphaned.BlockHash = faultID(t), "0xorphaned"+hash, height, hash
		if _, err := store.CommitInternal(f.ctx, scanner.InternalCommit{ChainID: chain, Shard: shard, From: 965, To: 970, Version: current(), Events: []domain.TransferEvent{orphaned}}); !errors.Is(err, domain.ErrStateConflict) {
			t.Fatalf("a transfer from a replaced block was accepted: %v", err)
		}
	}
	if n := f.count(t, `SELECT count(*) FROM scanner_internal_cursors WHERE chain_id=$1 AND cursor_height=964`, chain); n != 1 {
		t.Fatal("a refused commit moved the lane cursor")
	}
	// Settlement refuses a replaced block even for a transfer that got past the queue.
	orphaned := event
	orphaned.ID, orphaned.Identity.TransactionID, orphaned.BlockHeight, orphaned.BlockHash = faultID(t), "0xlate", 966, "0xorphan"
	orphaned.EvidenceHash, orphaned.ParserVersion, orphaned.OnChainTime = strings.Repeat("a", 64), "evm-v1", now
	err = pgx.BeginFunc(f.ctx, f.admin, func(tx pgx.Tx) error {
		_, _, _, err := insertCanonicalTransfer(f.ctx, tx, orphaned)
		return err
	})
	if !errors.Is(err, domain.ErrStateConflict) {
		t.Fatalf("settlement accepted a replaced block: %v", err)
	}

	// A reorg rewinds the lane with the scan and takes the queue with it.
	const owner = "internal-lane-reorg"
	faultExec(t, f.ctx, f.admin, `UPDATE scanner_cursors SET cursor_hash='0xtip' WHERE chain_id=$1`, chain)
	faultExec(t, f.ctx, f.admin, `INSERT INTO chain_blocks(chain_id,height,block_hash,block_time,canonical_status,first_observed_at,last_observed_at) VALUES($1,900,'0xancestor',$2,'safe',$2,$2),($1,1000,'0xtip',$2,'safe',$2,$2)`, chain, now)
	queued := event
	queued.ID, queued.Identity.TransactionID, queued.BlockHeight, queued.BlockHash = faultID(t), "0xqueued", 1000, "0xtip"
	if _, err := store.CommitInternal(f.ctx, scanner.InternalCommit{ChainID: chain, Shard: shard, From: 965, To: 1000, Version: current(), Events: []domain.TransferEvent{queued}}); err != nil {
		t.Fatal(err)
	}
	lease, err := store.Acquire(f.ctx, chain, shard, owner, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	batch := scanner.RangeBatch{From: 900, To: 1000, Blocks: []scanner.Block{{Height: 900, Hash: "0xancestor", Time: now}, {Height: 1000, Hash: "0xnewtip", Time: now}}}
	if err := store.RewindReorg(f.ctx, lease, batch, scanner.ReorgError{Height: 1000, CommittedHash: "0xtip", NewHash: "0xnewtip"}); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT count(*) FROM scanner_transfer_queue WHERE event_id=$1 AND status='reorged'`, queued.ID); n != 1 {
		t.Fatal("a queued transfer from a replaced block stayed claimable")
	}
	if n := f.count(t, `SELECT count(*) FROM scanner_internal_cursors WHERE chain_id=$1 AND cursor_height=900`, chain); n != 1 {
		t.Fatal("the lane stayed ahead of a rewound scan and would skip the new blocks")
	}

	// A lane sitting below the fork keeps its height, and a read from before the
	// reorg must still lose: the scan may be past the range again by then.
	faultExec(t, f.ctx, f.admin, `UPDATE scanner_cursors SET cursor_height=1000,cursor_hash='0xtip2',locked_by=NULL,locked_until=NULL WHERE chain_id=$1`, chain)
	faultExec(t, f.ctx, f.admin, `INSERT INTO chain_blocks(chain_id,height,block_hash,block_time,canonical_status,first_observed_at,last_observed_at) VALUES($1,950,'0xfork',$2,'safe',$2,$2),($1,1000,'0xtip2',$2,'safe',$2,$2) ON CONFLICT DO NOTHING`, chain, now)
	stale := scanner.InternalCommit{ChainID: chain, Shard: shard, From: 901, To: 932, Version: current()}
	lease, err = store.Acquire(f.ctx, chain, shard, owner, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	batch = scanner.RangeBatch{From: 950, To: 1000, Blocks: []scanner.Block{{Height: 950, Hash: "0xfork", Time: now}, {Height: 1000, Hash: "0xnewtip2", Time: now}}}
	if err := store.RewindReorg(f.ctx, lease, batch, scanner.ReorgError{Height: 1000, CommittedHash: "0xtip2", NewHash: "0xnewtip2"}); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT count(*) FROM scanner_internal_cursors WHERE chain_id=$1 AND cursor_height=900`, chain); n != 1 {
		t.Fatal("a lane below the fork must keep its height")
	}
	faultExec(t, f.ctx, f.admin, `UPDATE scanner_cursors SET cursor_height=1000 WHERE chain_id=$1`, chain)
	if _, err := store.CommitInternal(f.ctx, stale); !errors.Is(err, domain.ErrVersionConflict) {
		t.Fatalf("a read from before the reorg committed and would skip the replacement blocks: %v", err)
	}

	// One replica holds the lane until its lease runs out.
	claimed, err := store.ClaimInternal(f.ctx, chain, shard, "replica-a", time.Minute)
	if err != nil || !claimed {
		t.Fatalf("claimed=%v err=%v", claimed, err)
	}
	if claimed, err = store.ClaimInternal(f.ctx, chain, shard, "replica-b", time.Minute); err != nil || claimed {
		t.Fatalf("a second replica took a live lease: %v %v", claimed, err)
	}
	if claimed, err = store.ClaimInternal(f.ctx, chain, shard, "replica-a", time.Minute); err != nil || !claimed {
		t.Fatalf("the owner lost its own lease: %v %v", claimed, err)
	}
	faultExec(t, f.ctx, f.admin, `UPDATE scanner_internal_cursors SET locked_until=clock_timestamp()-interval '1 second' WHERE chain_id=$1`, chain)
	if claimed, err = store.ClaimInternal(f.ctx, chain, shard, "replica-b", time.Minute); err != nil || !claimed {
		t.Fatalf("an expired lease was not taken over: %v %v", claimed, err)
	}
	progress, err = store.InternalProgress(f.ctx, chain, shard)
	if err != nil || progress.OpenFindings != 2 {
		t.Fatalf("open findings %d: %v", progress.OpenFindings, err)
	}
}
