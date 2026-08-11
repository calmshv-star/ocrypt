BEGIN;

-- Merchant runtime completion: append-only intent versions, immutable lookup
-- surfaces, and durable asynchronous reconciliation exports.

CREATE SEQUENCE ledger_transaction_sequence AS bigint;
-- The access-exclusive admission lock is held until COMMIT. New ledger writes
-- cannot race the deterministic backfill, NOT NULL, default, or unique
-- constraint. Existing rows follow booked_at/id; subsequent rows use append
-- order from the sequence default.
LOCK TABLE ledger_transactions IN ACCESS EXCLUSIVE MODE;
ALTER TABLE ledger_transactions ADD COLUMN ledger_sequence bigint;
DO $$
DECLARE item record;
BEGIN
  FOR item IN SELECT id FROM ledger_transactions ORDER BY booked_at,id LOOP
    UPDATE ledger_transactions SET ledger_sequence=nextval('ledger_transaction_sequence') WHERE id=item.id;
  END LOOP;
END $$;
ALTER TABLE ledger_transactions ALTER COLUMN ledger_sequence SET DEFAULT nextval('ledger_transaction_sequence');
ALTER TABLE ledger_transactions ALTER COLUMN ledger_sequence SET NOT NULL;
ALTER TABLE ledger_transactions ADD CONSTRAINT ledger_transactions_sequence_unique UNIQUE(ledger_sequence);

CREATE TABLE payment_intent_versions (
    tenant_id uuid NOT NULL,
    merchant_id uuid NOT NULL,
    intent_id uuid NOT NULL,
    version bigint NOT NULL CHECK(version>0),
    merchant_order_id text NOT NULL,
    customer_reference text,
    amount_minor uint256 NOT NULL CHECK(amount_minor>0),
    currency char(3) NOT NULL,
    currency_scale smallint NOT NULL CHECK(currency_scale BETWEEN 0 AND 9),
    description text NOT NULL,
    metadata jsonb NOT NULL CHECK(jsonb_typeof(metadata)='object' AND pg_column_size(metadata)<=16384),
    allowed_routes jsonb NOT NULL CHECK(jsonb_typeof(allowed_routes)='array'),
    status intent_status NOT NULL,
    status_reason text NOT NULL,
    policy_snapshot jsonb NOT NULL,
    expires_at timestamptz NOT NULL,
    settled_at timestamptz,
    cancelled_at timestamptz,
    recorded_at timestamptz NOT NULL,
    correlation_id text,
    PRIMARY KEY(intent_id,version),
    FOREIGN KEY(intent_id,tenant_id) REFERENCES payment_intents(id,tenant_id),
    FOREIGN KEY(merchant_id,tenant_id) REFERENCES merchants(id,tenant_id)
);
INSERT INTO payment_intent_versions(
    tenant_id,merchant_id,intent_id,version,merchant_order_id,customer_reference,
    amount_minor,currency,currency_scale,description,metadata,allowed_routes,status,
    status_reason,policy_snapshot,expires_at,settled_at,cancelled_at,recorded_at
)
SELECT tenant_id,merchant_id,id,version,merchant_order_id,customer_reference,
       amount_minor,currency,currency_scale,description,metadata,allowed_routes,status,
       status_reason,policy_snapshot,expires_at,settled_at,cancelled_at,updated_at
  FROM payment_intents;

CREATE FUNCTION capture_payment_intent_version() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP='UPDATE' AND NEW.version<>OLD.version+1 THEN
    RAISE EXCEPTION 'payment intent mutation must advance version exactly once';
  END IF;
  INSERT INTO payment_intent_versions(
    tenant_id,merchant_id,intent_id,version,merchant_order_id,customer_reference,
    amount_minor,currency,currency_scale,description,metadata,allowed_routes,status,
    status_reason,policy_snapshot,expires_at,settled_at,cancelled_at,recorded_at,correlation_id
  ) VALUES(
    NEW.tenant_id,NEW.merchant_id,NEW.id,NEW.version,NEW.merchant_order_id,NEW.customer_reference,
    NEW.amount_minor,NEW.currency,NEW.currency_scale,NEW.description,NEW.metadata,NEW.allowed_routes,NEW.status,
    NEW.status_reason,NEW.policy_snapshot,NEW.expires_at,NEW.settled_at,NEW.cancelled_at,clock_timestamp(),
    NULLIF(current_setting('app.correlation_id',true),'')
  );
  RETURN NEW;
END $$;
CREATE TRIGGER payment_intents_capture_insert
AFTER INSERT ON payment_intents FOR EACH ROW EXECUTE FUNCTION capture_payment_intent_version();
CREATE TRIGGER payment_intents_capture_update
AFTER UPDATE ON payment_intents
FOR EACH ROW WHEN(NEW.version IS DISTINCT FROM OLD.version)
EXECUTE FUNCTION capture_payment_intent_version();

CREATE FUNCTION require_payment_intent_version_advance() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.version<>OLD.version+1 THEN
    RAISE EXCEPTION 'payment intent state mutation must advance version exactly once';
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER payment_intents_require_version_advance
BEFORE UPDATE OF merchant_order_id,customer_reference,amount_minor,currency,currency_scale,description,metadata,allowed_routes,status,status_reason,policy_snapshot,expires_at,settled_at,cancelled_at
ON payment_intents FOR EACH ROW EXECUTE FUNCTION require_payment_intent_version_advance();

