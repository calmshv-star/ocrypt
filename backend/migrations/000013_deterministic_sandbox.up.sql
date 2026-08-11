BEGIN;

-- The deterministic merchant sandbox is physically separate from production
-- payment, route, callback and idempotency tables. Only the workspace has an
-- outward foreign key, and every cascade target remains inside sandbox_*.
CREATE TABLE sandbox_workspaces (
    tenant_id uuid NOT NULL,
    merchant_id uuid NOT NULL,
    test_clock timestamptz NOT NULL,
    credential_key_id text NOT NULL CHECK (credential_key_id LIKE 'mk_test_%'),
    addresses jsonb NOT NULL CHECK (jsonb_typeof(addresses)='array' AND pg_column_size(addresses)<=16384),
    version bigint NOT NULL CHECK (version>0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id,merchant_id),
    FOREIGN KEY (merchant_id,tenant_id) REFERENCES merchants(id,tenant_id)
);

CREATE FUNCTION sandbox_test_credential_admitted(requested_tenant uuid,requested_merchant uuid,requested_key_id text)
RETURNS boolean
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path=pg_catalog,public
SET row_security=off AS $$
    SELECT EXISTS (
        SELECT 1
          FROM public.tenants t
          JOIN public.merchants m ON m.tenant_id=t.id
          JOIN public.api_clients c ON c.tenant_id=m.tenant_id AND c.merchant_id=m.id
         WHERE t.id=requested_tenant AND t.status='active'
           AND m.id=requested_merchant AND m.status='active' AND m.environment='test'
           AND c.key_id=requested_key_id AND c.key_id LIKE 'mk_test_%'
           AND c.revoked_at IS NULL AND c.valid_from<=clock_timestamp()
           AND (c.valid_until IS NULL OR c.valid_until>clock_timestamp())
    )
$$;
REVOKE ALL ON FUNCTION sandbox_test_credential_admitted(uuid,uuid,text) FROM PUBLIC;

-- Defense in depth: no application role can create a workspace for a live or
-- disabled merchant even if the Go admission query is accidentally removed.
CREATE FUNCTION enforce_sandbox_test_merchant() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,public
SET row_security=off AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM public.tenants t
        JOIN public.merchants m ON m.tenant_id=t.id
        JOIN public.api_clients c ON c.tenant_id=m.tenant_id AND c.merchant_id=m.id
        WHERE t.id=NEW.tenant_id AND t.status='active'
          AND m.id=NEW.merchant_id AND m.status='active' AND m.environment='test'
          AND c.key_id=NEW.credential_key_id AND c.key_id LIKE 'mk_test_%'
          AND c.revoked_at IS NULL AND c.valid_from<=clock_timestamp()
          AND (c.valid_until IS NULL OR c.valid_until>clock_timestamp())
    ) THEN
        RAISE EXCEPTION 'sandbox workspace requires an active test merchant' USING ERRCODE='42501';
    END IF;
    RETURN NEW;
END $$;
REVOKE ALL ON FUNCTION enforce_sandbox_test_merchant() FROM PUBLIC;
CREATE TRIGGER sandbox_workspace_test_merchant
BEFORE INSERT OR UPDATE OF tenant_id,merchant_id,credential_key_id ON sandbox_workspaces
FOR EACH ROW EXECUTE FUNCTION enforce_sandbox_test_merchant();

