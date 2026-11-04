package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/ids"
	"github.com/calmshv-star/ocrypt/backend/internal/scanner"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ScannerStore persists the quorum-safe cursor, every covered block, gaps, and
// a replayable normalized-event queue. Chain scanners use a dedicated database
// role; merchant API roles must not be granted access to these tables.
type ScannerStore struct {
	pool       *pgxpool.Pool
	capability string
}

const scannerBlockHistory = uint64(512)

func NewScannerStore(pool *pgxpool.Pool, capability string) (*ScannerStore, error) {
	if pool == nil || capability == "" {
		return nil, errors.New("scanner store requires PostgreSQL and a capability")
	}
	return &ScannerStore{pool: pool, capability: capability}, nil
}

func (s *ScannerStore) Acquire(ctx context.Context, chainID, shard, owner string, duration time.Duration) (lease scanner.Lease, err error) {
	if chainID == "" || shard == "" || owner == "" || duration < time.Second || duration > 10*time.Minute {
		return lease, errors.New("invalid scanner lease request")
	}
	now := time.Now().UTC()
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO scanner_cursors (chain_id,scanner_shard,capability,cursor_height,cursor_hash,version,updated_at) VALUES ($1,$2,$3,0,'',1,$4) ON CONFLICT DO NOTHING`, chainID, shard, s.capability, now)
		if err != nil {
			return err
		}
		var height string
		err = tx.QueryRow(ctx, `UPDATE scanner_cursors SET locked_by=$1,locked_until=$2,heartbeat_at=$3,version=version+1,updated_at=$3 WHERE chain_id=$4 AND scanner_shard=$5 AND capability=$6 AND (locked_until IS NULL OR locked_until<$3 OR locked_by=$1) RETURNING cursor_height::text,COALESCE(cursor_hash,''),version`, owner, now.Add(duration), now, chainID, shard, s.capability).Scan(&height, &lease.Hash, &lease.Version)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: scanner cursor is already leased", domain.ErrStateConflict)
		}
		if err != nil {
			return err
		}
		lease.Height, err = strconv.ParseUint(height, 10, 64)
		return err
	})
	lease.ChainID, lease.Shard, lease.Owner, lease.Until = chainID, shard, owner, now.Add(duration)
	return lease, err
}

// Renew extends the same fenced lease without changing its version. A slow
// provider range must not lose ownership before its canonical batch commits.
func (s *ScannerStore) Renew(ctx context.Context, lease scanner.Lease, duration time.Duration) error {
	if duration <= 0 {
		return errors.New("invalid scanner lease duration")
	}
	command, err := s.pool.Exec(ctx, `UPDATE scanner_cursors SET locked_until=clock_timestamp()+$1::double precision*interval '1 second',heartbeat_at=clock_timestamp() WHERE chain_id=$2 AND scanner_shard=$3 AND capability=$4 AND locked_by=$5 AND version=$6 AND locked_until>clock_timestamp()`, duration.Seconds(), lease.ChainID, lease.Shard, s.capability, lease.Owner, lease.Version)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return errors.New("scanner lease was lost")
	}
	return nil
}

func (s *ScannerStore) Commit(ctx context.Context, lease scanner.Lease, batch scanner.RangeBatch) error {
	if lease.ChainID == "" || lease.Shard == "" || lease.Owner == "" || len(batch.Blocks) == 0 || batch.To < batch.From || batch.Blocks[0].Height != batch.From || batch.Blocks[len(batch.Blocks)-1].Height != batch.To {
		return errors.New("invalid scanner commit")
	}
	blockHashes := make(map[uint64]string, len(batch.Blocks))
	for _, block := range batch.Blocks {
		blockHashes[block.Height] = block.Hash
	}
	for _, event := range batch.Events {
		if event.Identity.ChainID != lease.ChainID || blockHashes[event.BlockHeight] != event.BlockHash {
			return fmt.Errorf("%w: staged transfer is not bound to the committed block range", domain.ErrInvariantViolation)
		}
	}
	for _, evidence := range batch.RuntimeEvidence {
		if evidence.Kind == "" || evidence.LogicalKey == "" || !ids.Valid(evidence.SnapshotID) || len(evidence.PayloadHash) != 64 || evidence.Version < 1 || evidence.FenceToken < 1 {
			return fmt.Errorf("%w: invalid scanner runtime evidence", domain.ErrInvariantViolation)
		}
	}
	commit := func() error {
		return pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx pgx.Tx) error {
			for _, block := range batch.Blocks {
				_, err := tx.Exec(ctx, `UPDATE chain_blocks SET canonical_status='reorged',last_observed_at=clock_timestamp() WHERE chain_id=$1 AND height=$2::numeric AND block_hash<>$3 AND canonical_status<>'reorged'`, lease.ChainID, strconv.FormatUint(block.Height, 10), block.Hash)
				if err != nil {
					return err
				}
				_, err = tx.Exec(ctx, `INSERT INTO chain_blocks (chain_id,height,block_hash,parent_hash,block_time,canonical_status,first_observed_at,last_observed_at) VALUES ($1,$2::numeric,$3,NULLIF($4,''),$5,'safe',clock_timestamp(),clock_timestamp()) ON CONFLICT (chain_id,block_hash) DO UPDATE SET last_observed_at=clock_timestamp(),canonical_status=CASE WHEN chain_blocks.canonical_status='finalized' THEN 'finalized' ELSE 'safe' END`, lease.ChainID, strconv.FormatUint(block.Height, 10), block.Hash, block.ParentHash, block.Time.UTC())
				if err != nil {
					return err
				}
			}
			for _, event := range batch.Events {
				if event.Identity.ChainID != lease.ChainID {
					return fmt.Errorf("%w: queued transfer belongs to another chain", domain.ErrInvariantViolation)
				}
				if err := enqueueTransfer(ctx, tx, lease.ChainID, event); err != nil {
					return err
				}
			}
			last := batch.Blocks[len(batch.Blocks)-1]
			command, err := tx.Exec(ctx, `UPDATE scanner_cursors SET cursor_height=$1::numeric,cursor_hash=$2,locked_by=NULL,locked_until=NULL,heartbeat_at=clock_timestamp(),version=version+1,updated_at=clock_timestamp() WHERE chain_id=$3 AND scanner_shard=$4 AND capability=$5 AND locked_by=$6 AND version=$7 AND locked_until>clock_timestamp()`, strconv.FormatUint(batch.To, 10), last.Hash, lease.ChainID, lease.Shard, s.capability, lease.Owner, lease.Version)
			if err != nil {
				return err
			}
			if command.RowsAffected() != 1 {
				return fmt.Errorf("%w: scanner lease was lost", domain.ErrVersionConflict)
			}
			if batch.To > scannerBlockHistory {
				cutoff := batch.To - scannerBlockHistory
				if _, err := tx.Exec(ctx, `DELETE FROM chain_blocks WHERE chain_id=$1 AND height<$2::numeric`, lease.ChainID, strconv.FormatUint(cutoff, 10)); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `DELETE FROM scanner_gaps WHERE chain_id=$1 AND status='healed' AND to_height<$2::numeric`, lease.ChainID, strconv.FormatUint(cutoff, 10)); err != nil {
					return err
				}
			}
			if _, err := tx.Exec(ctx, `UPDATE scanner_gaps SET status='healed',healed_at=clock_timestamp(),last_seen_at=clock_timestamp() WHERE chain_id=$1 AND status='open' AND to_height<=$2::numeric`, lease.ChainID, strconv.FormatUint(batch.To, 10)); err != nil {
				return err
			}
			if len(batch.RuntimeEvidence) > 0 {
				evidenceID, err := ids.New()
				if err != nil {
					return err
				}
				encoded, err := json.Marshal(batch.RuntimeEvidence)
				if err != nil {
					return err
				}
				_, err = tx.Exec(ctx, `INSERT INTO scanner_runtime_config_evidence(id,chain_id,scanner_shard,capability,from_height,to_height,config_evidence,evidence_hash,committed_at) VALUES($1,$2,$3,$4,$5::numeric,$6::numeric,$7::jsonb,digest(($7::jsonb)::text,'sha256'),clock_timestamp())`, evidenceID, lease.ChainID, lease.Shard, s.capability, strconv.FormatUint(batch.From, 10), strconv.FormatUint(batch.To, 10), encoded)
				if err != nil {
					return err
				}
			}
			return nil
		})
	}
	for attempt := 0; attempt < 3; attempt++ {
		err := commit()
		if !isSerializationFailure(err) || attempt == 2 {
			return err
		}
		timer := time.NewTimer(time.Duration(5*(1<<attempt)) * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return errors.New("unreachable scanner transaction retry state")
}

func enqueueTransfer(ctx context.Context, tx pgx.Tx, chainID string, event domain.TransferEvent) error {
	identityKey, err := event.Identity.Key()
	if err != nil {
		return err
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO scanner_transfer_queue (event_id,chain_id,identity_key,canonical_event,status,attempt_count,next_attempt_at,created_at,updated_at) VALUES ($1,$2,$3,$4::jsonb,'pending',0,clock_timestamp(),clock_timestamp(),clock_timestamp()) ON CONFLICT (chain_id,identity_key) DO UPDATE SET canonical_event=EXCLUDED.canonical_event,status='pending',attempt_count=0,next_attempt_at=clock_timestamp(),locked_by=NULL,locked_until=NULL,lease_token=NULL,last_error=NULL,updated_at=clock_timestamp() WHERE scanner_transfer_queue.status='reorged'`, event.ID, chainID, identityKey, payload)
	return err
}

