BEGIN;

DROP TRIGGER IF EXISTS financial_ledger_legs_append_only ON financial_ledger_legs;
DROP TRIGGER IF EXISTS financial_ledger_transactions_append_only ON financial_ledger_transactions;
DROP TRIGGER IF EXISTS financial_audit_append_only ON financial_audit_log;
DROP FUNCTION IF EXISTS deny_financial_history_mutation();
DROP FUNCTION IF EXISTS append_financial_audit(uuid,uuid,text,uuid,text,text,text,timestamptz);
DROP TRIGGER IF EXISTS financial_ledger_balanced ON financial_ledger_legs;
DROP FUNCTION IF EXISTS assert_financial_ledger_balanced();

DROP TABLE IF EXISTS financial_work_leases;
DROP TABLE IF EXISTS financial_integrity_snapshots;
DROP TABLE IF EXISTS financial_balance_snapshots;
DROP TABLE IF EXISTS financial_reconciliation_integrity_items;
DROP TABLE IF EXISTS financial_reconciliation_items;
DROP TABLE IF EXISTS financial_reconciliation_runs;
DROP TABLE IF EXISTS financial_outbox;
DROP TABLE IF EXISTS financial_proxy_nonces;
DROP TABLE IF EXISTS financial_audit_log;
DROP TABLE IF EXISTS financial_ledger_legs;
DROP TABLE IF EXISTS financial_ledger_transactions;
DROP TABLE IF EXISTS financial_usage_buckets;
DROP TABLE IF EXISTS financial_refund_reservations;
DROP TABLE IF EXISTS financial_refund_requests;
DROP TABLE IF EXISTS financial_verified_refund_destinations;
DROP TABLE IF EXISTS financial_refund_settlements;
DROP TABLE IF EXISTS financial_sweep_source_reservations;
DROP TABLE IF EXISTS financial_sweep_requests;
DROP TABLE IF EXISTS financial_refund_policies;
DROP TABLE IF EXISTS financial_treasury_policies;

ALTER TABLE payment_matches DROP CONSTRAINT IF EXISTS financial_refund_match_scope_unique;

DROP TYPE IF EXISTS financial_reconciliation_status;
DROP TYPE IF EXISTS financial_refund_status;
DROP TYPE IF EXISTS financial_sweep_status;

COMMIT;
