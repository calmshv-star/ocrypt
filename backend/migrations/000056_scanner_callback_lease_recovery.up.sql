BEGIN;

-- Retry counters reset on reorg reinclusion. A fresh claim token fences each
-- transport incarnation independently from worker identity and retry count.
ALTER TABLE scanner_transfer_queue ADD COLUMN lease_token uuid;

-- Expired leases must remain visible to the normal bounded queue claim query.
DROP INDEX scanner_transfer_claim_idx;
CREATE INDEX scanner_transfer_claim_idx ON scanner_transfer_queue(next_attempt_at,event_id)
  WHERE status IN ('pending','retry','leased');
DROP INDEX callback_claim_idx;
CREATE INDEX callback_claim_idx ON callback_deliveries(next_attempt_at,id)
  WHERE status IN ('pending','retry','leased');

-- Scanner compensation and settlement reinclusion need only these identity,
-- status and environment reads. Roles can follow the initial migration.
DO $roles$
BEGIN
  IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='merchant_scanner_worker') THEN
    GRANT SELECT(id,tenant_id,environment) ON merchants TO merchant_scanner_worker;
    GRANT SELECT(event_id,status,version) ON unmatched_payments TO merchant_scanner_worker;
  END IF;
  IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='merchant_settlement_worker') THEN
    GRANT SELECT(chain_id,height,block_hash,canonical_status) ON chain_blocks TO merchant_settlement_worker;
  END IF;
  IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='merchant_proof_worker') THEN
    GRANT SELECT(chain_id,height,block_hash,canonical_status) ON chain_blocks TO merchant_proof_worker;
  END IF;
END $roles$;

COMMIT;
