BEGIN;

DO $roles$
BEGIN
  IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='merchant_scanner_worker') THEN
    REVOKE SELECT(id,tenant_id,environment) ON merchants FROM merchant_scanner_worker;
    REVOKE SELECT(event_id,status,version) ON unmatched_payments FROM merchant_scanner_worker;
  END IF;
  IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='merchant_settlement_worker') THEN
    REVOKE SELECT(chain_id,height,block_hash,canonical_status) ON chain_blocks FROM merchant_settlement_worker;
  END IF;
  IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='merchant_proof_worker') THEN
    REVOKE SELECT(chain_id,height,block_hash,canonical_status) ON chain_blocks FROM merchant_proof_worker;
  END IF;
END $roles$;

DROP INDEX scanner_transfer_claim_idx;
CREATE INDEX scanner_transfer_claim_idx ON scanner_transfer_queue(next_attempt_at,event_id)
  WHERE status IN ('pending','retry');
DROP INDEX callback_claim_idx;
CREATE INDEX callback_claim_idx ON callback_deliveries(next_attempt_at,id)
  WHERE status IN ('pending','retry');
ALTER TABLE scanner_transfer_queue DROP COLUMN lease_token;

COMMIT;
