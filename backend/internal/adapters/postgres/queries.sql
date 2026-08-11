-- These statements define the concurrency-critical PostgreSQL operations used
-- by the application.Store implementation. They are kept SQL-first so sqlc can
-- generate typed pgx bindings in the production assembly.

-- name: ConsumeAuthenticationNonce :execrows
INSERT INTO auth_nonces (key_id, nonce, expires_at, created_at)
VALUES ($1, $2, $3, clock_timestamp())
ON CONFLICT (key_id, nonce) DO NOTHING;

-- name: FindIdempotencyRecord :one
SELECT request_hash, resource_type, resource_id, response_status, response_body
FROM idempotency_records
WHERE merchant_id = $1 AND operation = $2 AND idempotency_key = $3
FOR UPDATE;

-- name: InsertIdempotencyRecord :exec
INSERT INTO idempotency_records (
    tenant_id, merchant_id, operation, idempotency_key, request_hash,
    resource_type, resource_id, response_status, response_body, expires_at, created_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, clock_timestamp());

-- name: ReserveExactAmount :one
INSERT INTO amount_reservations (
    id, tenant_id, route_id, chain_id, receiving_address, asset_id,
    exact_amount_atomic, active_window, state, created_at, updated_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7,
    tstzrange($8, $9, '[)'), 'active', clock_timestamp(), clock_timestamp()
)
RETURNING id;

-- name: InsertTransferEvent :one
INSERT INTO transfer_events (
    id, chain_id, transaction_id, event_identity, event_kind, asset_id,
    from_address, to_address, amount_atomic, asset_decimals, block_hash,
    block_height, on_chain_time, status, parser_version, evidence_hash,
    created_at, updated_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13,
    $14, $15, $16, clock_timestamp(), clock_timestamp()
)
ON CONFLICT (chain_id, transaction_id, event_identity, asset_id, to_address)
DO UPDATE SET updated_at = transfer_events.updated_at
RETURNING id, (xmax = 0) AS inserted;

-- name: AdvanceScannerCursor :execrows
UPDATE scanner_cursors
SET cursor_height = $5, cursor_token = $6, cursor_hash = $7,
    heartbeat_at = clock_timestamp(), updated_at = clock_timestamp(), version = version + 1
WHERE chain_id = $1 AND scanner_shard = $2 AND capability = $3
  AND locked_by = $4 AND locked_until > clock_timestamp() AND version = $8;

-- name: ClaimOutboxBatch :many
WITH candidates AS (
    SELECT id
    FROM outbox_events
    WHERE published_at IS NULL
      AND available_at <= clock_timestamp()
      AND (locked_until IS NULL OR locked_until < clock_timestamp())
    ORDER BY available_at, id
    LIMIT $3
    FOR UPDATE SKIP LOCKED
)
UPDATE outbox_events AS event
SET locked_by = $1, locked_until = clock_timestamp() + $2::interval,
    attempt_count = attempt_count + 1
FROM candidates
WHERE event.id = candidates.id
RETURNING event.*;

-- name: ClaimCallbackBatch :many
WITH candidates AS (
    SELECT id
    FROM callback_deliveries
    WHERE status IN ('pending', 'retry')
      AND next_attempt_at <= clock_timestamp()
      AND (locked_until IS NULL OR locked_until < clock_timestamp())
    ORDER BY next_attempt_at, id
    LIMIT $3
    FOR UPDATE SKIP LOCKED
)
UPDATE callback_deliveries AS delivery
SET status = 'leased', locked_by = $1,
    locked_until = clock_timestamp() + $2::interval,
    attempt_count = attempt_count + 1, updated_at = clock_timestamp(),
    version = version + 1
FROM candidates
WHERE delivery.id = candidates.id
RETURNING delivery.*;

-- name: InsertConsumerInbox :execrows
INSERT INTO consumer_inbox (consumer_name, event_id, processed_at, result_hash)
VALUES ($1, $2, clock_timestamp(), $3)
ON CONFLICT (consumer_name, event_id) DO NOTHING;

-- Settlement is one database transaction: lock the intent and route, consume
-- the transfer event with payment_matches, post a balanced ledger transaction,
-- transition the intent, append callback_events, and append outbox_events.
-- Any failed statement rolls back the complete settlement.