// InternalProgress reads the internal lane's cursor and the ordinary cursor it
// follows. The lane never reads ahead of a range the ordinary scan committed.
func (s *ScannerStore) InternalProgress(ctx context.Context, chainID, shard string) (scanner.InternalProgress, error) {
	var progress scanner.InternalProgress
	var scanned, cursor *string
	err := s.pool.QueryRow(ctx, `SELECT (SELECT cursor_height::text FROM scanner_cursors WHERE chain_id=$1 AND scanner_shard=$2 AND capability=$3 AND cursor_hash IS NOT NULL),
(SELECT cursor_height::text FROM scanner_internal_cursors WHERE chain_id=$1 AND scanner_shard=$2)`, chainID, shard, s.capability).Scan(&scanned, &cursor)
	if err != nil {
		return progress, err
	}
	if scanned != nil {
		if progress.Scanned, err = strconv.ParseUint(*scanned, 10, 64); err != nil {
			return progress, err
		}
		progress.HasScanned = true
	}
	if cursor != nil {
		if progress.Cursor, err = strconv.ParseUint(*cursor, 10, 64); err != nil {
			return progress, err
		}
		progress.Started = true
	}
	return progress, nil
}

func (s *ScannerStore) StartInternal(ctx context.Context, chainID, shard string, height uint64) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO scanner_internal_cursors(chain_id,scanner_shard,cursor_height,updated_at) VALUES($1,$2,$3::numeric,clock_timestamp()) ON CONFLICT DO NOTHING`, chainID, shard, strconv.FormatUint(height, 10))
	return err
}

// CommitInternal moves the lane's cursor together with what the range found, and
// returns how many records of uncredited money are new. Events reach the
// queue that scanned transfers use, so they settle the same way, and a replayed
// range changes nothing.
func (s *ScannerStore) CommitInternal(ctx context.Context, commit scanner.InternalCommit) (int, error) {
	if commit.ChainID == "" || commit.Shard == "" || commit.From == 0 || commit.To < commit.From {
		return 0, errors.New("invalid internal lane commit")
	}
	for _, event := range commit.Events {
		if event.Identity.ChainID != commit.ChainID || event.BlockHeight < commit.From || event.BlockHeight > commit.To {
			return 0, fmt.Errorf("%w: internal transfer is not bound to the committed range", domain.ErrInvariantViolation)
		}
	}
	recorded := 0
	write := func() error {
		recorded = 0
		return pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx pgx.Tx) error {
			command, err := tx.Exec(ctx, `UPDATE scanner_internal_cursors SET cursor_height=$1::numeric,updated_at=clock_timestamp() WHERE chain_id=$2 AND scanner_shard=$3 AND cursor_height=$4::numeric`, strconv.FormatUint(commit.To, 10), commit.ChainID, commit.Shard, strconv.FormatUint(commit.From-1, 10))
			if err != nil {
				return err
			}
			if command.RowsAffected() != 1 {
				return fmt.Errorf("%w: internal lane cursor moved", domain.ErrVersionConflict)
			}
			// The ordinary cursor is the only proof a range is still canonical: a
			// reorg may have rewound it while the lane was reading.
			var scanned bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM scanner_cursors WHERE chain_id=$1 AND scanner_shard=$2 AND capability=$3 AND cursor_height>=$4::numeric)`, commit.ChainID, commit.Shard, s.capability, strconv.FormatUint(commit.To, 10)).Scan(&scanned); err != nil {
				return err
			}
			if !scanned {
				return fmt.Errorf("%w: internal lane range is ahead of the ordinary scan", domain.ErrVersionConflict)
			}
			for _, event := range commit.Events {
				// History older than the scanner keeps has no rows to contradict it.
				var replaced bool
				err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM chain_blocks WHERE chain_id=$1 AND ((block_hash=$2 AND canonical_status='reorged') OR (height=$3::numeric AND block_hash<>$2 AND canonical_status<>'reorged')))`, commit.ChainID, event.BlockHash, strconv.FormatUint(event.BlockHeight, 10)).Scan(&replaced)
				if err != nil {
					return err
				}
				if replaced {
					return fmt.Errorf("%w: internal transfer belongs to a replaced block", domain.ErrStateConflict)
				}
				if err := enqueueTransfer(ctx, tx, commit.ChainID, event); err != nil {
					return err
				}
			}
			for _, inflow := range commit.Unattributed {
				id, err := ids.New()
				if err != nil {
					return err
				}
				var created bool
				err = tx.QueryRow(ctx, `INSERT INTO scanner_unattributed_inflows(id,chain_id,address,transaction_id,from_height,to_height,amount_atomic,reason,status,first_seen_at,last_seen_at)