CREATE TABLE sandbox_scenarios (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    merchant_id uuid NOT NULL,
    kind text NOT NULL CHECK (kind IN (
        'exact_payment','partial_payment','underpayment','overpayment','late_payment','wrong_asset',
        'duplicate_callback','out_of_order_callback','timeout','dead_letter','reorg','reorg_recovery'
    )),
    merchant_order_id text NOT NULL CHECK (length(merchant_order_id) BETWEEN 1 AND 128),
    payment_intent_id uuid NOT NULL,
    payment_route_id uuid NOT NULL,
    intent_snapshot jsonb NOT NULL CHECK (jsonb_typeof(intent_snapshot)='object' AND pg_column_size(intent_snapshot)<=32768),
    route_snapshot jsonb NOT NULL CHECK (jsonb_typeof(route_snapshot)='object' AND pg_column_size(route_snapshot)<=32768),
    observed_amount_atomic uint256 NOT NULL DEFAULT 0,
    observed_asset_id text NOT NULL DEFAULT '',
    confirmations bigint NOT NULL DEFAULT 0 CHECK (confirmations BETWEEN 0 AND 1000000),
    finalized boolean NOT NULL DEFAULT false,
    last_event_sequence bigint NOT NULL DEFAULT 0 CHECK (last_event_sequence>=0),
    version bigint NOT NULL CHECK (version>0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    UNIQUE (merchant_id,merchant_order_id),
    UNIQUE (tenant_id,merchant_id,payment_intent_id),
    UNIQUE (tenant_id,merchant_id,payment_route_id),
    UNIQUE (id,tenant_id,merchant_id),
    FOREIGN KEY (tenant_id,merchant_id) REFERENCES sandbox_workspaces(tenant_id,merchant_id)
);
CREATE INDEX sandbox_scenarios_page_idx ON sandbox_scenarios (tenant_id,merchant_id,created_at DESC,id DESC);

CREATE TABLE sandbox_events (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    merchant_id uuid NOT NULL,
    scenario_id uuid NOT NULL,
    sequence bigint NOT NULL CHECK (sequence>0),
    event_type text NOT NULL CHECK (length(event_type) BETWEEN 1 AND 128),
    payload jsonb NOT NULL CHECK (jsonb_typeof(payload)='object' AND pg_column_size(payload)<=32768),
    occurred_at timestamptz NOT NULL,
    UNIQUE (scenario_id,sequence),
    UNIQUE (id,tenant_id,merchant_id),
    FOREIGN KEY (scenario_id,tenant_id,merchant_id) REFERENCES sandbox_scenarios(id,tenant_id,merchant_id) ON DELETE CASCADE
);

CREATE TABLE sandbox_callbacks (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    merchant_id uuid NOT NULL,
    scenario_id uuid NOT NULL,
    event_id uuid NOT NULL,
    event_sequence bigint NOT NULL CHECK (event_sequence>0),
    status text NOT NULL CHECK (status IN ('pending','retry','acknowledged','dead_letter')),
    canonical_body bytea NOT NULL CHECK (octet_length(canonical_body) BETWEEN 2 AND 65536),
    body_sha256 bytea NOT NULL CHECK (octet_length(body_sha256)=32),
    attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count BETWEEN 0 AND 100),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    version bigint NOT NULL CHECK (version>0),
    UNIQUE (scenario_id,event_id),
    UNIQUE (id,tenant_id,merchant_id),
    FOREIGN KEY (scenario_id,tenant_id,merchant_id) REFERENCES sandbox_scenarios(id,tenant_id,merchant_id) ON DELETE CASCADE,
    FOREIGN KEY (event_id,tenant_id,merchant_id) REFERENCES sandbox_events(id,tenant_id,merchant_id) ON DELETE CASCADE
);
CREATE INDEX sandbox_callbacks_page_idx ON sandbox_callbacks (tenant_id,merchant_id,created_at DESC,id DESC);

CREATE TABLE sandbox_callback_attempts (
    callback_id uuid NOT NULL,
    tenant_id uuid NOT NULL,
    merchant_id uuid NOT NULL,
    attempt_number integer NOT NULL CHECK (attempt_number BETWEEN 1 AND 100),
    outcome text NOT NULL CHECK (outcome IN ('delivered','failed','timeout')),
    http_status integer CHECK (http_status BETWEEN 100 AND 599),
    error_category text NOT NULL DEFAULT '' CHECK (error_category IN ('','timeout','http_3xx','http_4xx','http_5xx','http_error')),
    response_bytes integer NOT NULL DEFAULT 0 CHECK (response_bytes BETWEEN 0 AND 4096),
    attempted_at timestamptz NOT NULL,
    PRIMARY KEY (callback_id,attempt_number),
    FOREIGN KEY (callback_id,tenant_id,merchant_id) REFERENCES sandbox_callbacks(id,tenant_id,merchant_id) ON DELETE CASCADE
);

CREATE TABLE sandbox_idempotency (
    tenant_id uuid NOT NULL,
    merchant_id uuid NOT NULL,
    operation text NOT NULL CHECK (length(operation) BETWEEN 1 AND 128),
    idempotency_key text NOT NULL CHECK (length(idempotency_key) BETWEEN 8 AND 255),
    request_hash bytea NOT NULL CHECK (octet_length(request_hash)=32),
    resource_type text NOT NULL CHECK (resource_type IN ('scenario','workspace','reset')),
    resource_id uuid,
    response_body jsonb NOT NULL CHECK (jsonb_typeof(response_body)='object' AND pg_column_size(response_body)<=262144),
    created_at timestamptz NOT NULL,
    PRIMARY KEY (merchant_id,operation,idempotency_key),
    FOREIGN KEY (tenant_id,merchant_id) REFERENCES sandbox_workspaces(tenant_id,merchant_id)
);

