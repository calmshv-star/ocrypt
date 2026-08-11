BEGIN;

CREATE FUNCTION hosted_https_origins_valid(origins text[]) RETURNS boolean
LANGUAGE sql IMMUTABLE STRICT SET search_path=pg_catalog AS $$
  SELECT cardinality(origins) BETWEEN 1 AND 16
    AND bool_and(v ~ '^https://[^/?#]+$' AND length(v)<=512) FROM unnest(origins) v
$$;

-- A provider_id names one globally unique admitted provider account. Secrets
-- remain external; only bounded references into a secret-mounted directory are
-- stored here.
CREATE TABLE hosted_provider_configs (
    id text PRIMARY KEY CHECK (id ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$'),
    tenant_id uuid NOT NULL,
    merchant_id uuid NOT NULL,
    adapter_kind text NOT NULL CHECK (adapter_kind='hmac_json_v1'),
    api_origin text NOT NULL CHECK (api_origin ~ '^https://[^/?#]+$' AND length(api_origin)<=512),
    create_path text NOT NULL CHECK (create_path ~ '^/[^?#]*$' AND create_path !~ '^//' AND length(create_path)<=512),
    cancel_path text NOT NULL CHECK (cancel_path ~ '^/[^?#]*$' AND cancel_path !~ '^//' AND length(cancel_path)<=512),
    status_path text NOT NULL CHECK (status_path ~ '^/[^?#]*$' AND status_path !~ '^//' AND length(status_path)<=512),
    refund_path text NOT NULL CHECK (refund_path ~ '^/[^?#]*$' AND refund_path !~ '^//' AND length(refund_path)<=512),
    reconcile_path text NOT NULL CHECK (reconcile_path ~ '^/[^?#]*$' AND reconcile_path !~ '^//' AND length(reconcile_path)<=512),
    payment_url_origins text[] NOT NULL CHECK (cardinality(payment_url_origins) BETWEEN 1 AND 16),
    api_credential_ref text NOT NULL CHECK (api_credential_ref ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$'),
    api_key_id text NOT NULL CHECK (length(api_key_id) BETWEEN 1 AND 128),
    callback_secret_ref text NOT NULL CHECK (callback_secret_ref ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$'),
    callback_key_id text NOT NULL CHECK (length(callback_key_id) BETWEEN 1 AND 128),
    signature_scheme text NOT NULL CHECK (signature_scheme='hmac-sha256'),
    asset_id text NOT NULL REFERENCES assets(id),
    asset_decimals smallint NOT NULL CHECK (asset_decimals BETWEEN 0 AND 77),
    currency char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    status text NOT NULL CHECK (status IN ('active','paused','disabled')),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    version bigint NOT NULL DEFAULT 1 CHECK (version>0),
    FOREIGN KEY (merchant_id,tenant_id) REFERENCES merchants(id,tenant_id) ON DELETE RESTRICT,
    UNIQUE (id,tenant_id,merchant_id),
    CHECK (hosted_https_origins_valid(payment_url_origins))
);

CREATE TABLE hosted_provider_create_attempts (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    merchant_id uuid NOT NULL,
    provider_id text NOT NULL,
    intent_id uuid NOT NULL,
    idempotency_key text NOT NULL CHECK (length(idempotency_key) BETWEEN 8 AND 255),
    request_hash bytea NOT NULL CHECK (octet_length(request_hash)=32),
    state text NOT NULL CHECK (state IN ('claimed','retry','completed')),
    claim_token uuid,
    claim_until timestamptz,
    provider_order_id uuid,
    provider_reference text,
    payment_url text,
    asset_id text NOT NULL,
    fiat_amount_minor uint256 NOT NULL CHECK (fiat_amount_minor>0),
    currency char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    currency_scale smallint NOT NULL CHECK (currency_scale BETWEEN 0 AND 9),
    amount_atomic uint256,
    asset_decimals smallint CHECK (asset_decimals BETWEEN 0 AND 77),
    quote_id text,
    rate_numerator uint256,
    rate_denominator uint256,
    quote_issued_at timestamptz,
    create_response_body bytea,
    create_response_digest bytea,
    create_response_received_at timestamptz,
    expires_at timestamptz NOT NULL,
    last_error_code text,
    recovery_status text NOT NULL DEFAULT 'pending' CHECK (recovery_status IN ('pending','claimed','complete','incident')),
    recovery_claim_token uuid,
    recovery_claim_until timestamptz,
    recovery_attempt_count integer NOT NULL DEFAULT 0 CHECK (recovery_attempt_count>=0),
    next_recovery_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    last_recovery_error_code text CHECK (last_recovery_error_code IS NULL OR length(last_recovery_error_code)<=64),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    version bigint NOT NULL DEFAULT 1 CHECK (version>0),
    FOREIGN KEY (provider_id,tenant_id,merchant_id) REFERENCES hosted_provider_configs(id,tenant_id,merchant_id) ON DELETE RESTRICT,
    FOREIGN KEY (intent_id,tenant_id) REFERENCES payment_intents(id,tenant_id) ON DELETE RESTRICT,
    FOREIGN KEY (merchant_id,tenant_id) REFERENCES merchants(id,tenant_id) ON DELETE RESTRICT,
    UNIQUE (merchant_id,idempotency_key),
    CHECK ((state='claimed')=(claim_token IS NOT NULL AND claim_until IS NOT NULL)),
    CHECK ((state='completed')=(provider_order_id IS NOT NULL AND provider_reference IS NOT NULL AND payment_url IS NOT NULL
      AND amount_atomic IS NOT NULL AND asset_decimals IS NOT NULL AND quote_id IS NOT NULL
      AND rate_numerator IS NOT NULL AND rate_numerator>0 AND rate_denominator IS NOT NULL AND rate_denominator>0
      AND quote_issued_at IS NOT NULL AND create_response_body IS NOT NULL
      AND octet_length(create_response_body) BETWEEN 1 AND 262144
      AND create_response_digest=digest(create_response_body,'sha256') AND octet_length(create_response_digest)=32
      AND create_response_received_at IS NOT NULL)),
    CHECK (provider_reference IS NULL OR length(provider_reference) BETWEEN 1 AND 255),
    CHECK (payment_url IS NULL OR payment_url ~ '^https://' AND length(payment_url)<=2048)
    ,CHECK ((recovery_status='claimed')=(recovery_claim_token IS NOT NULL AND recovery_claim_until IS NOT NULL))
);
CREATE INDEX hosted_provider_create_claim_idx ON hosted_provider_create_attempts(claim_until,id)
  WHERE state='claimed';
CREATE INDEX hosted_provider_recovery_claim_idx ON hosted_provider_create_attempts(next_recovery_at,id)
  WHERE recovery_status='pending';
CREATE UNIQUE INDEX hosted_provider_create_reference_idx ON hosted_provider_create_attempts(provider_id,provider_reference)
  WHERE state='completed';

CREATE TABLE provider_orders (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    merchant_id uuid NOT NULL,
    route_id uuid NOT NULL,
    provider_id text NOT NULL,
    provider_reference text NOT NULL CHECK (length(provider_reference) BETWEEN 1 AND 255),
    provider_status text NOT NULL CHECK (provider_status IN ('pending','authorized','paid','cancel_requested','cancelled','failed','refunded','superseded')),
    asset_id text NOT NULL REFERENCES assets(id),
    fiat_amount_minor uint256 NOT NULL CHECK (fiat_amount_minor>0),
    currency char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    currency_scale smallint NOT NULL CHECK (currency_scale BETWEEN 0 AND 9),
    amount_atomic uint256 NOT NULL CHECK (amount_atomic>0),
    asset_decimals smallint NOT NULL CHECK (asset_decimals BETWEEN 0 AND 77),
    quote_id text NOT NULL CHECK (length(quote_id) BETWEEN 1 AND 255),
    rate_numerator uint256 NOT NULL CHECK (rate_numerator>0),
    rate_denominator uint256 NOT NULL CHECK (rate_denominator>0),
    quote_issued_at timestamptz NOT NULL,
    create_response_body bytea NOT NULL CHECK (octet_length(create_response_body) BETWEEN 1 AND 262144),
    create_response_digest bytea NOT NULL CHECK (octet_length(create_response_digest)=32 AND create_response_digest=digest(create_response_body,'sha256')),
    create_response_received_at timestamptz NOT NULL,
    payment_url text NOT NULL CHECK (payment_url ~ '^https://' AND length(payment_url)<=2048),
    provider_idempotency_key text NOT NULL CHECK (length(provider_idempotency_key) BETWEEN 8 AND 255),
    last_provider_occurred_at timestamptz,
    last_provider_event_id text,
    reconcile_claim_token uuid,
    reconcile_claim_until timestamptz,
    reconcile_attempt_count integer NOT NULL DEFAULT 0 CHECK (reconcile_attempt_count>=0),
    next_reconcile_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    last_reconcile_error_code text CHECK (last_reconcile_error_code IS NULL OR length(last_reconcile_error_code)<=64),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    version bigint NOT NULL DEFAULT 1 CHECK (version>0),
    FOREIGN KEY (route_id,tenant_id) REFERENCES payment_routes(id,tenant_id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
    FOREIGN KEY (provider_id,tenant_id,merchant_id) REFERENCES hosted_provider_configs(id,tenant_id,merchant_id) ON DELETE RESTRICT,
    FOREIGN KEY (merchant_id,tenant_id) REFERENCES merchants(id,tenant_id) ON DELETE RESTRICT,
    UNIQUE (route_id,tenant_id),
    UNIQUE (provider_id,provider_reference),
    UNIQUE (merchant_id,provider_idempotency_key),
    UNIQUE (id,tenant_id,merchant_id),
    CHECK ((reconcile_claim_token IS NULL)=(reconcile_claim_until IS NULL))
);
CREATE INDEX provider_order_reconcile_claim_idx ON provider_orders(next_reconcile_at,id)
  WHERE provider_status IN ('pending','authorized','cancel_requested');

ALTER TABLE payment_routes ALTER COLUMN chain_id DROP NOT NULL;
ALTER TABLE payment_routes ALTER COLUMN receiving_address DROP NOT NULL;
ALTER TABLE payment_routes ADD COLUMN provider_order_id uuid;
ALTER TABLE payment_routes ADD COLUMN provider_id text;
ALTER TABLE payment_routes ADD COLUMN provider_reference text;
ALTER TABLE payment_routes ADD COLUMN payment_url text;
ALTER TABLE payment_routes ADD CONSTRAINT payment_routes_provider_order_fk
  FOREIGN KEY(provider_order_id,tenant_id,merchant_id) REFERENCES provider_orders(id,tenant_id,merchant_id)
  ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE payment_routes ADD CONSTRAINT payment_routes_provider_shape_xor CHECK (
  (provider='on_chain' AND chain_id IS NOT NULL AND receiving_address IS NOT NULL
    AND provider_order_id IS NULL AND provider_id IS NULL AND provider_reference IS NULL AND payment_url IS NULL)
  OR
  (provider='hosted_gateway' AND chain_id IS NULL AND receiving_address IS NULL
    AND quote_id IS NULL AND address_assignment_id IS NULL AND required_finality=0
    AND provider_order_id IS NOT NULL AND provider_id IS NOT NULL
    AND provider_reference IS NOT NULL AND payment_url IS NOT NULL)
);
CREATE UNIQUE INDEX payment_routes_hosted_reference_idx ON payment_routes(provider_id,provider_reference)
  WHERE provider='hosted_gateway';

CREATE TABLE provider_inbox (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    merchant_id uuid NOT NULL,
    provider_id text NOT NULL,
    provider_order_id uuid NOT NULL,
    route_id uuid NOT NULL,
    provider_event_id text NOT NULL CHECK (length(provider_event_id) BETWEEN 1 AND 255),
    provider_reference text NOT NULL CHECK (length(provider_reference) BETWEEN 1 AND 255),
    provider_status text NOT NULL CHECK (provider_status IN ('pending','authorized','paid','cancelled','failed','refunded')),
    asset_id text NOT NULL,
    amount_atomic uint256 NOT NULL CHECK (amount_atomic>0),
    asset_decimals smallint NOT NULL CHECK (asset_decimals BETWEEN 0 AND 77),
    raw_body bytea NOT NULL CHECK (octet_length(raw_body) BETWEEN 1 AND 1048576),
    raw_body_digest bytea NOT NULL CHECK (octet_length(raw_body_digest)=32 AND raw_body_digest=digest(raw_body,'sha256')),
    signature_scheme text NOT NULL CHECK (signature_scheme='hmac-sha256'),
    signature_key_id text NOT NULL CHECK (length(signature_key_id) BETWEEN 1 AND 128),
    signature_digest bytea NOT NULL CHECK (octet_length(signature_digest)=32),
    provider_occurred_at timestamptz NOT NULL,
    received_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL,
    FOREIGN KEY (provider_order_id,tenant_id,merchant_id) REFERENCES provider_orders(id,tenant_id,merchant_id) ON DELETE RESTRICT,
    FOREIGN KEY (route_id,tenant_id) REFERENCES payment_routes(id,tenant_id) ON DELETE RESTRICT,
    FOREIGN KEY (provider_id,tenant_id,merchant_id) REFERENCES hosted_provider_configs(id,tenant_id,merchant_id) ON DELETE RESTRICT,
    UNIQUE (provider_id,provider_event_id),
    UNIQUE (id,tenant_id)
);
CREATE INDEX provider_inbox_order_time_idx ON provider_inbox(provider_order_id,provider_occurred_at,provider_event_id);

CREATE TABLE provider_prebind_inbox (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    merchant_id uuid NOT NULL,
    provider_id text NOT NULL,
    provider_event_id text NOT NULL CHECK (length(provider_event_id) BETWEEN 1 AND 255),
    provider_reference text NOT NULL CHECK (length(provider_reference) BETWEEN 1 AND 255),
    provider_status text NOT NULL CHECK (provider_status IN ('pending','authorized','paid','cancelled','failed','refunded')),
    asset_id text NOT NULL,
    amount_atomic uint256 NOT NULL CHECK (amount_atomic>0),
    asset_decimals smallint NOT NULL CHECK (asset_decimals BETWEEN 0 AND 77),
    raw_body bytea NOT NULL CHECK (octet_length(raw_body) BETWEEN 1 AND 1048576),
    raw_body_digest bytea NOT NULL CHECK (octet_length(raw_body_digest)=32 AND raw_body_digest=digest(raw_body,'sha256')),
    signature_scheme text NOT NULL CHECK (signature_scheme='hmac-sha256'),
    signature_key_id text NOT NULL CHECK (length(signature_key_id) BETWEEN 1 AND 128),
    signature_digest bytea NOT NULL CHECK (octet_length(signature_digest)=32),
    provider_paused_at_receipt boolean NOT NULL,
    provider_occurred_at timestamptz NOT NULL,
    received_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    state text NOT NULL CHECK (state IN ('pending','attached','expired')),
    attached_provider_inbox_id uuid,
    claim_token uuid,
    claim_until timestamptz,
    attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count>=0),
    next_attempt_at timestamptz NOT NULL,
    last_error_code text CHECK (last_error_code IS NULL OR length(last_error_code)<=64),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    version bigint NOT NULL DEFAULT 1 CHECK (version>0),
    FOREIGN KEY (provider_id,tenant_id,merchant_id) REFERENCES hosted_provider_configs(id,tenant_id,merchant_id) ON DELETE RESTRICT,
    FOREIGN KEY (attached_provider_inbox_id,tenant_id) REFERENCES provider_inbox(id,tenant_id) ON DELETE RESTRICT,
    UNIQUE (provider_id,provider_event_id),
    UNIQUE (id,tenant_id,merchant_id),
    CHECK ((claim_token IS NULL)=(claim_until IS NULL)),
    CHECK ((state='attached')=(attached_provider_inbox_id IS NOT NULL)),
    CHECK (expires_at>received_at)
);
CREATE INDEX provider_prebind_claim_idx ON provider_prebind_inbox(next_attempt_at,id) WHERE state='pending';

CREATE TABLE provider_reconciliation_incidents (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    merchant_id uuid NOT NULL,
    provider_id text NOT NULL,
    provider_order_id uuid NOT NULL,
    provider_inbox_id uuid NOT NULL,
    incident_kind text NOT NULL CHECK (incident_kind IN ('refund_after_settlement','refund_before_settlement','paid_after_refund','economic_fact_mismatch')),
    status text NOT NULL CHECK (status IN ('open','resolved')),
    created_at timestamptz NOT NULL,
    resolved_at timestamptz,
    version bigint NOT NULL DEFAULT 1 CHECK (version>0),
    FOREIGN KEY (provider_order_id,tenant_id,merchant_id) REFERENCES provider_orders(id,tenant_id,merchant_id) ON DELETE RESTRICT,
    FOREIGN KEY (provider_inbox_id,tenant_id) REFERENCES provider_inbox(id,tenant_id) ON DELETE RESTRICT,
    FOREIGN KEY (provider_id,tenant_id,merchant_id) REFERENCES hosted_provider_configs(id,tenant_id,merchant_id) ON DELETE RESTRICT,
    UNIQUE (provider_inbox_id),
    CHECK ((status='resolved')=(resolved_at IS NOT NULL))
);

-- Reconcile/status responses are transport-authenticated observations, not
-- signed payment evidence. They are immutable and can raise an incident, but
-- can never enter payment_matches or the ledger.
CREATE TABLE provider_reconcile_observations (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    merchant_id uuid NOT NULL,
    provider_id text NOT NULL,
    provider_order_id uuid NOT NULL,
    provider_reference text NOT NULL CHECK (length(provider_reference) BETWEEN 1 AND 255),
    provider_status text NOT NULL CHECK (provider_status IN ('pending','authorized','paid','cancelled','failed','refunded')),
    asset_id text NOT NULL,
    amount_atomic uint256 NOT NULL CHECK (amount_atomic>0),
    asset_decimals smallint NOT NULL CHECK (asset_decimals BETWEEN 0 AND 77),
    provider_occurred_at timestamptz NOT NULL,
    response_body bytea NOT NULL CHECK (octet_length(response_body) BETWEEN 1 AND 262144),
    response_digest bytea NOT NULL CHECK (octet_length(response_digest)=32 AND response_digest=digest(response_body,'sha256')),
    response_received_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL,
    FOREIGN KEY (provider_order_id,tenant_id,merchant_id) REFERENCES provider_orders(id,tenant_id,merchant_id) ON DELETE RESTRICT,
    FOREIGN KEY (provider_id,tenant_id,merchant_id) REFERENCES hosted_provider_configs(id,tenant_id,merchant_id) ON DELETE RESTRICT,
    UNIQUE (provider_order_id,response_digest),
    UNIQUE (id,tenant_id)
);

CREATE TABLE hosted_provider_runtime_incidents (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    merchant_id uuid NOT NULL,
    provider_id text NOT NULL,
    create_attempt_id uuid,
    provider_order_id uuid,
    provider_prebind_id uuid,
    provider_reference text NOT NULL CHECK (length(provider_reference) BETWEEN 1 AND 255),
    incident_kind text NOT NULL CHECK (incident_kind IN ('orphan_cancelled','recovery_exhausted','provider_paid_without_callback','provider_refunded_without_callback','provider_callback_quarantined')),
    evidence_digest bytea CHECK (evidence_digest IS NULL OR octet_length(evidence_digest)=32),
    status text NOT NULL CHECK (status IN ('open','resolved')),
    created_at timestamptz NOT NULL,
    resolved_at timestamptz,
    version bigint NOT NULL DEFAULT 1 CHECK (version>0),
    FOREIGN KEY (create_attempt_id) REFERENCES hosted_provider_create_attempts(id) ON DELETE RESTRICT,
    FOREIGN KEY (provider_order_id,tenant_id,merchant_id) REFERENCES provider_orders(id,tenant_id,merchant_id) ON DELETE RESTRICT,
    FOREIGN KEY (provider_prebind_id,tenant_id,merchant_id) REFERENCES provider_prebind_inbox(id,tenant_id,merchant_id) ON DELETE RESTRICT,
    FOREIGN KEY (provider_id,tenant_id,merchant_id) REFERENCES hosted_provider_configs(id,tenant_id,merchant_id) ON DELETE RESTRICT,
    UNIQUE (create_attempt_id,incident_kind),
    UNIQUE (provider_order_id,incident_kind,evidence_digest),
    UNIQUE (provider_prebind_id,incident_kind),
    CHECK ((create_attempt_id IS NOT NULL)::integer+(provider_order_id IS NOT NULL)::integer+(provider_prebind_id IS NOT NULL)::integer=1),
    CHECK ((status='resolved')=(resolved_at IS NOT NULL))
);

ALTER TABLE payment_matches ALTER COLUMN event_id DROP NOT NULL;
ALTER TABLE payment_matches ADD COLUMN provider_inbox_id uuid REFERENCES provider_inbox(id) ON DELETE RESTRICT;
DROP INDEX payment_matches_event_active_idx;
CREATE UNIQUE INDEX payment_matches_event_active_idx ON payment_matches(event_id)
  WHERE state<>'reversed' AND event_id IS NOT NULL;
CREATE UNIQUE INDEX payment_matches_provider_inbox_active_idx ON payment_matches(provider_inbox_id)
  WHERE state<>'reversed' AND provider_inbox_id IS NOT NULL;
ALTER TABLE payment_matches ADD CONSTRAINT payment_matches_source_xor
  CHECK ((event_id IS NOT NULL)::integer + (provider_inbox_id IS NOT NULL)::integer = 1);

ALTER TABLE hosted_provider_configs ENABLE ROW LEVEL SECURITY;
ALTER TABLE hosted_provider_create_attempts ENABLE ROW LEVEL SECURITY;
ALTER TABLE provider_orders ENABLE ROW LEVEL SECURITY;
ALTER TABLE provider_inbox ENABLE ROW LEVEL SECURITY;
ALTER TABLE provider_reconciliation_incidents ENABLE ROW LEVEL SECURITY;
ALTER TABLE provider_prebind_inbox ENABLE ROW LEVEL SECURITY;
ALTER TABLE provider_reconcile_observations ENABLE ROW LEVEL SECURITY;
ALTER TABLE hosted_provider_runtime_incidents ENABLE ROW LEVEL SECURITY;
ALTER TABLE hosted_provider_configs FORCE ROW LEVEL SECURITY;
ALTER TABLE hosted_provider_create_attempts FORCE ROW LEVEL SECURITY;
ALTER TABLE provider_orders FORCE ROW LEVEL SECURITY;
ALTER TABLE provider_inbox FORCE ROW LEVEL SECURITY;
ALTER TABLE provider_reconciliation_incidents FORCE ROW LEVEL SECURITY;
ALTER TABLE provider_prebind_inbox FORCE ROW LEVEL SECURITY;
ALTER TABLE provider_reconcile_observations FORCE ROW LEVEL SECURITY;
ALTER TABLE hosted_provider_runtime_incidents FORCE ROW LEVEL SECURITY;

CREATE POLICY hosted_provider_config_scope ON hosted_provider_configs
  USING (tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid
    AND merchant_id=nullif(current_setting('app.merchant_id',true),'')::uuid)
  WITH CHECK (tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid
    AND merchant_id=nullif(current_setting('app.merchant_id',true),'')::uuid);
CREATE POLICY hosted_provider_create_scope ON hosted_provider_create_attempts
  USING (tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid
    AND merchant_id=nullif(current_setting('app.merchant_id',true),'')::uuid)
  WITH CHECK (tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid
    AND merchant_id=nullif(current_setting('app.merchant_id',true),'')::uuid);
CREATE POLICY provider_order_scope ON provider_orders
  USING (tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid
    AND merchant_id=nullif(current_setting('app.merchant_id',true),'')::uuid)
  WITH CHECK (tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid
    AND merchant_id=nullif(current_setting('app.merchant_id',true),'')::uuid);
CREATE POLICY provider_inbox_scope ON provider_inbox
  USING (tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid
    AND merchant_id=nullif(current_setting('app.merchant_id',true),'')::uuid)
  WITH CHECK (tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid
    AND merchant_id=nullif(current_setting('app.merchant_id',true),'')::uuid);
CREATE POLICY provider_reconciliation_incident_scope ON provider_reconciliation_incidents
  USING (tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid
    AND merchant_id=nullif(current_setting('app.merchant_id',true),'')::uuid)
  WITH CHECK (tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid
    AND merchant_id=nullif(current_setting('app.merchant_id',true),'')::uuid);
CREATE POLICY provider_prebind_inbox_scope ON provider_prebind_inbox
  USING (tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid
    AND merchant_id=nullif(current_setting('app.merchant_id',true),'')::uuid)
  WITH CHECK (tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid
    AND merchant_id=nullif(current_setting('app.merchant_id',true),'')::uuid);
CREATE POLICY provider_reconcile_observation_scope ON provider_reconcile_observations
  USING (tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid
    AND merchant_id=nullif(current_setting('app.merchant_id',true),'')::uuid)
  WITH CHECK (tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid
    AND merchant_id=nullif(current_setting('app.merchant_id',true),'')::uuid);
CREATE POLICY hosted_provider_runtime_incident_scope ON hosted_provider_runtime_incidents
  USING (tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid
    AND merchant_id=nullif(current_setting('app.merchant_id',true),'')::uuid)
  WITH CHECK (tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid
    AND merchant_id=nullif(current_setting('app.merchant_id',true),'')::uuid);

CREATE FUNCTION provider_inbox_reject_mutation() RETURNS trigger
LANGUAGE plpgsql SET search_path=pg_catalog,public AS $$
BEGIN RAISE EXCEPTION 'verified provider inbox is append-only' USING ERRCODE='55000'; END $$;
CREATE TRIGGER provider_inbox_immutable BEFORE UPDATE OR DELETE ON provider_inbox
  FOR EACH ROW EXECUTE FUNCTION provider_inbox_reject_mutation();
CREATE TRIGGER provider_reconcile_observation_immutable BEFORE UPDATE OR DELETE ON provider_reconcile_observations
  FOR EACH ROW EXECUTE FUNCTION provider_inbox_reject_mutation();
CREATE FUNCTION provider_prebind_reject_evidence_mutation() RETURNS trigger
LANGUAGE plpgsql SET search_path=pg_catalog,public AS $$
BEGIN
  IF (to_jsonb(NEW)-ARRAY['state','attached_provider_inbox_id','claim_token','claim_until','attempt_count','next_attempt_at','last_error_code','updated_at','version'])
     IS DISTINCT FROM
     (to_jsonb(OLD)-ARRAY['state','attached_provider_inbox_id','claim_token','claim_until','attempt_count','next_attempt_at','last_error_code','updated_at','version']) THEN
    RAISE EXCEPTION 'provider pre-bind evidence is immutable' USING ERRCODE='55000';
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER provider_prebind_evidence_immutable BEFORE UPDATE ON provider_prebind_inbox
  FOR EACH ROW EXECUTE FUNCTION provider_prebind_reject_evidence_mutation();
CREATE TRIGGER provider_prebind_reject_delete BEFORE DELETE ON provider_prebind_inbox
  FOR EACH ROW EXECUTE FUNCTION provider_inbox_reject_mutation();

-- Provider lifecycle workers may advance status/lease metadata, but economic
-- and provider-create evidence is frozen after insertion.
CREATE FUNCTION provider_order_reject_economic_mutation() RETURNS trigger
LANGUAGE plpgsql SET search_path=pg_catalog,public AS $$
BEGIN
  IF (to_jsonb(NEW)-ARRAY['provider_status','last_provider_occurred_at','last_provider_event_id','reconcile_claim_token','reconcile_claim_until','reconcile_attempt_count','next_reconcile_at','last_reconcile_error_code','updated_at','version'])
     IS DISTINCT FROM
     (to_jsonb(OLD)-ARRAY['provider_status','last_provider_occurred_at','last_provider_event_id','reconcile_claim_token','reconcile_claim_until','reconcile_attempt_count','next_reconcile_at','last_reconcile_error_code','updated_at','version']) THEN
    RAISE EXCEPTION 'provider order economic and create evidence is immutable' USING ERRCODE='55000';
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER provider_order_economic_immutable BEFORE UPDATE ON provider_orders
  FOR EACH ROW EXECUTE FUNCTION provider_order_reject_economic_mutation();

-- Callback admission happens before a tenant is known. This narrow definer
-- exposes non-secret metadata only and admits active live/test merchants;
-- credential material remains in the external secret mount.
CREATE FUNCTION hosted_provider_callback_admitted(requested_provider_id text)
RETURNS TABLE (
  id text,tenant_id uuid,merchant_id uuid,adapter_kind text,api_origin text,
  create_path text,cancel_path text,status_path text,refund_path text,reconcile_path text,
  payment_url_origins text[],api_credential_ref text,api_key_id text,callback_secret_ref text,
  callback_key_id text,signature_scheme text,asset_id text,asset_decimals smallint,currency text,status text
)
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path=pg_catalog,public SET row_security=off AS $$
SELECT c.id,c.tenant_id,c.merchant_id,c.adapter_kind,c.api_origin,c.create_path,c.cancel_path,c.status_path,c.refund_path,c.reconcile_path,
       c.payment_url_origins,c.api_credential_ref,c.api_key_id,c.callback_secret_ref,c.callback_key_id,c.signature_scheme,c.asset_id,c.asset_decimals,c.currency::text,c.status
FROM public.hosted_provider_configs c
JOIN public.tenants t ON t.id=c.tenant_id AND t.status='active'
JOIN public.merchants m ON m.id=c.merchant_id AND m.tenant_id=c.tenant_id AND m.status='active'
WHERE c.id=requested_provider_id AND c.status='active'
$$;
REVOKE ALL ON FUNCTION hosted_provider_callback_admitted(text) FROM PUBLIC;

-- Cross-tenant recovery discovery is intentionally exposed only through
-- narrowly fenced claim functions. The worker never performs an unscoped
-- SELECT/UPDATE against FORCE-RLS hosted tables; every returned row carries
-- its tenant, merchant, and a fresh claim token for subsequent mutations.
CREATE FUNCTION claim_hosted_create_recoveries(claim_now timestamptz,lease_until timestamptz,claim_limit integer)
RETURNS TABLE (
  id uuid,claim_token uuid,attempt integer,tenant_id uuid,merchant_id uuid,provider_id text,intent_id uuid,
  idempotency_key text,request_hash text,asset_id text,fiat_amount_minor text,currency text,currency_scale smallint,
  expires_at timestamptz,create_state text,provider_order_id text,provider_reference text,payment_url text,
  amount_atomic text,asset_decimals smallint,quote_id text,rate_numerator text,rate_denominator text,
  quote_issued_at timestamptz,create_response_body bytea,create_response_digest bytea,create_response_received_at timestamptz
)
LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,public SET row_security=off AS $$
BEGIN
  IF claim_limit<1 OR claim_limit>100 OR lease_until<=claim_now OR lease_until>claim_now+interval '5 minutes' THEN
    RAISE EXCEPTION 'invalid hosted recovery claim bounds' USING ERRCODE='22023';
  END IF;
  RETURN QUERY
  WITH picked AS (
    SELECT a.id FROM public.hosted_provider_create_attempts a
    JOIN public.payment_intents i ON i.id=a.intent_id AND i.tenant_id=a.tenant_id AND i.merchant_id=a.merchant_id
    WHERE a.recovery_status='pending' AND a.next_recovery_at<=claim_now
      AND (a.state IN('retry','completed') OR a.state='claimed' AND a.claim_until<claim_now)
    ORDER BY a.next_recovery_at,a.id FOR UPDATE OF a SKIP LOCKED LIMIT claim_limit
  ), claimed AS (
    UPDATE public.hosted_provider_create_attempts a
    SET recovery_status='claimed',recovery_claim_token=gen_random_uuid(),recovery_claim_until=lease_until,
        recovery_attempt_count=a.recovery_attempt_count+1,updated_at=claim_now,version=a.version+1
    FROM picked WHERE a.id=picked.id RETURNING a.*
  )
  SELECT a.id,a.recovery_claim_token,a.recovery_attempt_count,a.tenant_id,a.merchant_id,a.provider_id,a.intent_id,
    a.idempotency_key,encode(a.request_hash,'hex'),a.asset_id,a.fiat_amount_minor::text,a.currency::text,a.currency_scale,
    a.expires_at,a.state,COALESCE(a.provider_order_id::text,''),COALESCE(a.provider_reference,''),COALESCE(a.payment_url,''),
    COALESCE(a.amount_atomic::text,''),COALESCE(a.asset_decimals,0),COALESCE(a.quote_id,''),
    COALESCE(a.rate_numerator::text,''),COALESCE(a.rate_denominator::text,''),a.quote_issued_at,
    COALESCE(a.create_response_body,''::bytea),COALESCE(a.create_response_digest,''::bytea),a.create_response_received_at
  FROM claimed a ORDER BY a.next_recovery_at,a.id;
END $$;
REVOKE ALL ON FUNCTION claim_hosted_create_recoveries(timestamptz,timestamptz,integer) FROM PUBLIC;

CREATE FUNCTION claim_hosted_prebind_recoveries(claim_now timestamptz,lease_until timestamptz,claim_limit integer)
RETURNS TABLE (
  id uuid,claim_token uuid,attempt integer,tenant_id uuid,merchant_id uuid,provider_id text,route_id text,
  provider_event_id text,provider_reference text,provider_status text,asset_id text,amount_atomic text,
  asset_decimals smallint,raw_body bytea,raw_body_digest bytea,signature_scheme text,signature_key_id text,
  signature_digest bytea,provider_paused_at_receipt boolean,provider_occurred_at timestamptz,received_at timestamptz
)
LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,public SET row_security=off AS $$
BEGIN
  IF claim_limit<1 OR claim_limit>100 OR lease_until<=claim_now OR lease_until>claim_now+interval '5 minutes' THEN
    RAISE EXCEPTION 'invalid hosted recovery claim bounds' USING ERRCODE='22023';
  END IF;
  RETURN QUERY
  WITH picked AS (
    SELECT p.id FROM public.provider_prebind_inbox p
    LEFT JOIN public.provider_orders o ON o.provider_id=p.provider_id AND o.provider_reference=p.provider_reference
      AND o.tenant_id=p.tenant_id AND o.merchant_id=p.merchant_id
    WHERE p.state='pending' AND p.next_attempt_at<=claim_now AND(p.claim_token IS NULL OR p.claim_until<claim_now)
      AND(o.id IS NOT NULL OR p.expires_at<=claim_now)
    ORDER BY CASE WHEN o.id IS NOT NULL THEN 0 ELSE 1 END,p.next_attempt_at,p.id
    FOR UPDATE OF p SKIP LOCKED LIMIT claim_limit
  ), claimed AS (
    UPDATE public.provider_prebind_inbox p
    SET claim_token=gen_random_uuid(),claim_until=lease_until,attempt_count=p.attempt_count+1,
        updated_at=claim_now,version=p.version+1
    FROM picked WHERE p.id=picked.id RETURNING p.*
  )
  SELECT p.id,p.claim_token,p.attempt_count,p.tenant_id,p.merchant_id,p.provider_id,
    COALESCE((SELECT o.route_id::text FROM public.provider_orders o WHERE o.provider_id=p.provider_id
      AND o.provider_reference=p.provider_reference AND o.tenant_id=p.tenant_id AND o.merchant_id=p.merchant_id),''),
    p.provider_event_id,p.provider_reference,p.provider_status,p.asset_id,p.amount_atomic::text,p.asset_decimals,
    p.raw_body,p.raw_body_digest,p.signature_scheme,p.signature_key_id,p.signature_digest,
    p.provider_paused_at_receipt,p.provider_occurred_at,p.received_at
  FROM claimed p ORDER BY p.next_attempt_at,p.id;
END $$;
REVOKE ALL ON FUNCTION claim_hosted_prebind_recoveries(timestamptz,timestamptz,integer) FROM PUBLIC;

CREATE FUNCTION claim_hosted_order_recoveries(claim_now timestamptz,lease_until timestamptz,claim_limit integer)
RETURNS TABLE (
  id uuid,claim_token uuid,attempt integer,tenant_id uuid,merchant_id uuid,route_id uuid,provider_id text,
  provider_reference text,asset_id text,amount_atomic text,asset_decimals smallint
)
LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,public SET row_security=off AS $$
BEGIN
  IF claim_limit<1 OR claim_limit>100 OR lease_until<=claim_now OR lease_until>claim_now+interval '5 minutes' THEN
    RAISE EXCEPTION 'invalid hosted recovery claim bounds' USING ERRCODE='22023';
  END IF;
  RETURN QUERY
  WITH picked AS (
    SELECT o.id FROM public.provider_orders o
    WHERE o.provider_status IN('pending','authorized','cancel_requested') AND o.next_reconcile_at<=claim_now
      AND(o.reconcile_claim_token IS NULL OR o.reconcile_claim_until<claim_now)
    ORDER BY o.next_reconcile_at,o.id FOR UPDATE OF o SKIP LOCKED LIMIT claim_limit
  ), claimed AS (
    UPDATE public.provider_orders o
    SET reconcile_claim_token=gen_random_uuid(),reconcile_claim_until=lease_until,
        reconcile_attempt_count=o.reconcile_attempt_count+1,updated_at=claim_now,version=o.version+1
    FROM picked WHERE o.id=picked.id RETURNING o.*
  )
  SELECT o.id,o.reconcile_claim_token,o.reconcile_attempt_count,o.tenant_id,o.merchant_id,o.route_id,
    o.provider_id,o.provider_reference,o.asset_id,o.amount_atomic::text,o.asset_decimals
  FROM claimed o ORDER BY o.next_reconcile_at,o.id;
END $$;
REVOKE ALL ON FUNCTION claim_hosted_order_recoveries(timestamptz,timestamptz,integer) FROM PUBLIC;

DO $$ BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='merchant_api_runtime') THEN
	GRANT SELECT ON hosted_provider_configs TO merchant_api_runtime;
	GRANT SELECT,INSERT,UPDATE ON hosted_provider_create_attempts,provider_orders TO merchant_api_runtime;
    GRANT SELECT,INSERT ON provider_inbox TO merchant_api_runtime;
    GRANT SELECT,INSERT,UPDATE ON provider_prebind_inbox TO merchant_api_runtime;
    GRANT SELECT,INSERT ON provider_reconciliation_incidents,provider_reconcile_observations,hosted_provider_runtime_incidents TO merchant_api_runtime;
    GRANT EXECUTE ON FUNCTION hosted_provider_callback_admitted(text) TO merchant_api_runtime;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='merchant_plan_worker') THEN
    GRANT SELECT ON hosted_provider_configs,provider_reconciliation_incidents,provider_reconcile_observations,hosted_provider_runtime_incidents TO merchant_plan_worker;
    GRANT SELECT,UPDATE ON hosted_provider_create_attempts,provider_orders,provider_prebind_inbox TO merchant_plan_worker;
    GRANT INSERT ON provider_orders,provider_reconcile_observations,hosted_provider_runtime_incidents,payment_routes,idempotency_records TO merchant_plan_worker;
    GRANT EXECUTE ON FUNCTION claim_hosted_create_recoveries(timestamptz,timestamptz,integer),claim_hosted_prebind_recoveries(timestamptz,timestamptz,integer),claim_hosted_order_recoveries(timestamptz,timestamptz,integer) TO merchant_plan_worker;
  END IF;
END $$;

COMMIT;
