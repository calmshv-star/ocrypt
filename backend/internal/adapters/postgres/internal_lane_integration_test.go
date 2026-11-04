package postgres

import (
	"errors"
	"testing"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/money"
	"github.com/calmshv-star/ocrypt/backend/internal/scanner"
)

// internalLane proves the lane's storage under the production scanner role: its
// cursor follows the ordinary one, events reach the shared queue once, a replay
// changes nothing, and findings are recorded once.
func (f *faultDatabase) internalLane(t *testing.T) {
	const chain, shard = "eip155:990099", "default"
	now := time.Now().UTC()
	faultExec(t, f.ctx, f.admin, `INSERT INTO chains(id,family,network_name,status,required_confirmations,maximum_reorg_depth,created_at,updated_at) VALUES($1,'evm','Internal lane chain','active',12,64,$2,$2)`, chain, now)
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
		Identity:      domain.EventIdentity{ChainID: chain, TransactionID: "0xtx", EventIndex: "trace:0,11", AssetID: "eth", ToAddress: "0xwallet"},
		Kind:          "native_internal",
		Amount:        money.MustParse("5"),
		AssetDecimals: 18,
		BlockHeight:   905,
		BlockHash:     "0xblock",
		Status:        domain.TransferFinalized,
	}
	inflow := scanner.UnattributedInflow{Address: "0xwallet", FromHeight: 901, ToHeight: 932, Amount: "7", Reason: scanner.UnattributedBalance}
	commit := scanner.InternalCommit{ChainID: chain, Shard: shard, From: 901, To: 932, Events: []domain.TransferEvent{event}, Unattributed: []scanner.UnattributedInflow{inflow}}
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
	faultExec(t, f.ctx, f.admin, `UPDATE scanner_internal_cursors SET cursor_height=900 WHERE chain_id=$1`, chain)
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
	recorded, err = store.CommitInternal(f.ctx, scanner.InternalCommit{ChainID: chain, Shard: shard, From: 933, To: 964, Unattributed: []scanner.UnattributedInflow{{FromHeight: 933, ToHeight: 964, Reason: scanner.UnattributedGaveUp}}})
	if err != nil || recorded != 1 {
		t.Fatalf("recorded=%d err=%v", recorded, err)
	}
	if n := f.count(t, `SELECT count(*) FROM scanner_internal_cursors WHERE chain_id=$1 AND cursor_height=964`, chain); n != 1 {
		t.Fatal("the lane cursor did not follow the commit")
	}
	if _, err := store.CommitInternal(f.ctx, scanner.InternalCommit{ChainID: chain, Shard: shard, From: 965, To: 970, Events: []domain.TransferEvent{event}}); !errors.Is(err, domain.ErrInvariantViolation) {
		t.Fatalf("a transfer outside the range was accepted: %v", err)
	}
	if n := f.count(t, `SELECT count(*) FROM scanner_cursors WHERE chain_id=$1 AND cursor_height=1000`, chain); n != 1 {
		t.Fatal("the lane touched the ordinary cursor")
	}
}
