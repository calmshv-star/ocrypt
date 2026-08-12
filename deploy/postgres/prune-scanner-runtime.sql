\set ON_ERROR_STOP on
BEGIN;

UPDATE scanner_gaps gap
SET status='healed',healed_at=COALESCE(healed_at,clock_timestamp()),last_seen_at=clock_timestamp()
WHERE gap.status='open'
  AND EXISTS (
    SELECT 1
    FROM scanner_cursors scanner_cursor
    WHERE scanner_cursor.chain_id=gap.chain_id
      AND scanner_cursor.cursor_height>=gap.to_height
  );

DELETE FROM scanner_gaps gap
USING (
  SELECT chain_id,max(cursor_height::numeric) AS cursor_height
  FROM scanner_cursors
  GROUP BY chain_id
) scanner_cursor
WHERE gap.chain_id=scanner_cursor.chain_id
  AND gap.status='healed'
  AND gap.to_height::numeric<scanner_cursor.cursor_height-512;

-- Old scanner revisions ingested every TRON recipient. Remove only events
-- whose destination has never belonged to Ocrypt and which have no financial,
-- operational, migration, observation, or manual-resolution reference.
CREATE TEMP TABLE prune_irrelevant_transfer_ids(id uuid PRIMARY KEY) ON COMMIT DROP;
INSERT INTO prune_irrelevant_transfer_ids(id)
SELECT te.id
FROM transfer_events te
WHERE NOT EXISTS (
        SELECT 1 FROM addresses a
        WHERE a.chain_id=te.chain_id AND a.canonical_address=te.to_address
      )
  AND NOT EXISTS (SELECT 1 FROM payment_matches pm WHERE pm.event_id=te.id)
  AND NOT EXISTS (SELECT 1 FROM payment_observations po WHERE po.transfer_event_id=te.id)
  AND NOT EXISTS (SELECT 1 FROM event_observations eo WHERE eo.event_id=te.id)
  AND NOT EXISTS (SELECT 1 FROM financial_refund_settlements fr WHERE fr.chain_event_id=te.id)
  AND NOT EXISTS (SELECT 1 FROM manual_resolutions mr WHERE mr.event_id=te.id)
  AND NOT EXISTS (SELECT 1 FROM migration_event_ownership mo WHERE mo.platform_event_id=te.id)
  AND NOT EXISTS (
        SELECT 1
        FROM unmatched_payments u
        WHERE u.event_id=te.id
          AND (EXISTS (SELECT 1 FROM match_candidates mc WHERE mc.unmatched_id=u.id)
            OR EXISTS (SELECT 1 FROM manual_resolutions mr WHERE mr.unmatched_id=u.id)
            OR EXISTS (SELECT 1 FROM ai_rank_suggestions ai WHERE ai.unmatched_id=u.id))
      );

DELETE FROM unmatched_payments WHERE event_id IN (SELECT id FROM prune_irrelevant_transfer_ids);
DELETE FROM scanner_transfer_queue WHERE event_id IN (SELECT id FROM prune_irrelevant_transfer_ids);
DELETE FROM transfer_events WHERE id IN (SELECT id FROM prune_irrelevant_transfer_ids);

-- Queue JSON is only transient transport state; transfer_events and ledger
-- tables remain the authoritative evidence after successful processing.
DELETE FROM scanner_transfer_queue WHERE status='completed';

-- Keep a bounded reorg window per active chain instead of a local header copy
-- of the blockchain. Payment transfer facts are intentionally unaffected.
WITH active_heads AS (
  SELECT chain_id,max(cursor_height) AS height
  FROM scanner_cursors
  WHERE updated_at>clock_timestamp()-interval '1 day'
  GROUP BY chain_id
)
DELETE FROM chain_blocks block
USING active_heads head
WHERE block.chain_id=head.chain_id
  AND head.height>512
  AND block.height<head.height-512;

COMMIT;
ANALYZE chain_blocks;
ANALYZE scanner_gaps;
ANALYZE scanner_transfer_queue;
ANALYZE transfer_events;
