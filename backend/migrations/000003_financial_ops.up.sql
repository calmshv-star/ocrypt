BEGIN;

CREATE EXTENSION IF NOT EXISTS pgcrypto;

-- Financial operations are intentionally isolated from the operator/admin
-- control-plane migration. This migration only depends on tenants, assets and
-- the uint256 domain created by 000001_platform.up.sql.

CREATE TYPE financial_sweep_status AS ENUM (
    'approval_required','approved','building','awaiting_signature','signed',
    'broadcast','confirmed','finalized','rejected','cancelled','failed','reorged'
);
CREATE TYPE financial_refund_status AS ENUM (
    'approval_required','approved','building','awaiting_signature','signed',
    'broadcast','confirmed','finalized','rejected','cancelled','failed','reorged'
);
CREATE TYPE financial_reconciliation_status AS ENUM ('requested','running','completed','failed');

CREATE TABLE financial_treasury_policies (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    asset_id text NOT NULL REFERENCES assets(id),
    chain_id text NOT NULL,
    version bigint NOT NULL CHECK (version > 0),
    enabled boolean NOT NULL,
    emergency_paused boolean NOT NULL DEFAULT false,
    policy jsonb NOT NULL CHECK (jsonb_typeof(policy) = 'object'),
    active_from timestamptz NOT NULL,
    active_until timestamptz,
    created_at timestamptz NOT NULL,
    CHECK (active_until IS NULL OR active_until > active_from),
    UNIQUE (tenant_id, id),
    UNIQUE (tenant_id, id, asset_id, chain_id),
    UNIQUE (tenant_id, asset_id, version),
    FOREIGN KEY (chain_id, asset_id) REFERENCES assets(chain_id, id)
);
CREATE UNIQUE INDEX financial_treasury_policy_active_idx
    ON financial_treasury_policies (tenant_id, asset_id)
    WHERE enabled AND NOT emergency_paused AND active_until IS NULL;

CREATE TABLE financial_refund_policies (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    asset_id text NOT NULL REFERENCES assets(id),
    chain_id text NOT NULL,
    version bigint NOT NULL CHECK (version > 0),
    enabled boolean NOT NULL,
    emergency_paused boolean NOT NULL DEFAULT false,
    refund_to_origin_only boolean NOT NULL DEFAULT true,
    policy jsonb NOT NULL CHECK (jsonb_typeof(policy) = 'object'),
    active_from timestamptz NOT NULL,
    active_until timestamptz,
    created_at timestamptz NOT NULL,
    CHECK (active_until IS NULL OR active_until > active_from),
    UNIQUE (tenant_id, id),
    UNIQUE (tenant_id, id, asset_id, chain_id),
    UNIQUE (tenant_id, asset_id, version),
    FOREIGN KEY (chain_id, asset_id) REFERENCES assets(chain_id, id)
);
CREATE UNIQUE INDEX financial_refund_policy_active_idx
    ON financial_refund_policies (tenant_id, asset_id)
    WHERE enabled AND NOT emergency_paused AND active_until IS NULL;

-- A refundable settlement must point at the exact tenant-scoped match that
-- bound the global chain event to the tenant's payment intent.
ALTER TABLE payment_matches ADD CONSTRAINT financial_refund_match_scope_unique
    UNIQUE (id, tenant_id, event_id, intent_id);

