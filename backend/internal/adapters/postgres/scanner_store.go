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
			identityKey, err := event.Identity.Key()
			if err != nil {
				return err
			}
			payload, err := json.Marshal(event)
			if err != nil {
				return err
			}
			_, err = tx.Exec(ctx, `INSERT INTO scanner_transfer_queue (event_id,chain_id,identity_key,canonical_event,status,attempt_count,next_attempt_at,created_at,updated_at) VALUES ($1,$2,$3,$4::jsonb,'pending',0,clock_timestamp(),clock_timestamp(),clock_timestamp()) ON CONFLICT (chain_id,identity_key) DO UPDATE SET canonical_event=EXCLUDED.canonical_event,status='pending',attempt_count=0,next_attempt_at=clock_timestamp(),locked_by=NULL,locked_until=NULL,last_error=NULL,updated_at=clock_timestamp() WHERE scanner_transfer_queue.status='reorged'`, event.ID, lease.ChainID, identityKey, payload)
			if err != nil {
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
	Event   domain.TransferEvent
	Attempt int
}

func (s *ScannerStore) ClaimTransfers(ctx context.Context, worker string, now time.Time, lease time.Duration, limit int) (claimed []ClaimedTransfer, err error) {
	if worker == "" || lease < time.Second || limit < 1 || limit > 500 {
		return nil, errors.New("invalid transfer claim")
	}
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT event_id::text,canonical_event::text,attempt_count+1 FROM scanner_transfer_queue WHERE status IN ('pending','retry') AND next_attempt_at<=$1 AND (locked_until IS NULL OR locked_until<$1) ORDER BY next_attempt_at,event_id LIMIT $2 FOR UPDATE SKIP LOCKED`, now.UTC(), limit)
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
			command, err := tx.Exec(ctx, `UPDATE scanner_transfer_queue SET status='leased',locked_by=$1,locked_until=$2,attempt_count=attempt_count+1,updated_at=$3 WHERE event_id=$4 AND status IN ('pending','retry')`, worker, now.Add(lease), now, item.id)
			if err != nil {
				return err
			}
			if command.RowsAffected() != 1 {
				return domain.ErrVersionConflict
			}
			claimed = append(claimed, ClaimedTransfer{Event: event, Attempt: item.attempt})
		}
		return nil
	})
	return claimed, err
}

func (s *ScannerStore) CompleteTransfer(ctx context.Context, worker, eventID string) error {
	command, err := s.pool.Exec(ctx, `UPDATE scanner_transfer_queue SET status='completed',locked_by=NULL,locked_until=NULL,last_error=NULL,updated_at=clock_timestamp() WHERE event_id=$1 AND status='leased' AND locked_by=$2`, eventID, worker)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return domain.ErrVersionConflict
	}
	return nil
}

func (s *ScannerStore) RetryTransfer(ctx context.Context, worker, eventID, reason string, next time.Time, deadLetter bool) error {
	status := "retry"
	if deadLetter {
		status = "dead_letter"
	}
	command, err := s.pool.Exec(ctx, `UPDATE scanner_transfer_queue SET status=$1,locked_by=NULL,locked_until=NULL,last_error=$2,next_attempt_at=$3,updated_at=clock_timestamp() WHERE event_id=$4 AND status='leased' AND locked_by=$5`, status, reason, next.UTC(), eventID, worker)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return domain.ErrVersionConflict
	}
	return nil
}

var _ scanner.CursorStore = (*ScannerStore)(nil)