ALTER TABLE sandbox_workspaces ENABLE ROW LEVEL SECURITY;
ALTER TABLE sandbox_scenarios ENABLE ROW LEVEL SECURITY;
ALTER TABLE sandbox_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE sandbox_callbacks ENABLE ROW LEVEL SECURITY;
ALTER TABLE sandbox_callback_attempts ENABLE ROW LEVEL SECURITY;
ALTER TABLE sandbox_idempotency ENABLE ROW LEVEL SECURITY;
ALTER TABLE sandbox_workspaces FORCE ROW LEVEL SECURITY;
ALTER TABLE sandbox_scenarios FORCE ROW LEVEL SECURITY;
ALTER TABLE sandbox_events FORCE ROW LEVEL SECURITY;
ALTER TABLE sandbox_callbacks FORCE ROW LEVEL SECURITY;
ALTER TABLE sandbox_callback_attempts FORCE ROW LEVEL SECURITY;
ALTER TABLE sandbox_idempotency FORCE ROW LEVEL SECURITY;

CREATE POLICY sandbox_workspaces_tenant_policy ON sandbox_workspaces
    USING (tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid AND merchant_id=nullif(current_setting('app.merchant_id',true),'')::uuid)
    WITH CHECK (tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid AND merchant_id=nullif(current_setting('app.merchant_id',true),'')::uuid);
CREATE POLICY sandbox_scenarios_tenant_policy ON sandbox_scenarios
    USING (tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid AND merchant_id=nullif(current_setting('app.merchant_id',true),'')::uuid)
    WITH CHECK (tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid AND merchant_id=nullif(current_setting('app.merchant_id',true),'')::uuid);
CREATE POLICY sandbox_events_tenant_policy ON sandbox_events
    USING (tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid AND merchant_id=nullif(current_setting('app.merchant_id',true),'')::uuid)
    WITH CHECK (tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid AND merchant_id=nullif(current_setting('app.merchant_id',true),'')::uuid);
CREATE POLICY sandbox_callbacks_tenant_policy ON sandbox_callbacks
    USING (tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid AND merchant_id=nullif(current_setting('app.merchant_id',true),'')::uuid)
    WITH CHECK (tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid AND merchant_id=nullif(current_setting('app.merchant_id',true),'')::uuid);
CREATE POLICY sandbox_callback_attempts_tenant_policy ON sandbox_callback_attempts
    USING (tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid AND merchant_id=nullif(current_setting('app.merchant_id',true),'')::uuid)
    WITH CHECK (tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid AND merchant_id=nullif(current_setting('app.merchant_id',true),'')::uuid);
CREATE POLICY sandbox_idempotency_tenant_policy ON sandbox_idempotency
    USING (tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid AND merchant_id=nullif(current_setting('app.merchant_id',true),'')::uuid)
    WITH CHECK (tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid AND merchant_id=nullif(current_setting('app.merchant_id',true),'')::uuid);

-- Least privilege for merchant_api_runtime. Reset owns only the DELETE on
-- sandbox_scenarios (internal cascades) and sandbox_idempotency; no DELETE is
-- granted on the workspace or any production table.
DO $$ BEGIN
  IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='merchant_api_runtime') THEN
    GRANT EXECUTE ON FUNCTION sandbox_test_credential_admitted(uuid,uuid,text) TO merchant_api_runtime;
    GRANT SELECT,INSERT,UPDATE ON sandbox_workspaces TO merchant_api_runtime;
    GRANT SELECT,INSERT,UPDATE,DELETE ON sandbox_scenarios TO merchant_api_runtime;
    GRANT SELECT,INSERT ON sandbox_events TO merchant_api_runtime;
    GRANT SELECT,INSERT,UPDATE ON sandbox_callbacks TO merchant_api_runtime;
    GRANT SELECT,INSERT ON sandbox_callback_attempts TO merchant_api_runtime;
    GRANT SELECT,INSERT,DELETE ON sandbox_idempotency TO merchant_api_runtime;
  END IF;
END $$;

COMMIT;
