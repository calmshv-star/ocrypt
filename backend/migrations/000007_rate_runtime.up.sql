BEGIN;

-- Workload identities are provisioned by operations. The application login is
-- expected to inherit the optional rate_runtime_worker role granted below.
CREATE TABLE rate_runtime_identities (
    id uuid PRIMARY KEY,
    name text NOT NULL UNIQUE CHECK(length(name) BETWEEN 3 AND 128),
    purpose text NOT NULL DEFAULT 'rate_collection' CHECK(purpose='rate_collection'),
    enabled boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE TABLE rate_runtime_jobs (
    scope_id uuid NOT NULL,
    tenant_id uuid,
    policy_key text NOT NULL CHECK(policy_key ~ '^[a-z0-9][a-z0-9._:/-]{0,126}[a-z0-9]$'),
    status text NOT NULL DEFAULT 'active' CHECK(status IN ('active','dead_letter')),
    lease_owner uuid REFERENCES rate_runtime_identities(id),
    lease_until timestamptz,
    claim_token bigint NOT NULL DEFAULT 0 CHECK(claim_token>=0),
    attempts integer NOT NULL DEFAULT 0 CHECK(attempts>=0),
    next_attempt_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    last_error_code text CHECK(last_error_code IS NULL OR last_error_code IN
      ('invalid_config','no_quorum','stale','future_timestamp','divergent','identity_disabled','dependency_unavailable')),
    last_success_at timestamptz,
    dead_lettered_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK(scope_id=platform_scope_uuid(tenant_id)),
    CHECK((lease_owner IS NULL)=(lease_until IS NULL)),
    CHECK((status='dead_letter')=(dead_lettered_at IS NOT NULL)),
    PRIMARY KEY(scope_id,policy_key),
    FOREIGN KEY(tenant_id) REFERENCES tenants(id)
);
CREATE INDEX rate_runtime_jobs_due_idx ON rate_runtime_jobs(next_attempt_at,scope_id,policy_key)
WHERE status='active';

-- PersistedPlanner has a global asset/fiat contract. One stable policy logical
-- key owns each pair; changing a policy to another pair requires a new key and
-- an explicit operations migration, preventing two workers from oscillating it.
CREATE TABLE rate_runtime_pair_bindings (
    base_asset text NOT NULL REFERENCES assets(id),
    quote_asset char(3) NOT NULL CHECK(quote_asset ~ '^[A-Z]{3}$'),
    policy_key text NOT NULL UNIQUE CHECK(policy_key ~ '^[a-z0-9][a-z0-9._:/-]{0,126}[a-z0-9]$'),
    scope_id uuid NOT NULL DEFAULT platform_scope_uuid(NULL) CHECK(scope_id=platform_scope_uuid(NULL)),
    policy_config_kind platform_config_kind NOT NULL DEFAULT 'rate_policy' CHECK(policy_config_kind='rate_policy'),
    first_policy_snapshot_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY(base_asset,quote_asset),
    FOREIGN KEY(first_policy_snapshot_id,scope_id,policy_config_kind,policy_key)
      REFERENCES platform_config_snapshots(id,scope_id,kind,logical_key)
);

CREATE TABLE rate_source_observations (
    id uuid PRIMARY KEY,
    scope_id uuid NOT NULL,
    tenant_id uuid,
    policy_key text NOT NULL,
    source_key text NOT NULL,
    provider_ref text NOT NULL CHECK(length(provider_ref) BETWEEN 1 AND 128),
    provider_observation_id text NOT NULL CHECK(length(provider_observation_id) BETWEEN 1 AND 256),
    base_asset text NOT NULL CHECK(base_asset ~ '^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$'),
    quote_asset text NOT NULL CHECK(quote_asset ~ '^[A-Z]{3}$'),
    price_numerator numeric(78,0) NOT NULL CHECK(price_numerator>0 AND scale(price_numerator)=0),
    price_denominator numeric(78,0) NOT NULL CHECK(price_denominator>0 AND scale(price_denominator)=0),
    provider_observed_at timestamptz NOT NULL,
    received_at timestamptz NOT NULL,
    raw_response_hash bytea NOT NULL CHECK(octet_length(raw_response_hash)=32),
    source_config_kind platform_config_kind NOT NULL DEFAULT 'rate_source' CHECK(source_config_kind='rate_source'),
    rate_source_snapshot_id uuid NOT NULL,
    source_fence_token bigint NOT NULL CHECK(source_fence_token>0),
    CHECK(scope_id=platform_scope_uuid(tenant_id)),
    CHECK(base_asset<>quote_asset),
    CHECK(provider_observed_at<=received_at+interval '60 seconds'),
    UNIQUE(id,scope_id),
    FOREIGN KEY(tenant_id) REFERENCES tenants(id),
    FOREIGN KEY(rate_source_snapshot_id,scope_id,source_config_kind,source_key)
      REFERENCES platform_config_snapshots(id,scope_id,kind,logical_key)
);
CREATE INDEX rate_observations_pair_time_idx
ON rate_source_observations(scope_id,base_asset,quote_asset,provider_observed_at DESC,id DESC);

-- This immutable admission ledger is separate from the pre-existing
-- asset_rate_ticks planner projection created by migration 000001.
CREATE TABLE admitted_rate_ticks (
    id uuid PRIMARY KEY,
    scope_id uuid NOT NULL,
    tenant_id uuid,
    policy_key text NOT NULL,
    base_asset text NOT NULL CHECK(base_asset ~ '^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$'),
    quote_asset text NOT NULL CHECK(quote_asset ~ '^[A-Z]{3}$'),
    price_numerator numeric(78,0) NOT NULL CHECK(price_numerator>0 AND scale(price_numerator)=0),
    price_denominator numeric(78,0) NOT NULL CHECK(price_denominator>0 AND scale(price_denominator)=0),
    observed_at timestamptz NOT NULL,
    admitted_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    spread_bps integer NOT NULL CHECK(spread_bps BETWEEN 0 AND 10000),
    quorum integer NOT NULL CHECK(quorum BETWEEN 2 AND 32),
    source_count integer NOT NULL CHECK(source_count BETWEEN 2 AND 32 AND source_count>=quorum),
    policy_config_kind platform_config_kind NOT NULL DEFAULT 'rate_policy' CHECK(policy_config_kind='rate_policy'),
    rate_policy_snapshot_id uuid NOT NULL,
    policy_fence_token bigint NOT NULL CHECK(policy_fence_token>0),
    sources_digest bytea NOT NULL CHECK(octet_length(sources_digest)=32),
    CHECK(scope_id=platform_scope_uuid(tenant_id)),
    CHECK(base_asset<>quote_asset),
    CHECK(observed_at<=admitted_at+interval '60 seconds'),
    CHECK(expires_at>observed_at),
    UNIQUE(id,scope_id),
    FOREIGN KEY(tenant_id) REFERENCES tenants(id),
    FOREIGN KEY(id) REFERENCES asset_rate_ticks(id),
    FOREIGN KEY(rate_policy_snapshot_id,scope_id,policy_config_kind,policy_key)
      REFERENCES platform_config_snapshots(id,scope_id,kind,logical_key)
);
CREATE INDEX admitted_rate_ticks_current_idx
ON admitted_rate_ticks(scope_id,policy_key,admitted_at DESC,id DESC);

-- PersistedPlanner must see exactly one active projection for a pair. If an
-- existing installation has ambiguous active rows, migration fails closed and
-- operations must reconcile them before retrying; no row is silently changed.
CREATE UNIQUE INDEX asset_rate_ticks_one_active_pair_idx
ON asset_rate_ticks(asset_id,fiat_currency) WHERE status='active';

CREATE TABLE admitted_rate_tick_observations (
    scope_id uuid NOT NULL,
    tick_id uuid NOT NULL,
    observation_id uuid NOT NULL,
    source_key text NOT NULL,
    PRIMARY KEY(scope_id,tick_id,observation_id),
    UNIQUE(scope_id,tick_id,source_key),
    FOREIGN KEY(tick_id,scope_id) REFERENCES admitted_rate_ticks(id,scope_id),
    FOREIGN KEY(observation_id,scope_id) REFERENCES rate_source_observations(id,scope_id)
);

CREATE TABLE rate_collection_dead_letters (
    id bigserial PRIMARY KEY,
    scope_id uuid NOT NULL,
    tenant_id uuid,
    policy_key text NOT NULL,
    claim_token bigint NOT NULL CHECK(claim_token>0),
    attempts integer NOT NULL CHECK(attempts>0),
    error_code text NOT NULL CHECK(error_code IN
      ('invalid_config','no_quorum','stale','future_timestamp','divergent','identity_disabled','dependency_unavailable')),
    occurred_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK(scope_id=platform_scope_uuid(tenant_id)),
    UNIQUE(scope_id,policy_key,claim_token),
    FOREIGN KEY(scope_id,policy_key) REFERENCES rate_runtime_jobs(scope_id,policy_key),
    FOREIGN KEY(tenant_id) REFERENCES tenants(id)
);

CREATE FUNCTION rate_runtime_immutable_row() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'rate provenance is immutable' USING ERRCODE='55000'; END $$;
CREATE TRIGGER rate_observations_immutable BEFORE UPDATE OR DELETE ON rate_source_observations FOR EACH ROW EXECUTE FUNCTION rate_runtime_immutable_row();
CREATE TRIGGER rate_ticks_immutable BEFORE UPDATE OR DELETE ON admitted_rate_ticks FOR EACH ROW EXECUTE FUNCTION rate_runtime_immutable_row();
CREATE TRIGGER rate_tick_observations_immutable BEFORE UPDATE OR DELETE ON admitted_rate_tick_observations FOR EACH ROW EXECUTE FUNCTION rate_runtime_immutable_row();
CREATE TRIGGER rate_dead_letters_immutable BEFORE UPDATE OR DELETE ON rate_collection_dead_letters FOR EACH ROW EXECUTE FUNCTION rate_runtime_immutable_row();
CREATE TRIGGER rate_pair_bindings_immutable BEFORE UPDATE OR DELETE ON rate_runtime_pair_bindings FOR EACH ROW EXECUTE FUNCTION rate_runtime_immutable_row();

ALTER TABLE rate_runtime_identities ENABLE ROW LEVEL SECURITY;
ALTER TABLE rate_runtime_identities FORCE ROW LEVEL SECURITY;
CREATE POLICY rate_runtime_identity_self ON rate_runtime_identities
USING(id::text=current_setting('app.rate_worker_id',true));
ALTER TABLE rate_runtime_pair_bindings ENABLE ROW LEVEL SECURITY;
ALTER TABLE rate_runtime_pair_bindings FORCE ROW LEVEL SECURITY;
CREATE POLICY rate_runtime_pair_bindings_global ON rate_runtime_pair_bindings
USING(current_setting('app.rate_worker_id',true)<>'' AND current_setting('app.rate_runtime_global',true)='true')
WITH CHECK(current_setting('app.rate_worker_id',true)<>'' AND current_setting('app.rate_runtime_global',true)='true');

DO $$ DECLARE table_name text; BEGIN
  FOREACH table_name IN ARRAY ARRAY['rate_runtime_jobs','rate_source_observations','admitted_rate_ticks','rate_collection_dead_letters'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY',table_name);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY',table_name);
    EXECUTE format('CREATE POLICY %I_worker_scope ON %I USING (current_setting(''app.rate_worker_id'',true)<>'''' AND ((current_setting(''app.rate_runtime_global'',true)=''true'' AND tenant_id IS NULL) OR tenant_id::text=ANY(string_to_array(current_setting(''app.rate_runtime_tenants'',true),'','')))) WITH CHECK (current_setting(''app.rate_worker_id'',true)<>'''' AND ((current_setting(''app.rate_runtime_global'',true)=''true'' AND tenant_id IS NULL) OR tenant_id::text=ANY(string_to_array(current_setting(''app.rate_runtime_tenants'',true),'',''))))',table_name,table_name);
  END LOOP;
END $$;
ALTER TABLE admitted_rate_tick_observations ENABLE ROW LEVEL SECURITY;
ALTER TABLE admitted_rate_tick_observations FORCE ROW LEVEL SECURITY;
CREATE POLICY admitted_rate_tick_observations_worker_scope ON admitted_rate_tick_observations
USING(current_setting('app.rate_worker_id',true)<>'' AND EXISTS(
  SELECT 1 FROM admitted_rate_ticks t WHERE t.scope_id=admitted_rate_tick_observations.scope_id AND t.id=admitted_rate_tick_observations.tick_id
)) WITH CHECK(current_setting('app.rate_worker_id',true)<>'' AND EXISTS(
  SELECT 1 FROM admitted_rate_ticks t WHERE t.scope_id=admitted_rate_tick_observations.scope_id AND t.id=admitted_rate_tick_observations.tick_id
));

REVOKE UPDATE,DELETE,TRUNCATE ON rate_runtime_pair_bindings,rate_source_observations,admitted_rate_ticks,admitted_rate_tick_observations,rate_collection_dead_letters FROM PUBLIC;
DO $$ BEGIN
  IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='rate_runtime_worker') THEN
    EXECUTE 'GRANT SELECT ON platform_config_heads,platform_config_snapshots,rate_runtime_identities TO rate_runtime_worker';
    EXECUTE 'GRANT SELECT ON assets TO rate_runtime_worker';
    EXECUTE 'GRANT SELECT,INSERT,UPDATE ON rate_runtime_jobs TO rate_runtime_worker';
    EXECUTE 'GRANT SELECT,INSERT ON asset_rate_ticks TO rate_runtime_worker';
    EXECUTE 'GRANT UPDATE(status) ON asset_rate_ticks TO rate_runtime_worker';
    EXECUTE 'GRANT SELECT,INSERT ON rate_runtime_pair_bindings,rate_source_observations,admitted_rate_ticks,admitted_rate_tick_observations,rate_collection_dead_letters TO rate_runtime_worker';
    EXECUTE 'GRANT USAGE,SELECT ON SEQUENCE rate_collection_dead_letters_id_seq TO rate_runtime_worker';
    EXECUTE 'REVOKE DELETE,TRUNCATE ON asset_rate_ticks FROM rate_runtime_worker';
    EXECUTE 'REVOKE UPDATE,DELETE,TRUNCATE ON rate_runtime_pair_bindings,rate_source_observations,admitted_rate_ticks,admitted_rate_tick_observations,rate_collection_dead_letters FROM rate_runtime_worker';
  END IF;
END $$;

COMMIT;