CREATE TABLE financial_sweep_requests (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    asset_id text NOT NULL REFERENCES assets(id),
    chain_id text NOT NULL,
    policy_id uuid NOT NULL,
    policy_version bigint NOT NULL CHECK (policy_version > 0),
    creator_id text NOT NULL,
    idempotency_key text NOT NULL CHECK (length(idempotency_key) BETWEEN 8 AND 255),
    request_hash text NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
    status financial_sweep_status NOT NULL,
    amount_atomic uint256 NOT NULL CHECK (amount_atomic > 0),
    aggregate jsonb NOT NULL CHECK (jsonb_typeof(aggregate) = 'object'),
    version bigint NOT NULL CHECK (version > 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    UNIQUE (tenant_id, id),
    UNIQUE (tenant_id, idempotency_key),
    FOREIGN KEY (tenant_id, policy_id, asset_id, chain_id)
        REFERENCES financial_treasury_policies(tenant_id, id, asset_id, chain_id),
    FOREIGN KEY (chain_id, asset_id) REFERENCES assets(chain_id, id)
);
CREATE INDEX financial_sweeps_queue_idx ON financial_sweep_requests (tenant_id, status, id);

CREATE TABLE financial_sweep_source_reservations (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    sweep_id uuid NOT NULL,
    chain_id text NOT NULL,
    source_address text NOT NULL,
    nonce_ref text NOT NULL,
    reserved_at timestamptz NOT NULL,
    released_at timestamptz,
    PRIMARY KEY (tenant_id, sweep_id, source_address, nonce_ref),
    FOREIGN KEY (tenant_id, sweep_id) REFERENCES financial_sweep_requests(tenant_id, id)
);
CREATE UNIQUE INDEX financial_sweep_source_active_idx
    ON financial_sweep_source_reservations (tenant_id, chain_id, source_address, nonce_ref)
    WHERE released_at IS NULL;

CREATE TABLE financial_refund_settlements (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    payment_match_id uuid NOT NULL,
    asset_id text NOT NULL REFERENCES assets(id),
    chain_id text NOT NULL,
    intent_id uuid NOT NULL,
    chain_event_id uuid NOT NULL,
    observed_sender text NOT NULL,
    received_amount_atomic uint256 NOT NULL CHECK (received_amount_atomic > 0),
    refunded_amount_atomic uint256 NOT NULL DEFAULT 0,
    finalized boolean NOT NULL,
    risk_hold boolean NOT NULL DEFAULT false,
    evidence_digest text NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CHECK (refunded_amount_atomic <= received_amount_atomic),
    UNIQUE (tenant_id, id),
    UNIQUE (tenant_id, id, asset_id, chain_id),
    UNIQUE (tenant_id, chain_event_id),
    FOREIGN KEY (intent_id, tenant_id) REFERENCES payment_intents(id, tenant_id),
    FOREIGN KEY (chain_event_id, asset_id) REFERENCES transfer_events(id, asset_id),
    FOREIGN KEY (payment_match_id, tenant_id, chain_event_id, intent_id)
        REFERENCES payment_matches(id, tenant_id, event_id, intent_id),
    FOREIGN KEY (chain_id, asset_id) REFERENCES assets(chain_id, id)
);

CREATE TABLE financial_verified_refund_destinations (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    settlement_id uuid NOT NULL,
    asset_id text NOT NULL REFERENCES assets(id),
    chain_id text NOT NULL,
    address text NOT NULL,
    method text NOT NULL CHECK (method IN ('wallet_signature','custodian_return_instruction','merchant_evidence')),
    evidence_digest text NOT NULL,
    verified_at timestamptz NOT NULL,
    expires_at timestamptz,
    revoked_at timestamptz,
    created_at timestamptz NOT NULL,
    UNIQUE (tenant_id, id),
    FOREIGN KEY (tenant_id, settlement_id, asset_id, chain_id)
        REFERENCES financial_refund_settlements(tenant_id, id, asset_id, chain_id),
    FOREIGN KEY (chain_id, asset_id) REFERENCES assets(chain_id, id),
    CHECK (expires_at IS NULL OR expires_at > verified_at)
);

CREATE TABLE financial_refund_requests (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    settlement_id uuid NOT NULL,
    asset_id text NOT NULL REFERENCES assets(id),
    chain_id text NOT NULL,
    policy_id uuid NOT NULL,
    policy_version bigint NOT NULL CHECK (policy_version > 0),
    creator_id text NOT NULL,
    idempotency_key text NOT NULL CHECK (length(idempotency_key) BETWEEN 8 AND 255),
    request_hash text NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
    status financial_refund_status NOT NULL,
    gross_amount_atomic uint256 NOT NULL CHECK (gross_amount_atomic > 0),
    aggregate jsonb NOT NULL CHECK (jsonb_typeof(aggregate) = 'object'),
    version bigint NOT NULL CHECK (version > 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    UNIQUE (tenant_id, id),
    UNIQUE (tenant_id, id, settlement_id),
    UNIQUE (tenant_id, idempotency_key),
    FOREIGN KEY (tenant_id, settlement_id, asset_id, chain_id)
        REFERENCES financial_refund_settlements(tenant_id, id, asset_id, chain_id),
    FOREIGN KEY (tenant_id, policy_id, asset_id, chain_id)
        REFERENCES financial_refund_policies(tenant_id, id, asset_id, chain_id),
    FOREIGN KEY (chain_id, asset_id) REFERENCES assets(chain_id, id)
);
CREATE INDEX financial_refunds_queue_idx ON financial_refund_requests (tenant_id, status, id);

CREATE TABLE financial_refund_reservations (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    settlement_id uuid NOT NULL,
    refund_id uuid NOT NULL,
    amount_atomic uint256 NOT NULL CHECK (amount_atomic > 0),
    reserved_at timestamptz NOT NULL,
    released_at timestamptz,
    finalized_at timestamptz,
    PRIMARY KEY (tenant_id, refund_id),
    FOREIGN KEY (tenant_id, settlement_id) REFERENCES financial_refund_settlements(tenant_id, id),
    FOREIGN KEY (tenant_id, refund_id, settlement_id)
        REFERENCES financial_refund_requests(tenant_id, id, settlement_id),
    CHECK (NOT (released_at IS NOT NULL AND finalized_at IS NOT NULL))
);

CREATE TABLE financial_usage_buckets (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    asset_id text NOT NULL REFERENCES assets(id),
    operation text NOT NULL CHECK (operation IN ('sweep','refund')),
    window_start timestamptz NOT NULL,
    window_end timestamptz NOT NULL,
    amount_atomic uint256 NOT NULL DEFAULT 0,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, asset_id, operation, window_start),
    CHECK (window_end > window_start)
);

CREATE TABLE financial_ledger_transactions (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    asset_id text NOT NULL REFERENCES assets(id),
    entry_type text NOT NULL,
    aggregate_type text NOT NULL CHECK (aggregate_type IN ('sweep','refund')),
    aggregate_id uuid NOT NULL,
    occurred_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL,
    UNIQUE (tenant_id, id),
    UNIQUE (id, tenant_id, asset_id)
);

CREATE TABLE financial_ledger_legs (
    transaction_id uuid NOT NULL,
    tenant_id uuid NOT NULL,
    asset_id text NOT NULL REFERENCES assets(id),
    sequence smallint NOT NULL CHECK (sequence IN (1,2)),
    direction ledger_direction NOT NULL,
    account_code text NOT NULL,
    amount_atomic uint256 NOT NULL CHECK (amount_atomic > 0),
    created_at timestamptz NOT NULL,
    PRIMARY KEY (transaction_id, sequence),
    FOREIGN KEY (transaction_id, tenant_id, asset_id)
        REFERENCES financial_ledger_transactions(id, tenant_id, asset_id)
);

CREATE FUNCTION assert_financial_ledger_balanced() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    target uuid := COALESCE(NEW.transaction_id, OLD.transaction_id);
    leg_count integer;
    debit_total numeric;
    credit_total numeric;
BEGIN
    SELECT count(*),
           COALESCE(sum(amount_atomic) FILTER (WHERE direction='debit'),0),
           COALESCE(sum(amount_atomic) FILTER (WHERE direction='credit'),0)
      INTO leg_count, debit_total, credit_total
      FROM financial_ledger_legs WHERE transaction_id=target;
    IF leg_count <> 2 OR debit_total <> credit_total THEN
        RAISE EXCEPTION 'financial ledger transaction % must contain one balanced debit/credit pair', target;
    END IF;
    RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER financial_ledger_balanced
AFTER INSERT OR UPDATE OR DELETE ON financial_ledger_legs
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION assert_financial_ledger_balanced();

CREATE TABLE financial_audit_log (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    aggregate_type text NOT NULL CHECK (aggregate_type IN ('sweep','refund','reconciliation')),
    aggregate_id uuid NOT NULL,
    actor_id text NOT NULL,
    action text NOT NULL,
    reason text NOT NULL,
    chain_sequence bigint NOT NULL CHECK (chain_sequence > 0),
    previous_hash bytea NOT NULL CHECK (octet_length(previous_hash) = 32),
    entry_hash bytea NOT NULL CHECK (octet_length(entry_hash) = 32),
    occurred_at timestamptz NOT NULL,
    recorded_at timestamptz NOT NULL,
    UNIQUE (tenant_id, id),
    UNIQUE (tenant_id, chain_sequence),
    UNIQUE (tenant_id, entry_hash)
);
CREATE INDEX financial_audit_aggregate_idx ON financial_audit_log (tenant_id, aggregate_type, aggregate_id, occurred_at, id);

CREATE FUNCTION append_financial_audit(
    requested_id uuid, requested_tenant uuid, requested_aggregate_type text,
    requested_aggregate_id uuid, requested_actor text, requested_action text,
    requested_reason text, requested_occurred_at timestamptz
) RETURNS bytea
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
DECLARE
    prior bytea;
    next_sequence bigint;
    calculated bytea;
BEGIN
    PERFORM pg_advisory_xact_lock(hashtextextended(requested_tenant::text || ':financial-audit', 0));
    SELECT entry_hash, chain_sequence + 1 INTO prior, next_sequence
      FROM financial_audit_log WHERE tenant_id=requested_tenant
      ORDER BY chain_sequence DESC LIMIT 1;
    prior := COALESCE(prior, decode(repeat('00',32),'hex'));
    next_sequence := COALESCE(next_sequence, 1);
    calculated := digest(convert_to(concat_ws(E'\x1f',
      encode(prior,'hex'), requested_id::text, requested_tenant::text,
      requested_aggregate_type, requested_aggregate_id::text, requested_actor,
      requested_action, requested_reason,
      to_char(requested_occurred_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
      next_sequence::text), 'UTF8'), 'sha256');
    INSERT INTO financial_audit_log
      (id,tenant_id,aggregate_type,aggregate_id,actor_id,action,reason,
       chain_sequence,previous_hash,entry_hash,occurred_at,recorded_at)
    VALUES (requested_id,requested_tenant,requested_aggregate_type,requested_aggregate_id,
      requested_actor,requested_action,requested_reason,next_sequence,prior,calculated,
      requested_occurred_at,clock_timestamp());
    RETURN calculated;
END $$;
REVOKE ALL ON FUNCTION append_financial_audit(uuid,uuid,text,uuid,text,text,text,timestamptz) FROM PUBLIC;

CREATE TABLE financial_outbox (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    aggregate_type text NOT NULL CHECK (aggregate_type IN ('sweep','refund','reconciliation')),
    aggregate_id uuid NOT NULL,
    event_type text NOT NULL,
    payload jsonb NOT NULL CHECK (jsonb_typeof(payload) = 'object'),
    occurred_at timestamptz NOT NULL,
    available_at timestamptz NOT NULL,
    published_at timestamptz,
    dead_lettered_at timestamptz,
    lease_owner text,
    lease_token bigint NOT NULL DEFAULT 0,
    lease_until timestamptz,
    attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    last_error text,
    UNIQUE (tenant_id, id)
);
CREATE INDEX financial_outbox_pending_idx ON financial_outbox (available_at, id) WHERE published_at IS NULL;

CREATE TABLE financial_proxy_nonces (
    key_id text NOT NULL,
    nonce text NOT NULL CHECK (length(nonce) BETWEEN 16 AND 128),
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (key_id, nonce)
);
CREATE INDEX financial_proxy_nonces_expiry_idx ON financial_proxy_nonces (expires_at);
REVOKE ALL ON financial_proxy_nonces FROM PUBLIC;

CREATE TABLE financial_reconciliation_runs (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    idempotency_key text NOT NULL CHECK (length(idempotency_key) BETWEEN 8 AND 255),
    request_hash text NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
    status financial_reconciliation_status NOT NULL,
    aggregate jsonb NOT NULL CHECK (jsonb_typeof(aggregate) = 'object'),
    report_digest text,
    version bigint NOT NULL CHECK (version > 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    UNIQUE (tenant_id, id),
    UNIQUE (tenant_id, idempotency_key)
);
CREATE INDEX financial_reconciliation_queue_idx ON financial_reconciliation_runs (tenant_id, status, id);

CREATE TABLE financial_reconciliation_items (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    run_id uuid NOT NULL,
    asset_id text NOT NULL REFERENCES assets(id),
    item jsonb NOT NULL CHECK (jsonb_typeof(item) = 'object'),
    PRIMARY KEY (tenant_id, run_id, asset_id),
    FOREIGN KEY (tenant_id, run_id) REFERENCES financial_reconciliation_runs(tenant_id, id)
);

CREATE TABLE financial_reconciliation_integrity_items (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    run_id uuid NOT NULL,
    asset_id text NOT NULL REFERENCES assets(id),
    subject_id text NOT NULL,
    item jsonb NOT NULL CHECK (jsonb_typeof(item) = 'object'),
    PRIMARY KEY (tenant_id, run_id, asset_id, subject_id),
    FOREIGN KEY (tenant_id, run_id) REFERENCES financial_reconciliation_runs(tenant_id, id)
);

-- Immutable, independently collected evidence consumed by deterministic runs.
CREATE TABLE financial_balance_snapshots (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    asset_id text NOT NULL REFERENCES assets(id),
    cutoff_at timestamptz NOT NULL,
    snapshot jsonb NOT NULL CHECK (jsonb_typeof(snapshot) = 'object'),
    evidence_digest text NOT NULL,
    observed_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, asset_id, cutoff_at)
);
CREATE TABLE financial_integrity_snapshots (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    asset_id text NOT NULL REFERENCES assets(id),
    cutoff_at timestamptz NOT NULL,
    subject_id text NOT NULL,
    snapshot jsonb NOT NULL CHECK (jsonb_typeof(snapshot) = 'object'),
    evidence_digest text NOT NULL,
    observed_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, asset_id, cutoff_at, subject_id)
);

CREATE TABLE financial_work_leases (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    aggregate_type text NOT NULL CHECK (aggregate_type IN ('sweep','refund','reconciliation')),
    aggregate_id uuid NOT NULL,
    owner_id text NOT NULL,
    fencing_token bigint NOT NULL CHECK (fencing_token > 0),
    lease_until timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, aggregate_type, aggregate_id)
);

