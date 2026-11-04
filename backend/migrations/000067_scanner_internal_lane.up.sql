-- Native transfers made through contracts are found on their own cursor, behind
-- the ordinary scan, so tracing can never hold up a normal payment.
BEGIN;

CREATE TABLE scanner_internal_cursors (
  chain_id text NOT NULL REFERENCES chains(id),
  scanner_shard text NOT NULL,
  cursor_height uint256 NOT NULL,
  -- Every write raises the version, so a range read before a reorg cannot commit
  -- after it, even when the reorg left the height where it was.
  version bigint NOT NULL DEFAULT 1,
  locked_by text,
  locked_until timestamptz,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (chain_id,scanner_shard)
);

-- Money or a transaction the lane could not credit, for an operator to look at.
-- One row per finding: a replayed range only refreshes last_seen_at.
CREATE TABLE scanner_unattributed_inflows (
  id uuid PRIMARY KEY,
  chain_id text NOT NULL REFERENCES chains(id),
  address text NOT NULL DEFAULT '',
  transaction_id text NOT NULL DEFAULT '',
  from_height uint256 NOT NULL,
  to_height uint256 NOT NULL CHECK (to_height>=from_height),
  amount_atomic uint256,
  reason text NOT NULL CHECK (reason IN ('balance_unexplained','call_tree_too_large','traces_too_large','trace_unavailable')),
  status text NOT NULL CHECK (status IN ('open','resolved')),
  first_seen_at timestamptz NOT NULL,
  last_seen_at timestamptz NOT NULL,
  UNIQUE (chain_id,reason,address,transaction_id,from_height,to_height)
);
CREATE INDEX scanner_unattributed_open_idx ON scanner_unattributed_inflows(chain_id,first_seen_at) WHERE status='open';

COMMIT;
