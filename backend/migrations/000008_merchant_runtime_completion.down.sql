BEGIN;

DROP POLICY IF EXISTS merchant_event_sequences_tenant_policy ON merchant_event_sequences;
DROP POLICY IF EXISTS reconciliation_reports_tenant_policy ON reconciliation_reports;
DROP POLICY IF EXISTS payment_intent_versions_tenant_policy ON payment_intent_versions;
DROP TABLE IF EXISTS merchant_event_sequences;
DROP TABLE IF EXISTS reconciliation_reports;
DROP TRIGGER IF EXISTS payment_intents_require_version_advance ON payment_intents;
DROP FUNCTION IF EXISTS require_payment_intent_version_advance();
DROP TRIGGER IF EXISTS payment_intents_capture_update ON payment_intents;
DROP TRIGGER IF EXISTS payment_intents_capture_insert ON payment_intents;
DROP FUNCTION IF EXISTS capture_payment_intent_version();
DROP TRIGGER IF EXISTS payment_intent_versions_immutable ON payment_intent_versions;
DROP FUNCTION IF EXISTS immutable_payment_intent_version();
DROP TABLE IF EXISTS payment_intent_versions;
ALTER TABLE ledger_transactions DROP CONSTRAINT IF EXISTS ledger_transactions_sequence_unique;
ALTER TABLE ledger_transactions DROP COLUMN IF EXISTS ledger_sequence;
DROP SEQUENCE IF EXISTS ledger_transaction_sequence;

COMMIT;