-- One-time safe backfill. The worker repeats this bridge idempotently for new
-- matches and marks existing rows on hold when core evidence is reorged.
INSERT INTO financial_refund_settlements
(id,tenant_id,payment_match_id,asset_id,chain_id,intent_id,chain_event_id,observed_sender,
 received_amount_atomic,refunded_amount_atomic,finalized,risk_hold,evidence_digest,created_at,updated_at)
SELECT pm.id,pm.tenant_id,pm.id,te.asset_id,te.chain_id,pm.intent_id,te.id,te.from_address,
 pm.received_atomic,0,true,false,encode(te.evidence_hash,'hex'),clock_timestamp(),clock_timestamp()
FROM payment_matches pm JOIN transfer_events te ON te.id=pm.event_id
WHERE pm.state='finalized' AND te.status='finalized'
ON CONFLICT (id) DO NOTHING;

DO $$
DECLARE table_name text;
BEGIN
  FOREACH table_name IN ARRAY ARRAY[
    'financial_treasury_policies','financial_refund_policies','financial_sweep_requests',
    'financial_sweep_source_reservations','financial_refund_settlements',
    'financial_verified_refund_destinations','financial_refund_requests',
    'financial_refund_reservations','financial_usage_buckets','financial_ledger_transactions',
    'financial_ledger_legs','financial_audit_log','financial_outbox',
    'financial_reconciliation_runs','financial_reconciliation_items',
    'financial_reconciliation_integrity_items','financial_balance_snapshots',
    'financial_integrity_snapshots','financial_work_leases'
  ] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', table_name);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', table_name);
    EXECUTE format(
      'CREATE POLICY %I ON %I USING (tenant_id = nullif(current_setting(''app.tenant_id'', true), '''')::uuid) WITH CHECK (tenant_id = nullif(current_setting(''app.tenant_id'', true), '''')::uuid)',
      table_name || '_tenant_policy', table_name
    );
  END LOOP;
END $$;

-- Audit and ledger history are append-only for the application role. The
-- migration role retains ownership so schema migrations and restore tooling can
-- still operate under explicit operational control.
CREATE FUNCTION deny_financial_history_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION '% is append-only', TG_TABLE_NAME;
END $$;
CREATE TRIGGER financial_audit_append_only BEFORE UPDATE OR DELETE ON financial_audit_log
FOR EACH ROW EXECUTE FUNCTION deny_financial_history_mutation();
CREATE TRIGGER financial_ledger_transactions_append_only BEFORE UPDATE OR DELETE ON financial_ledger_transactions
FOR EACH ROW EXECUTE FUNCTION deny_financial_history_mutation();
CREATE TRIGGER financial_ledger_legs_append_only BEFORE UPDATE OR DELETE ON financial_ledger_legs
FOR EACH ROW EXECUTE FUNCTION deny_financial_history_mutation();
REVOKE INSERT, UPDATE, DELETE ON financial_audit_log FROM PUBLIC;

COMMIT;
