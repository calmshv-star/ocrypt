BEGIN;

DROP TABLE IF EXISTS payment_receipt_evidence;
DROP FUNCTION IF EXISTS payment_receipt_evidence_immutable();
ALTER TABLE payment_proofs DROP CONSTRAINT IF EXISTS payment_proofs_id_tenant_unique;

COMMIT;
