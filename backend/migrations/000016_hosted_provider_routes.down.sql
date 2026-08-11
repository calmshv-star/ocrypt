BEGIN;

DROP FUNCTION IF EXISTS claim_hosted_order_recoveries(timestamptz,timestamptz,integer);
DROP FUNCTION IF EXISTS claim_hosted_prebind_recoveries(timestamptz,timestamptz,integer);
DROP FUNCTION IF EXISTS claim_hosted_create_recoveries(timestamptz,timestamptz,integer);

DROP FUNCTION IF EXISTS hosted_provider_callback_admitted(text);
DROP TRIGGER IF EXISTS provider_order_economic_immutable ON provider_orders;
DROP FUNCTION IF EXISTS provider_order_reject_economic_mutation();
DROP TRIGGER IF EXISTS provider_prebind_reject_delete ON provider_prebind_inbox;
DROP TRIGGER IF EXISTS provider_prebind_evidence_immutable ON provider_prebind_inbox;
DROP FUNCTION IF EXISTS provider_prebind_reject_evidence_mutation();
DROP TRIGGER IF EXISTS provider_reconcile_observation_immutable ON provider_reconcile_observations;
DROP TRIGGER IF EXISTS provider_inbox_immutable ON provider_inbox;
DROP FUNCTION IF EXISTS provider_inbox_reject_mutation();

ALTER TABLE payment_matches DROP CONSTRAINT IF EXISTS payment_matches_source_xor;
DROP INDEX IF EXISTS payment_matches_provider_inbox_active_idx;
DROP INDEX IF EXISTS payment_matches_event_active_idx;
ALTER TABLE payment_matches DROP COLUMN IF EXISTS provider_inbox_id;
ALTER TABLE payment_matches ALTER COLUMN event_id SET NOT NULL;
CREATE UNIQUE INDEX payment_matches_event_active_idx ON payment_matches(event_id) WHERE state<>'reversed';

DROP TABLE IF EXISTS hosted_provider_runtime_incidents;
DROP TABLE IF EXISTS provider_reconcile_observations;
DROP TABLE IF EXISTS provider_reconciliation_incidents;
DROP TABLE IF EXISTS provider_prebind_inbox;
DROP TABLE IF EXISTS provider_inbox;
ALTER TABLE payment_routes DROP CONSTRAINT IF EXISTS payment_routes_provider_shape_xor;
ALTER TABLE payment_routes DROP CONSTRAINT IF EXISTS payment_routes_provider_order_fk;
DROP INDEX IF EXISTS payment_routes_hosted_reference_idx;
ALTER TABLE payment_routes DROP COLUMN IF EXISTS payment_url;
ALTER TABLE payment_routes DROP COLUMN IF EXISTS provider_reference;
ALTER TABLE payment_routes DROP COLUMN IF EXISTS provider_id;
ALTER TABLE payment_routes DROP COLUMN IF EXISTS provider_order_id;
ALTER TABLE payment_routes ALTER COLUMN receiving_address SET NOT NULL;
ALTER TABLE payment_routes ALTER COLUMN chain_id SET NOT NULL;
DROP TABLE IF EXISTS provider_orders;
DROP TABLE IF EXISTS hosted_provider_create_attempts;
DROP TABLE IF EXISTS hosted_provider_configs;
DROP FUNCTION IF EXISTS hosted_https_origins_valid(text[]);

COMMIT;