VALUES($1,$2,$3,$4,$5::numeric,$6::numeric,NULLIF($7,'')::numeric,$8,'open',clock_timestamp(),clock_timestamp())
ON CONFLICT (chain_id,reason,address,transaction_id,from_height,to_height) DO UPDATE SET last_seen_at=clock_timestamp() RETURNING xmax=0`,
					id, commit.ChainID, inflow.Address, inflow.TransactionID, strconv.FormatUint(inflow.FromHeight, 10), strconv.FormatUint(inflow.ToHeight, 10), inflow.Amount, inflow.Reason).Scan(&created)
				if err != nil {
					return err
				}
				if created {
					recorded++
				}
			}
			return nil
		})
	}
	for attempt := 0; attempt < 3; attempt++ {
		err := write()
		if !isSerializationFailure(err) || attempt == 2 {
			return recorded, err
		}
		timer := time.NewTimer(time.Duration(5*(1<<attempt)) * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return 0, ctx.Err()
		case <-timer.C:
		}
	}
	return 0, errors.New("unreachable internal lane transaction retry state")
}

func (s *ScannerStore) Release(ctx context.Context, lease scanner.Lease) error {
	command, err := s.pool.Exec(ctx, `UPDATE scanner_cursors SET locked_by=NULL,locked_until=NULL,updated_at=clock_timestamp(),version=version+1 WHERE chain_id=$1 AND scanner_shard=$2 AND capability=$3 AND locked_by=$4 AND version=$5`, lease.ChainID, lease.Shard, s.capability, lease.Owner, lease.Version)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return fmt.Errorf("%w: scanner lease was lost", domain.ErrVersionConflict)
	}
	return nil
}

func (s *ScannerStore) RecordGap(ctx context.Context, chainID string, from, to uint64, reason string) error {
	if chainID == "" || reason == "" || to < from {
		return errors.New("invalid scanner gap")
	}
	command, err := s.pool.Exec(ctx, `UPDATE scanner_gaps SET to_height=GREATEST(to_height,$3::numeric),occurrence_count=occurrence_count+1,last_seen_at=clock_timestamp() WHERE chain_id=$1 AND from_height=$2::numeric AND reason=$4 AND status='open'`, chainID, strconv.FormatUint(from, 10), strconv.FormatUint(to, 10), reason)
	if err != nil {
		return err
	}
	if command.RowsAffected() > 0 {
		return nil
	}
	id, err := ids.New()
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO scanner_gaps (id,chain_id,from_height,to_height,reason,status,occurrence_count,first_seen_at,last_seen_at) VALUES ($1,$2,$3::numeric,$4::numeric,$5,'open',1,clock_timestamp(),clock_timestamp()) ON CONFLICT (chain_id,from_height,to_height) WHERE status='open' DO UPDATE SET reason=EXCLUDED.reason,occurrence_count=scanner_gaps.occurrence_count+1,last_seen_at=clock_timestamp()`, id, chainID, strconv.FormatUint(from, 10), strconv.FormatUint(to, 10), reason)
	return err
}