CREATE FUNCTION immutable_payment_intent_version() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'payment intent versions are immutable';
END $$;
CREATE TRIGGER payment_intent_versions_immutable
BEFORE UPDATE OR DELETE ON payment_intent_versions
FOR EACH ROW EXECUTE FUNCTION immutable_payment_intent_version();

CREATE TABLE reconciliation_reports (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    merchant_id uuid NOT NULL,
    format text NOT NULL CHECK(format IN('jsonl_v1')),
    period_start timestamptz NOT NULL,
    period_end timestamptz NOT NULL,
    snapshot_ledger_sequence bigint NOT NULL CHECK(snapshot_ledger_sequence>=0),
    snapshot_fence_sequence bigint NOT NULL CHECK(snapshot_fence_sequence>=0),
    snapshot_cutoff timestamptz NOT NULL,
    status text NOT NULL CHECK(status IN('queued','processing','retry','ready','dead_letter')),
    object_key text,
    object_size_bytes bigint CHECK(object_size_bytes IS NULL OR object_size_bytes>=0),
    object_sha256 bytea CHECK(object_sha256 IS NULL OR octet_length(object_sha256)=32),
    signature bytea,
    signing_key_id text,
    attempt_count integer NOT NULL DEFAULT 0 CHECK(attempt_count>=0),
    next_attempt_at timestamptz NOT NULL,
    last_error_code text,
    locked_by text,
    locked_until timestamptz,
    lease_token uuid,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    completed_at timestamptz,
    version bigint NOT NULL DEFAULT 1 CHECK(version>0),
    FOREIGN KEY(merchant_id,tenant_id) REFERENCES merchants(id,tenant_id),
    UNIQUE(id,tenant_id),
    CHECK(period_end>period_start),
    CHECK((status='ready')=(object_key IS NOT NULL AND object_size_bytes IS NOT NULL AND object_sha256 IS NOT NULL AND signature IS NOT NULL AND signing_key_id IS NOT NULL AND completed_at IS NOT NULL)),
    CHECK(object_key IS NULL OR object_key ~ '^reconciliation/[0-9a-f-]{36}/[0-9a-f-]{36}[.]jsonl$')
);
CREATE INDEX reconciliation_reports_claim_idx
ON reconciliation_reports(next_attempt_at,id) WHERE status IN('queued','retry');
CREATE INDEX reconciliation_reports_merchant_idx
ON reconciliation_reports(tenant_id,merchant_id,created_at DESC,id DESC);

-- Serialize every callback producer through one transactional row. Unlike a
-- PostgreSQL sequence, this allocator rolls back with the callback event, so
-- merchant-local pull recovery remains strictly monotonic without avoidable
-- gaps. The lock waits for in-flight legacy callback inserts before backfill.
LOCK TABLE callback_events IN SHARE MODE;
CREATE TABLE merchant_event_sequences (
    tenant_id uuid NOT NULL,
    merchant_id uuid NOT NULL,
    last_sequence bigint NOT NULL CHECK(last_sequence>=0),
    updated_at timestamptz NOT NULL,
    PRIMARY KEY(tenant_id,merchant_id),
    FOREIGN KEY(merchant_id,tenant_id) REFERENCES merchants(id,tenant_id)
);
INSERT INTO merchant_event_sequences(tenant_id,merchant_id,last_sequence,updated_at)
SELECT tenant_id,merchant_id,max(merchant_sequence),clock_timestamp()
FROM callback_events GROUP BY tenant_id,merchant_id;

ALTER TABLE payment_intent_versions ENABLE ROW LEVEL SECURITY;
ALTER TABLE reconciliation_reports ENABLE ROW LEVEL SECURITY;
ALTER TABLE merchant_event_sequences ENABLE ROW LEVEL SECURITY;
ALTER TABLE payment_intent_versions FORCE ROW LEVEL SECURITY;
ALTER TABLE reconciliation_reports FORCE ROW LEVEL SECURITY;
ALTER TABLE merchant_event_sequences FORCE ROW LEVEL SECURITY;
CREATE POLICY payment_intent_versions_tenant_policy ON payment_intent_versions
    USING(tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid)
    WITH CHECK(tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid);
CREATE POLICY reconciliation_reports_tenant_policy ON reconciliation_reports
    USING(tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid)
    WITH CHECK(tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid);
CREATE POLICY merchant_event_sequences_tenant_policy ON merchant_event_sequences
    USING(tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid)
    WITH CHECK(tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid);

REVOKE UPDATE,DELETE,TRUNCATE ON payment_intent_versions FROM PUBLIC;
REVOKE DELETE,TRUNCATE ON reconciliation_reports FROM PUBLIC;

DO $$
DECLARE role_name text;
BEGIN
  IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='merchant_reconciliation_worker') THEN
    GRANT SELECT,UPDATE ON reconciliation_reports TO merchant_reconciliation_worker;
    GRANT SELECT ON ledger_transactions,ledger_entries,ledger_accounts TO merchant_reconciliation_worker;
    REVOKE DELETE,TRUNCATE ON reconciliation_reports FROM merchant_reconciliation_worker;
  END IF;
  FOREACH role_name IN ARRAY ARRAY['merchant_api_runtime','merchant_management_runtime','merchant_scanner_worker','merchant_settlement_worker','merchant_matching_worker','merchant_resolution_worker','merchant_proof_worker','merchant_plan_worker'] LOOP
    IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname=role_name) THEN
      EXECUTE format('GRANT SELECT,INSERT,UPDATE ON merchant_event_sequences TO %I',role_name);
    END IF;
  END LOOP;
END $$;

COMMIT;