func (s *ScannerStore) HealGap(ctx context.Context, chainID string, from, to uint64) error {
	command, err := s.pool.Exec(ctx, `UPDATE scanner_gaps SET status='healed',healed_at=clock_timestamp(),last_seen_at=clock_timestamp() WHERE chain_id=$1 AND from_height=$2::numeric AND to_height=$3::numeric AND status='open'`, chainID, strconv.FormatUint(from, 10), strconv.FormatUint(to, 10))
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return domain.ErrNotFound
	}
	return nil
}

type ClaimedTransfer struct {
	Event      domain.TransferEvent
	Attempt    int
	ClaimToken string
}

func (s *ScannerStore) ClaimTransfers(ctx context.Context, worker string, now time.Time, lease time.Duration, limit int) (claimed []ClaimedTransfer, err error) {
	if worker == "" || lease < time.Second || limit < 1 || limit > 500 {
		return nil, errors.New("invalid transfer claim")
	}
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT event_id::text,canonical_event::text,attempt_count+1 FROM scanner_transfer_queue WHERE (status IN ('pending','retry') AND next_attempt_at<=$1 AND (locked_until IS NULL OR locked_until<=$1)) OR (status='leased' AND locked_until<=$1) ORDER BY next_attempt_at,event_id LIMIT $2 FOR UPDATE SKIP LOCKED`, now.UTC(), limit)
		if err != nil {
			return err
		}
		type row struct {
			id, payload string
			attempt     int
		}
		var selected []row
		for rows.Next() {
			var item row
			if err := rows.Scan(&item.id, &item.payload, &item.attempt); err != nil {
				rows.Close()
				return err
			}
			selected = append(selected, item)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		for _, item := range selected {
			var event domain.TransferEvent
			if err := json.Unmarshal([]byte(item.payload), &event); err != nil {
				return fmt.Errorf("decode staged transfer %s: %w", item.id, err)
			}
			claimToken, err := ids.New()
			if err != nil {
				return err
			}
			command, err := tx.Exec(ctx, `UPDATE scanner_transfer_queue SET status='leased',locked_by=$1,locked_until=$2,attempt_count=attempt_count+1,updated_at=$3,lease_token=$5 WHERE event_id=$4 AND ((status IN ('pending','retry') AND next_attempt_at<=$3 AND (locked_until IS NULL OR locked_until<=$3)) OR (status='leased' AND locked_until<=$3))`, worker, now.Add(lease), now, item.id, claimToken)
			if err != nil {
				return err
			}
			if command.RowsAffected() != 1 {
				return domain.ErrVersionConflict
			}
			claimed = append(claimed, ClaimedTransfer{Event: event, Attempt: item.attempt, ClaimToken: claimToken})
		}
		return nil
	})
	return claimed, err
}

func (s *ScannerStore) CompleteTransfer(ctx context.Context, worker, eventID, claimToken string) error {
	if claimToken == "" {
		return domain.ErrVersionConflict
	}
	// The queue is transport state, not financial evidence. The canonical
	// transfer and all settlement evidence live in their dedicated immutable
	// tables, so retaining a second JSON copy after acknowledgement only causes
	// unbounded storage growth.
	command, err := s.pool.Exec(ctx, `DELETE FROM scanner_transfer_queue WHERE event_id=$1 AND status='leased' AND locked_by=$2 AND lease_token=$3`, eventID, worker, claimToken)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return domain.ErrVersionConflict
	}
	return nil
}

func (s *ScannerStore) RetryTransfer(ctx context.Context, worker, eventID, claimToken, reason string, next time.Time, deadLetter bool) error {
	if claimToken == "" {
		return domain.ErrVersionConflict
	}
	status := "retry"
	if deadLetter {
		status = "dead_letter"
	}
	command, err := s.pool.Exec(ctx, `UPDATE scanner_transfer_queue SET status=$1,locked_by=NULL,locked_until=NULL,lease_token=NULL,last_error=$2,next_attempt_at=$3,updated_at=clock_timestamp() WHERE event_id=$4 AND status='leased' AND locked_by=$5 AND lease_token=$6`, status, reason, next.UTC(), eventID, worker, claimToken)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return domain.ErrVersionConflict
	}
	return nil
}

var _ scanner.CursorStore = (*ScannerStore)(nil)
