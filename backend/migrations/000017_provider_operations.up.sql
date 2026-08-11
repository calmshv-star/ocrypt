BEGIN;

-- Provider operations stores identities and policy only. Endpoints, credentials,
-- addresses, raw upstream errors, and secret material are deliberately absent.
CREATE TABLE provider_operation_bindings (
    id uuid PRIMARY KEY,
    scope_id uuid NOT NULL,
    provider_kind text NOT NULL CHECK(provider_kind IN ('on_chain','hosted')),
    provider_id text NOT NULL,
    tenant_id uuid,
    merchant_id uuid,
    platform_snapshot_id uuid REFERENCES platform_config_snapshots(id) ON DELETE RESTRICT,
    config_logical_key text,
    chain_id text,
    status text NOT NULL CHECK(status IN ('active','paused','disabled')),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    version bigint NOT NULL DEFAULT 1 CHECK(version>0),
    CHECK(scope_id=platform_scope_uuid(tenant_id)),
    CHECK((provider_kind='on_chain' AND provider_id ~ '^[A-Za-z0-9][A-Za-z0-9._/-]{0,127}$')
       OR (provider_kind='hosted' AND provider_id ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$')),
    CHECK (
      (provider_kind='on_chain' AND tenant_id IS NULL AND merchant_id IS NULL
        AND platform_snapshot_id IS NOT NULL AND config_logical_key IS NOT NULL AND chain_id IS NOT NULL)
      OR
      (provider_kind='hosted' AND tenant_id IS NOT NULL AND merchant_id IS NOT NULL
        AND platform_snapshot_id IS NULL AND config_logical_key IS NULL AND chain_id IS NULL)
    ),
    FOREIGN KEY(provider_id,tenant_id,merchant_id)
      REFERENCES hosted_provider_configs(id,tenant_id,merchant_id)
      ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
    FOREIGN KEY(merchant_id,tenant_id) REFERENCES merchants(id,tenant_id) ON DELETE RESTRICT
);
ALTER TABLE provider_operation_bindings ADD CONSTRAINT provider_operation_binding_scope_unique UNIQUE(id,scope_id);
CREATE UNIQUE INDEX provider_operation_on_chain_id_unique
  ON provider_operation_bindings(provider_id) WHERE provider_kind='on_chain';
CREATE UNIQUE INDEX provider_operation_on_chain_key_unique
  ON provider_operation_bindings(config_logical_key) WHERE provider_kind='on_chain';
CREATE UNIQUE INDEX provider_operation_hosted_id_unique
  ON provider_operation_bindings(provider_id,tenant_id,merchant_id) WHERE provider_kind='hosted';

CREATE TABLE provider_operation_policies (
    binding_id uuid NOT NULL REFERENCES provider_operation_bindings(id) ON DELETE CASCADE,
    scope_id uuid NOT NULL,
    tenant_id uuid,
    operation text NOT NULL CHECK(operation IN (
      'health','head','range','transaction_lookup','transfer_verify',
      'create','status','cancel','refund','reconciliation'
    )),
    timeout_ms integer NOT NULL CHECK(timeout_ms BETWEEN 100 AND 30000),
    max_attempts smallint NOT NULL CHECK(max_attempts BETWEEN 1 AND 5),
    backoff_ms integer NOT NULL CHECK(backoff_ms BETWEEN 0 AND 30000),
    rate_limit integer NOT NULL CHECK(rate_limit BETWEEN 1 AND 10000),
    rate_window_seconds integer NOT NULL CHECK(rate_window_seconds BETWEEN 1 AND 3600),
    max_health_age_seconds integer NOT NULL CHECK(max_health_age_seconds BETWEEN 5 AND 3600),
    max_lag_blocks bigint NOT NULL CHECK(max_lag_blocks BETWEEN 0 AND 1000000000),
    failure_threshold smallint NOT NULL CHECK(failure_threshold BETWEEN 1 AND 20),
    open_seconds integer NOT NULL CHECK(open_seconds BETWEEN 1 AND 3600),
    half_open_successes smallint NOT NULL CHECK(half_open_successes BETWEEN 1 AND 20),
    priority smallint NOT NULL CHECK(priority BETWEEN 0 AND 1000),
    failure_domain text NOT NULL CHECK(failure_domain ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$'),
    policy_snapshot_id uuid REFERENCES platform_config_snapshots(id) ON DELETE RESTRICT,
    policy_snapshot_version bigint CHECK(policy_snapshot_version>0),
    policy_fence_token bigint CHECK(policy_fence_token>0),
    hosted_policy_version_id uuid,
    approved_at timestamptz,
    updated_at timestamptz NOT NULL,
    version bigint NOT NULL DEFAULT 1 CHECK(version>0),
    CHECK((approved_at IS NULL AND policy_snapshot_id IS NULL AND policy_snapshot_version IS NULL AND policy_fence_token IS NULL AND hosted_policy_version_id IS NULL)
       OR (approved_at IS NOT NULL AND
         ((policy_snapshot_id IS NOT NULL AND policy_snapshot_version IS NOT NULL AND policy_fence_token IS NOT NULL AND hosted_policy_version_id IS NULL)
          OR (policy_snapshot_id IS NULL AND policy_snapshot_version IS NULL AND policy_fence_token IS NULL AND hosted_policy_version_id IS NOT NULL)))),
    CHECK(scope_id=platform_scope_uuid(tenant_id)),
    PRIMARY KEY(binding_id,operation),
    UNIQUE(binding_id,operation,scope_id),
    FOREIGN KEY(binding_id,scope_id) REFERENCES provider_operation_bindings(id,scope_id) ON DELETE CASCADE
);

CREATE TABLE provider_circuit_states (
    binding_id uuid NOT NULL,
    scope_id uuid NOT NULL,
    tenant_id uuid,
    operation text NOT NULL,
    state text NOT NULL CHECK(state IN ('closed','open','half_open')),
    consecutive_failures integer NOT NULL DEFAULT 0 CHECK(consecutive_failures BETWEEN 0 AND 1000000),
    half_open_successes integer NOT NULL DEFAULT 0 CHECK(half_open_successes BETWEEN 0 AND 1000000),
    opened_until timestamptz,
    probe_lease_owner text,
    probe_lease_token bigint NOT NULL DEFAULT 0 CHECK(probe_lease_token>=0),
    probe_lease_until timestamptz,
    last_success_at timestamptz,
    last_observed_at timestamptz,
    updated_at timestamptz NOT NULL,
    fence_token bigint NOT NULL DEFAULT 1 CHECK(fence_token>0),
    version bigint NOT NULL DEFAULT 1 CHECK(version>0),
    PRIMARY KEY(binding_id,operation),
    CHECK(scope_id=platform_scope_uuid(tenant_id)),
    FOREIGN KEY(binding_id,operation,scope_id) REFERENCES provider_operation_policies(binding_id,operation,scope_id) ON DELETE CASCADE,
    CHECK((probe_lease_owner IS NULL)=(probe_lease_until IS NULL)),
    CHECK(state='open' OR opened_until IS NULL),
    CHECK(state<>'open' OR probe_lease_owner IS NULL)
);

CREATE TABLE provider_operation_rate_windows (
    binding_id uuid NOT NULL,
    scope_id uuid NOT NULL,
    tenant_id uuid,
    operation text NOT NULL,
    window_started_at timestamptz NOT NULL,
    used integer NOT NULL CHECK(used BETWEEN 0 AND 1000000),
    version bigint NOT NULL DEFAULT 1 CHECK(version>0),
    PRIMARY KEY(binding_id,operation),
    CHECK(scope_id=platform_scope_uuid(tenant_id)),
    FOREIGN KEY(binding_id,operation,scope_id) REFERENCES provider_operation_policies(binding_id,operation,scope_id) ON DELETE CASCADE
);

CREATE TABLE provider_health_observations (
    id uuid PRIMARY KEY,
    binding_id uuid NOT NULL,
    scope_id uuid NOT NULL,
    tenant_id uuid,
    operation text NOT NULL,
    outcome text NOT NULL CHECK(outcome IN ('success','failure')),
    error_category text NOT NULL CHECK(error_category IN (
      'none','timeout','dns','tls','connect','rate_limited','auth_rejected',
      'upstream_4xx','upstream_5xx','invalid_response','chain_mismatch',
      'genesis_mismatch','stale_head','divergent_response','policy_denied'
    )),
    latency_ms integer NOT NULL CHECK(latency_ms BETWEEN 0 AND 30000),
    lag_blocks bigint CHECK(lag_blocks BETWEEN 0 AND 1000000000),
    observed_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    CHECK(scope_id=platform_scope_uuid(tenant_id)),
    FOREIGN KEY(binding_id,operation,scope_id) REFERENCES provider_operation_policies(binding_id,operation,scope_id) ON DELETE CASCADE,
    CHECK((outcome='success' AND error_category='none') OR (outcome='failure' AND error_category<>'none')),
    CHECK(expires_at>observed_at AND expires_at<=observed_at+interval '24 hours')
);
CREATE INDEX provider_health_bounded_idx ON provider_health_observations(binding_id,operation,observed_at DESC,id DESC);
CREATE INDEX provider_health_expiry_idx ON provider_health_observations(expires_at);

CREATE TABLE provider_operation_change_requests (
    id uuid PRIMARY KEY,
    binding_id uuid NOT NULL,
    scope_id uuid NOT NULL,
    tenant_id uuid,
    requested_status text NOT NULL CHECK(requested_status IN ('active','paused')),
    expected_binding_version bigint NOT NULL CHECK(expected_binding_version>0),
    status text NOT NULL CHECK(status IN ('pending_approval','completed','rejected','expired')),
    reason text NOT NULL CHECK(length(btrim(reason)) BETWEEN 8 AND 1000),
    requested_by uuid NOT NULL,
    approved_by uuid,
    rejected_by uuid,
    request_step_up_at timestamptz NOT NULL,
    decision_step_up_at timestamptz,
    decision_reason text,
    decided_at timestamptz,
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    version bigint NOT NULL DEFAULT 1 CHECK(version>0),
    CHECK(scope_id=platform_scope_uuid(tenant_id)),
    FOREIGN KEY(binding_id,scope_id) REFERENCES provider_operation_bindings(id,scope_id) ON DELETE RESTRICT,
    CHECK(expires_at>created_at AND expires_at<=created_at+interval '30 minutes'),
    CHECK(approved_by IS NULL OR approved_by<>requested_by),
    CHECK(rejected_by IS NULL OR rejected_by<>requested_by),
    CHECK(
      (status='pending_approval' AND approved_by IS NULL AND rejected_by IS NULL AND decided_at IS NULL)
      OR (status='completed' AND approved_by IS NOT NULL AND rejected_by IS NULL AND decided_at IS NOT NULL AND decision_step_up_at IS NOT NULL AND length(btrim(decision_reason)) BETWEEN 8 AND 1000)
      OR (status IN ('rejected','expired') AND approved_by IS NULL AND decided_at IS NOT NULL AND length(btrim(decision_reason)) BETWEEN 8 AND 1000)
    )
);
CREATE UNIQUE INDEX provider_operation_pending_change_unique
  ON provider_operation_change_requests(binding_id) WHERE status='pending_approval';
CREATE INDEX provider_operation_changes_list_idx ON provider_operation_change_requests(tenant_id,status,created_at DESC,id DESC);

CREATE TABLE provider_operation_idempotency (
    scope_id uuid NOT NULL,
    tenant_id uuid,
    actor_id uuid NOT NULL,
    operation text NOT NULL CHECK(operation IN (
      'provider.pause.request','provider.unpause.request','provider.change.approve','provider.change.reject',
      'provider.policy.request','provider.policy.approve','provider.policy.reject'
    )),
    idempotency_key text NOT NULL CHECK(length(idempotency_key) BETWEEN 8 AND 255 AND idempotency_key=btrim(idempotency_key)),
    request_hash bytea NOT NULL CHECK(octet_length(request_hash)=32),
    response_body jsonb NOT NULL CHECK(jsonb_typeof(response_body)='object' AND pg_column_size(response_body)<=65536),
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    CHECK(scope_id=platform_scope_uuid(tenant_id)),
    PRIMARY KEY(scope_id,actor_id,operation,idempotency_key)
);

CREATE TABLE provider_hosted_policy_versions (
    id uuid PRIMARY KEY,
    binding_id uuid NOT NULL,
    scope_id uuid NOT NULL,
    tenant_id uuid NOT NULL,
    policy_version bigint NOT NULL CHECK(policy_version>0),
    policy_payload jsonb NOT NULL CHECK(jsonb_typeof(policy_payload)='object' AND pg_column_size(policy_payload)<=65536),
    payload_hash bytea NOT NULL CHECK(octet_length(payload_hash)=32),
    bootstrap_probe_reference text NOT NULL CHECK(bootstrap_probe_reference ~ '^[A-Za-z0-9][A-Za-z0-9._:-]{0,254}$'),
    status text NOT NULL CHECK(status IN ('pending_approval','approved_pending_probe','active','rejected','superseded','expired')),
    expected_binding_version bigint NOT NULL CHECK(expected_binding_version>0),
    reason text NOT NULL CHECK(length(btrim(reason)) BETWEEN 8 AND 1000),
    requested_by uuid NOT NULL,
    approved_by uuid,
    rejected_by uuid,
    request_step_up_at timestamptz NOT NULL,
    decision_step_up_at timestamptz,
    decision_reason text,
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    decided_at timestamptz,
    activated_at timestamptz,
    updated_at timestamptz NOT NULL,
    row_version bigint NOT NULL DEFAULT 1 CHECK(row_version>0),
    CHECK(scope_id=platform_scope_uuid(tenant_id)),
    FOREIGN KEY(binding_id,scope_id) REFERENCES provider_operation_bindings(id,scope_id) ON DELETE RESTRICT,
    UNIQUE(binding_id,policy_version),
    CHECK(expires_at>created_at AND expires_at<=created_at+interval '30 minutes'),
    CHECK(approved_by IS NULL OR approved_by<>requested_by),
    CHECK(rejected_by IS NULL OR rejected_by<>requested_by),
    CHECK(
      (status='pending_approval' AND approved_by IS NULL AND rejected_by IS NULL AND decided_at IS NULL AND activated_at IS NULL)
      OR (status='approved_pending_probe' AND approved_by IS NOT NULL AND rejected_by IS NULL AND decided_at IS NOT NULL AND activated_at IS NULL AND decision_step_up_at IS NOT NULL AND length(btrim(decision_reason)) BETWEEN 8 AND 1000)
      OR (status='active' AND approved_by IS NOT NULL AND rejected_by IS NULL AND decided_at IS NOT NULL AND activated_at IS NOT NULL AND decision_step_up_at IS NOT NULL AND length(btrim(decision_reason)) BETWEEN 8 AND 1000)
      OR (status='superseded' AND approved_by IS NOT NULL AND rejected_by IS NULL AND decided_at IS NOT NULL AND decision_step_up_at IS NOT NULL AND length(btrim(decision_reason)) BETWEEN 8 AND 1000)
      OR (status='rejected' AND approved_by IS NULL AND rejected_by IS NOT NULL AND activated_at IS NULL AND decided_at IS NOT NULL AND length(btrim(decision_reason)) BETWEEN 8 AND 1000)
      OR (status='expired' AND approved_by IS NULL AND rejected_by IS NULL AND activated_at IS NULL AND decided_at IS NOT NULL AND length(btrim(decision_reason)) BETWEEN 8 AND 1000)
    )
);
CREATE UNIQUE INDEX provider_hosted_policy_pending_unique ON provider_hosted_policy_versions(binding_id) WHERE status='pending_approval';
CREATE UNIQUE INDEX provider_hosted_policy_active_unique ON provider_hosted_policy_versions(binding_id) WHERE status='active';
CREATE INDEX provider_hosted_policy_list_idx ON provider_hosted_policy_versions(tenant_id,status,created_at DESC,id DESC);
ALTER TABLE provider_operation_policies ADD CONSTRAINT provider_operation_hosted_policy_fk
  FOREIGN KEY(hosted_policy_version_id) REFERENCES provider_hosted_policy_versions(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED;

CREATE FUNCTION provider_hosted_policy_evidence_immutable() RETURNS trigger
LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN
  IF NEW.id<>OLD.id OR NEW.binding_id<>OLD.binding_id OR NEW.scope_id<>OLD.scope_id OR NEW.tenant_id<>OLD.tenant_id
    OR NEW.policy_version<>OLD.policy_version OR NEW.policy_payload<>OLD.policy_payload OR NEW.payload_hash<>OLD.payload_hash
    OR NEW.bootstrap_probe_reference<>OLD.bootstrap_probe_reference OR NEW.expected_binding_version<>OLD.expected_binding_version
    OR NEW.reason<>OLD.reason OR NEW.requested_by<>OLD.requested_by OR NEW.request_step_up_at<>OLD.request_step_up_at
    OR NEW.created_at<>OLD.created_at OR NEW.expires_at<>OLD.expires_at
  THEN RAISE EXCEPTION 'hosted provider policy evidence is immutable' USING ERRCODE='23514'; END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER provider_hosted_policy_evidence_immutable_before_update
BEFORE UPDATE ON provider_hosted_policy_versions FOR EACH ROW EXECUTE FUNCTION provider_hosted_policy_evidence_immutable();
REVOKE ALL ON FUNCTION provider_hosted_policy_evidence_immutable() FROM PUBLIC;

CREATE FUNCTION provider_operation_policy_payload_valid(value jsonb, required_operations text[]) RETURNS boolean
LANGUAGE plpgsql IMMUTABLE PARALLEL SAFE SET search_path=pg_catalog AS $$
DECLARE op text; policy jsonb; key text;
DECLARE allowed text[] := ARRAY['timeout_ms','max_attempts','backoff_ms','rate_limit','rate_window_seconds','max_health_age_seconds','failure_threshold','open_seconds','half_open_successes','priority','max_lag_blocks','failure_domain'];
BEGIN
  IF jsonb_typeof(value)<>'object' OR (SELECT array_agg(k ORDER BY k) FROM jsonb_object_keys(value) k) IS DISTINCT FROM (SELECT array_agg(k ORDER BY k) FROM unnest(required_operations) k) THEN RETURN false; END IF;
  FOREACH op IN ARRAY required_operations LOOP
    policy := value->op;
    IF jsonb_typeof(policy)<>'object' THEN RETURN false; END IF;
    FOREACH key IN ARRAY allowed LOOP IF NOT policy ? key THEN RETURN false; END IF; END LOOP;
    IF EXISTS(SELECT 1 FROM jsonb_object_keys(policy) k WHERE NOT k=ANY(allowed)) THEN RETURN false; END IF;
    IF jsonb_typeof(policy->'failure_domain')<>'string' OR policy->>'failure_domain' !~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$' THEN RETURN false; END IF;
    FOREACH key IN ARRAY allowed[1:11] LOOP IF jsonb_typeof(policy->key)<>'number' OR policy->>key !~ '^[0-9]+$' THEN RETURN false; END IF; END LOOP;
    IF (policy->>'timeout_ms')::numeric NOT BETWEEN 100 AND 30000
      OR (policy->>'max_attempts')::numeric NOT BETWEEN 1 AND 5
      OR (policy->>'backoff_ms')::numeric NOT BETWEEN 0 AND 30000
      OR (policy->>'rate_limit')::numeric NOT BETWEEN 1 AND 10000
      OR (policy->>'rate_window_seconds')::numeric NOT BETWEEN 1 AND 3600
      OR (policy->>'max_health_age_seconds')::numeric NOT BETWEEN 5 AND 3600
      OR (policy->>'failure_threshold')::numeric NOT BETWEEN 1 AND 20
      OR (policy->>'open_seconds')::numeric NOT BETWEEN 1 AND 3600
      OR (policy->>'half_open_successes')::numeric NOT BETWEEN 1 AND 20
      OR (policy->>'priority')::numeric NOT BETWEEN 0 AND 1000
      OR (policy->>'max_lag_blocks')::numeric NOT BETWEEN 0 AND 1000000000 THEN RETURN false; END IF;
  END LOOP;
  RETURN true;
END $$;

CREATE FUNCTION provider_operation_seed_policy() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE op text; operations text[];
DECLARE payload jsonb; policy jsonb; snapshot_version bigint; snapshot_fence bigint; approved timestamptz;
BEGIN
  operations := CASE WHEN NEW.provider_kind='on_chain'
    THEN ARRAY['health','head','range','transaction_lookup','transfer_verify']::text[]
    ELSE ARRAY['health','create','status','cancel','refund','reconciliation']::text[] END;
  IF NEW.provider_kind='on_chain' THEN
    SELECT s.payload,s.version,h.fence_token,s.activated_at INTO payload,snapshot_version,snapshot_fence,approved
      FROM public.platform_config_snapshots s JOIN public.platform_config_heads h ON h.snapshot_id=s.id AND h.scope_id=s.scope_id
     WHERE s.id=NEW.platform_snapshot_id;
    IF NOT public.provider_operation_policy_payload_valid(payload->'provider_operations',operations) THEN approved := NULL; END IF;
  END IF;
  FOREACH op IN ARRAY operations LOOP
    policy := payload->'provider_operations'->op;
    INSERT INTO public.provider_operation_policies(binding_id,scope_id,tenant_id,operation,timeout_ms,max_attempts,backoff_ms,rate_limit,rate_window_seconds,max_health_age_seconds,max_lag_blocks,failure_threshold,open_seconds,half_open_successes,priority,failure_domain,policy_snapshot_id,policy_snapshot_version,policy_fence_token,approved_at,updated_at)
    VALUES(NEW.id,NEW.scope_id,NEW.tenant_id,op,
      CASE WHEN approved IS NULL THEN CASE WHEN op='health' THEN 5000 ELSE 20000 END ELSE (policy->>'timeout_ms')::integer END,
      CASE WHEN approved IS NULL THEN 1 ELSE (policy->>'max_attempts')::smallint END,
      CASE WHEN approved IS NULL THEN 250 ELSE (policy->>'backoff_ms')::integer END,
      CASE WHEN approved IS NULL THEN 60 ELSE (policy->>'rate_limit')::integer END,
      CASE WHEN approved IS NULL THEN 60 ELSE (policy->>'rate_window_seconds')::integer END,
      CASE WHEN approved IS NULL THEN 120 ELSE (policy->>'max_health_age_seconds')::integer END,
      CASE WHEN approved IS NULL THEN 0 ELSE (policy->>'max_lag_blocks')::bigint END,
      CASE WHEN approved IS NULL THEN 3 ELSE (policy->>'failure_threshold')::smallint END,
      CASE WHEN approved IS NULL THEN 60 ELSE (policy->>'open_seconds')::integer END,
      CASE WHEN approved IS NULL THEN 2 ELSE (policy->>'half_open_successes')::smallint END,
      CASE WHEN approved IS NULL THEN 100 ELSE (policy->>'priority')::smallint END,
      CASE WHEN approved IS NULL THEN NEW.provider_id ELSE policy->>'failure_domain' END,
      CASE WHEN approved IS NULL THEN NULL ELSE NEW.platform_snapshot_id END,
      CASE WHEN approved IS NULL THEN NULL ELSE snapshot_version END,
      CASE WHEN approved IS NULL THEN NULL ELSE snapshot_fence END,approved,NEW.updated_at);
    INSERT INTO public.provider_circuit_states(binding_id,scope_id,tenant_id,operation,state,updated_at)
    VALUES(NEW.id,NEW.scope_id,NEW.tenant_id,op,'closed',NEW.updated_at);
    INSERT INTO public.provider_operation_rate_windows(binding_id,scope_id,tenant_id,operation,window_started_at,used)
    VALUES(NEW.id,NEW.scope_id,NEW.tenant_id,op,NEW.updated_at,0);
  END LOOP;
  RETURN NULL;
END $$;
CREATE TRIGGER provider_operation_seed_policy_after_insert
AFTER INSERT ON provider_operation_bindings FOR EACH ROW EXECUTE FUNCTION provider_operation_seed_policy();
REVOKE ALL ON FUNCTION provider_operation_seed_policy() FROM PUBLIC;

CREATE FUNCTION provider_operation_apply_rpc_policy(requested_binding uuid, requested_snapshot uuid, applied_at timestamptz) RETURNS boolean
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE payload jsonb; snapshot_version bigint; snapshot_fence bigint; approved timestamptz; op text; policy jsonb;
DECLARE required text[] := ARRAY['health','head','range','transaction_lookup','transfer_verify'];
BEGIN
  SELECT s.payload,s.version,h.fence_token,s.activated_at INTO payload,snapshot_version,snapshot_fence,approved
    FROM public.platform_config_snapshots s JOIN public.platform_config_heads h ON h.snapshot_id=s.id AND h.scope_id=s.scope_id
   WHERE s.id=requested_snapshot;
  IF NOT public.provider_operation_policy_payload_valid(payload->'provider_operations',required) THEN
    UPDATE public.provider_operation_policies SET policy_snapshot_id=NULL,policy_snapshot_version=NULL,policy_fence_token=NULL,approved_at=NULL,updated_at=applied_at,version=version+1 WHERE binding_id=requested_binding;
    UPDATE public.provider_operation_bindings SET status='paused',updated_at=applied_at,version=version+1 WHERE id=requested_binding AND status<>'disabled';
    RETURN false;
  END IF;
  FOREACH op IN ARRAY required LOOP
    policy:=payload->'provider_operations'->op;
    UPDATE public.provider_operation_policies SET timeout_ms=(policy->>'timeout_ms')::integer,max_attempts=(policy->>'max_attempts')::smallint,backoff_ms=(policy->>'backoff_ms')::integer,rate_limit=(policy->>'rate_limit')::integer,rate_window_seconds=(policy->>'rate_window_seconds')::integer,max_health_age_seconds=(policy->>'max_health_age_seconds')::integer,max_lag_blocks=(policy->>'max_lag_blocks')::bigint,failure_threshold=(policy->>'failure_threshold')::smallint,open_seconds=(policy->>'open_seconds')::integer,half_open_successes=(policy->>'half_open_successes')::smallint,priority=(policy->>'priority')::smallint,failure_domain=policy->>'failure_domain',policy_snapshot_id=requested_snapshot,policy_snapshot_version=snapshot_version,policy_fence_token=snapshot_fence,approved_at=approved,updated_at=applied_at,version=version+1
     WHERE binding_id=requested_binding AND operation=op;
  END LOOP;
  RETURN true;
END $$;
REVOKE ALL ON FUNCTION provider_operation_apply_rpc_policy(uuid,uuid,timestamptz) FROM PUBLIC;

-- Current admitted RPC heads become durable secret-free bindings. Later head
-- activations refresh only snapshot evidence and never implicitly unpause.
CREATE FUNCTION provider_operation_sync_rpc_head() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE s record; provider text; chain_ref text; binding uuid; valid_policy boolean;
BEGIN
  IF NEW.kind<>'rpc_provider'::public.platform_config_kind OR NEW.tenant_id IS NOT NULL THEN RETURN NULL; END IF;
  SELECT id,logical_key,payload,updated_at INTO s
    FROM public.platform_config_snapshots WHERE id=NEW.snapshot_id;
  provider := s.payload->>'provider_id'; chain_ref := s.payload->>'chain_ref';
  IF provider IS NULL OR provider !~ '^[A-Za-z0-9][A-Za-z0-9._/-]{0,127}$' OR chain_ref IS NULL OR length(chain_ref)>128 THEN
    RAISE EXCEPTION 'RPC provider snapshot has no safe stable identity' USING ERRCODE='23514';
  END IF;
  valid_policy:=public.provider_operation_policy_payload_valid(s.payload->'provider_operations',ARRAY['health','head','range','transaction_lookup','transfer_verify']);
  INSERT INTO public.provider_operation_bindings(id,scope_id,provider_kind,provider_id,platform_snapshot_id,config_logical_key,chain_id,status,created_at,updated_at)
  VALUES(gen_random_uuid(),public.platform_scope_uuid(NULL),'on_chain',provider,NEW.snapshot_id,s.logical_key,chain_ref,
    CASE WHEN valid_policy THEN 'active' ELSE 'paused' END,
    NEW.updated_at,NEW.updated_at)
  ON CONFLICT(config_logical_key) WHERE provider_kind='on_chain'
  DO UPDATE SET provider_id=EXCLUDED.provider_id,platform_snapshot_id=EXCLUDED.platform_snapshot_id,chain_id=EXCLUDED.chain_id,status=CASE WHEN valid_policy THEN provider_operation_bindings.status ELSE 'paused' END,updated_at=EXCLUDED.updated_at,version=provider_operation_bindings.version+1
  RETURNING id INTO binding;
  PERFORM public.provider_operation_apply_rpc_policy(binding,NEW.snapshot_id,NEW.updated_at);
  RETURN NULL;
END $$;
CREATE TRIGGER provider_operation_rpc_head_sync
AFTER INSERT OR UPDATE OF snapshot_id ON platform_config_heads
FOR EACH ROW EXECUTE FUNCTION provider_operation_sync_rpc_head();
REVOKE ALL ON FUNCTION provider_operation_sync_rpc_head() FROM PUBLIC;

INSERT INTO provider_operation_bindings(id,scope_id,provider_kind,provider_id,platform_snapshot_id,config_logical_key,chain_id,status,created_at,updated_at)
SELECT gen_random_uuid(),platform_scope_uuid(NULL),'on_chain',s.payload->>'provider_id',s.id,s.logical_key,s.payload->>'chain_ref',
       CASE WHEN provider_operation_policy_payload_valid(s.payload->'provider_operations',ARRAY['health','head','range','transaction_lookup','transfer_verify']) THEN 'active' ELSE 'paused' END,
       h.updated_at,h.updated_at
  FROM platform_config_heads h JOIN platform_config_snapshots s ON s.id=h.snapshot_id
 WHERE h.kind='rpc_provider' AND h.tenant_id IS NULL
   AND s.payload->>'provider_id' ~ '^[A-Za-z0-9][A-Za-z0-9._/-]{0,127}$';

CREATE FUNCTION provider_operation_sync_hosted_config() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
BEGIN
  IF current_setting('app.provider_ops_status_apply',true)='true' THEN RETURN NULL; END IF;
  INSERT INTO public.provider_operation_bindings(id,scope_id,provider_kind,provider_id,tenant_id,merchant_id,status,created_at,updated_at,version)
  VALUES(gen_random_uuid(),public.platform_scope_uuid(NEW.tenant_id),'hosted',NEW.id,NEW.tenant_id,NEW.merchant_id,
    CASE WHEN NEW.status='disabled' THEN 'disabled' ELSE 'paused' END,NEW.created_at,NEW.updated_at,NEW.version)
  ON CONFLICT(provider_id,tenant_id,merchant_id) WHERE provider_kind='hosted'
  DO UPDATE SET status=CASE WHEN EXCLUDED.status='disabled' THEN 'disabled' ELSE provider_operation_bindings.status END,updated_at=EXCLUDED.updated_at,version=GREATEST(provider_operation_bindings.version+1,EXCLUDED.version);
  RETURN NULL;
END $$;
CREATE TRIGGER provider_operation_hosted_config_sync
AFTER INSERT OR UPDATE OF status ON hosted_provider_configs
FOR EACH ROW EXECUTE FUNCTION provider_operation_sync_hosted_config();
REVOKE ALL ON FUNCTION provider_operation_sync_hosted_config() FROM PUBLIC;

INSERT INTO provider_operation_bindings(id,scope_id,provider_kind,provider_id,tenant_id,merchant_id,status,created_at,updated_at,version)
SELECT gen_random_uuid(),platform_scope_uuid(tenant_id),'hosted',id,tenant_id,merchant_id,
       CASE WHEN status='disabled' THEN 'disabled' ELSE 'paused' END,created_at,updated_at,version FROM hosted_provider_configs;

DO $$ DECLARE table_name text; BEGIN
  FOREACH table_name IN ARRAY ARRAY[
    'provider_operation_bindings','provider_operation_policies','provider_circuit_states',
    'provider_operation_rate_windows','provider_health_observations',
    'provider_operation_change_requests','provider_operation_idempotency',
    'provider_hosted_policy_versions'
  ] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY',table_name);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY',table_name);
    EXECUTE format('CREATE POLICY %I_scope ON %I USING ((current_setting(''app.platform_admin_global'',true)=''true'') OR tenant_id::text = ANY(string_to_array(current_setting(''app.platform_admin_tenants'',true),'',''))) WITH CHECK ((current_setting(''app.platform_admin_global'',true)=''true'') OR tenant_id::text = ANY(string_to_array(current_setting(''app.platform_admin_tenants'',true),'','')))',table_name,table_name);
  END LOOP;
END $$;

CREATE FUNCTION provider_operation_binding_policy_current(requested_binding uuid) RETURNS boolean
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
  SELECT CASE WHEN b.provider_kind='on_chain' THEN
    (SELECT count(*)=5 AND bool_and(p.approved_at IS NOT NULL AND p.policy_snapshot_id=b.platform_snapshot_id AND p.policy_snapshot_version=s.version AND p.policy_fence_token=h.fence_token)
       FROM public.provider_operation_policies p
       JOIN public.platform_config_snapshots s ON s.id=b.platform_snapshot_id
       JOIN public.platform_config_heads h ON h.snapshot_id=s.id AND h.scope_id=s.scope_id
      WHERE p.binding_id=b.id AND p.operation=ANY(ARRAY['health','head','range','transaction_lookup','transfer_verify']))
    WHEN b.provider_kind='hosted' THEN COALESCE(
      (SELECT count(*)=6 AND count(DISTINCT p.hosted_policy_version_id)=1
         AND bool_and(p.approved_at IS NOT NULL AND p.hosted_policy_version_id=v.id)
         AND v.status='active'
         FROM public.provider_operation_policies p
         JOIN public.provider_hosted_policy_versions v ON v.id=p.hosted_policy_version_id AND v.binding_id=b.id
        WHERE p.binding_id=b.id AND p.operation=ANY(ARRAY['health','create','status','cancel','refund','reconciliation'])
        GROUP BY v.id,v.status),false)
    ELSE false END
  FROM public.provider_operation_bindings b WHERE b.id=requested_binding
$$;
REVOKE ALL ON FUNCTION provider_operation_binding_policy_current(uuid) FROM PUBLIC;

CREATE FUNCTION provider_operation_binding_health_ready(requested_binding uuid,requested_now timestamptz) RETURNS boolean
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
  SELECT CASE WHEN b.provider_kind='hosted' THEN EXISTS(
    SELECT 1 FROM public.provider_operation_policies p
    JOIN public.provider_circuit_states c ON c.binding_id=p.binding_id AND c.operation=p.operation
    WHERE p.binding_id=b.id AND p.operation='status' AND c.state='closed'
      AND c.last_success_at IS NOT NULL AND c.last_success_at<=requested_now
      AND c.last_success_at+p.max_health_age_seconds*interval '1 second'>=requested_now
  ) ELSE true END
  FROM public.provider_operation_bindings b WHERE b.id=requested_binding
$$;
REVOKE ALL ON FUNCTION provider_operation_binding_health_ready(uuid,timestamptz) FROM PUBLIC;

CREATE FUNCTION admit_hosted_provider_operation(requested_tenant uuid,requested_merchant uuid,requested_provider text,requested_operation text,requested_now timestamptz) RETURNS boolean
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
  SELECT CASE WHEN requested_operation='callback' THEN
    EXISTS(SELECT 1 FROM public.hosted_provider_configs c JOIN public.provider_operation_bindings b ON b.provider_kind='hosted' AND b.provider_id=c.id AND b.tenant_id=c.tenant_id AND b.merchant_id=c.merchant_id
      WHERE c.id=requested_provider AND c.tenant_id=requested_tenant AND c.merchant_id=requested_merchant AND c.status<>'disabled' AND b.status<>'disabled')
  WHEN requested_operation IN ('create','status','cancel','refund','reconciliation') THEN
    EXISTS(SELECT 1 FROM public.hosted_provider_configs c JOIN public.provider_operation_bindings b ON b.provider_kind='hosted' AND b.provider_id=c.id AND b.tenant_id=c.tenant_id AND b.merchant_id=c.merchant_id
      JOIN public.provider_operation_policies p ON p.binding_id=b.id AND p.operation=requested_operation
      JOIN public.provider_operation_policies health_policy ON health_policy.binding_id=b.id AND health_policy.operation='status'
      JOIN public.provider_circuit_states circuit ON circuit.binding_id=b.id AND circuit.operation='status'
      WHERE c.id=requested_provider AND c.tenant_id=requested_tenant AND c.merchant_id=requested_merchant AND c.status='active' AND b.status='active'
        AND p.approved_at IS NOT NULL AND public.provider_operation_binding_policy_current(b.id)
        AND circuit.state='closed' AND circuit.last_success_at IS NOT NULL
        AND circuit.last_success_at<=requested_now AND circuit.last_success_at+health_policy.max_health_age_seconds*interval '1 second'>=requested_now)
  ELSE false END
$$;
REVOKE ALL ON FUNCTION admit_hosted_provider_operation(uuid,uuid,text,text,timestamptz) FROM PUBLIC;

-- Recovery discovery is also fenced at claim time. The workers recheck this
-- same capability immediately before each outbound call, so a pause between
-- claim and I/O remains fail-closed.
CREATE OR REPLACE FUNCTION claim_hosted_create_recoveries(claim_now timestamptz,lease_until timestamptz,claim_limit integer)
RETURNS TABLE (
  id uuid,claim_token uuid,attempt integer,tenant_id uuid,merchant_id uuid,provider_id text,intent_id uuid,
  idempotency_key text,request_hash text,asset_id text,fiat_amount_minor text,currency text,currency_scale smallint,
  expires_at timestamptz,create_state text,provider_order_id text,provider_reference text,payment_url text,
  amount_atomic text,asset_decimals smallint,quote_id text,rate_numerator text,rate_denominator text,
  quote_issued_at timestamptz,create_response_body bytea,create_response_digest bytea,create_response_received_at timestamptz
)
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
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
      AND public.admit_hosted_provider_operation(a.tenant_id,a.merchant_id,a.provider_id,
        CASE WHEN a.state='completed' AND (i.status NOT IN('awaiting_route_selection','pending') OR i.expires_at<=claim_now) THEN 'cancel' ELSE 'create' END,claim_now)
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

CREATE OR REPLACE FUNCTION claim_hosted_order_recoveries(claim_now timestamptz,lease_until timestamptz,claim_limit integer)
RETURNS TABLE (
  id uuid,claim_token uuid,attempt integer,tenant_id uuid,merchant_id uuid,route_id uuid,provider_id text,
  provider_reference text,asset_id text,amount_atomic text,asset_decimals smallint
)
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
BEGIN
  IF claim_limit<1 OR claim_limit>100 OR lease_until<=claim_now OR lease_until>claim_now+interval '5 minutes' THEN
    RAISE EXCEPTION 'invalid hosted recovery claim bounds' USING ERRCODE='22023';
  END IF;
  RETURN QUERY
  WITH picked AS (
    SELECT o.id FROM public.provider_orders o
    WHERE o.provider_status IN('pending','authorized','cancel_requested') AND o.next_reconcile_at<=claim_now
      AND (o.reconcile_claim_token IS NULL OR o.reconcile_claim_until<claim_now)
      AND public.admit_hosted_provider_operation(o.tenant_id,o.merchant_id,o.provider_id,'reconciliation',claim_now)
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

-- Preserve signed callback evidence while paused. Only outbound operations are
-- stopped; disabled providers remain rejected. Down restores the 000016 body.
CREATE OR REPLACE FUNCTION hosted_provider_callback_admitted(requested_provider_id text)
RETURNS TABLE (
  id text,tenant_id uuid,merchant_id uuid,adapter_kind text,api_origin text,
  create_path text,cancel_path text,status_path text,refund_path text,reconcile_path text,
  payment_url_origins text[],api_credential_ref text,api_key_id text,callback_secret_ref text,
  callback_key_id text,signature_scheme text,asset_id text,asset_decimals smallint,currency text,status text
)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
SELECT c.id,c.tenant_id,c.merchant_id,c.adapter_kind,c.api_origin,c.create_path,c.cancel_path,c.status_path,c.refund_path,c.reconcile_path,
       c.payment_url_origins,c.api_credential_ref,c.api_key_id,c.callback_secret_ref,c.callback_key_id,c.signature_scheme,c.asset_id,c.asset_decimals,c.currency::text,c.status
FROM public.hosted_provider_configs c
JOIN public.tenants t ON t.id=c.tenant_id AND t.status='active'
JOIN public.merchants m ON m.id=c.merchant_id AND m.tenant_id=c.tenant_id AND m.status='active'
WHERE c.id=requested_provider_id AND c.status IN ('active','paused')
  AND public.admit_hosted_provider_operation(c.tenant_id,c.merchant_id,c.id,'callback',clock_timestamp())
$$;
REVOKE ALL ON FUNCTION hosted_provider_callback_admitted(text) FROM PUBLIC;

CREATE FUNCTION request_provider_operation_change(
  requested_id uuid, requested_binding uuid, requested_tenant uuid, requested_status text,
  expected_version bigint, requested_reason text, requested_actor uuid,
  requested_session text, requested_step_up timestamptz, requested_now timestamptz
) RETURNS SETOF provider_operation_change_requests
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE binding public.provider_operation_bindings%ROWTYPE; event_id uuid; audit_id uuid;
BEGIN
  requested_now:=clock_timestamp();
  IF current_setting('app.provider_ops_actor_id',true) IS DISTINCT FROM requested_actor::text
    OR current_setting('app.provider_ops_session_id',true) IS DISTINCT FROM requested_session
    OR requested_step_up<requested_now-interval '10 minutes' OR requested_step_up>requested_now+interval '10 seconds'
    OR NOT EXISTS(
      SELECT 1 FROM public.admin_role_bindings b JOIN public.admin_users u ON u.id=b.user_id
      JOIN public.admin_role_permissions rp ON rp.role_key=b.role_key
      WHERE b.user_id=requested_actor AND u.status='active' AND rp.permission_key='provider_ops:request'
        AND b.revoked_at IS NULL AND (b.expires_at IS NULL OR b.expires_at>requested_now)
        AND (b.tenant_id IS NULL OR b.tenant_id=requested_tenant)
    ) THEN RAISE EXCEPTION 'provider operation request denied' USING ERRCODE='42501'; END IF;
  IF requested_status NOT IN ('active','paused') OR length(btrim(requested_reason)) NOT BETWEEN 8 AND 1000 THEN RAISE EXCEPTION 'invalid provider operation request' USING ERRCODE='22023'; END IF;
  SELECT * INTO binding FROM public.provider_operation_bindings
   WHERE id=requested_binding AND scope_id=public.platform_scope_uuid(requested_tenant) FOR UPDATE;
  IF NOT FOUND THEN RAISE EXCEPTION 'provider binding not found' USING ERRCODE='P0002'; END IF;
  IF binding.version<>expected_version OR binding.status=requested_status OR binding.status='disabled' THEN RAISE EXCEPTION 'provider binding version or state conflict' USING ERRCODE='40001'; END IF;
  IF requested_status='active' AND (NOT public.provider_operation_binding_policy_current(binding.id) OR NOT public.provider_operation_binding_health_ready(binding.id,requested_now)) THEN RAISE EXCEPTION 'provider policy or health evidence is not approved and current' USING ERRCODE='40001'; END IF;
  IF EXISTS(SELECT 1 FROM public.provider_operation_change_requests WHERE binding_id=binding.id AND status='pending_approval') THEN RAISE EXCEPTION 'pending provider change exists' USING ERRCODE='40001'; END IF;
  INSERT INTO public.provider_operation_change_requests(id,binding_id,scope_id,tenant_id,requested_status,expected_binding_version,status,reason,requested_by,request_step_up_at,created_at,expires_at,updated_at)
  VALUES(requested_id,binding.id,binding.scope_id,binding.tenant_id,requested_status,expected_version,'pending_approval',btrim(requested_reason),requested_actor,requested_step_up,requested_now,requested_now+interval '30 minutes',requested_now);
  audit_id:=gen_random_uuid(); event_id:=gen_random_uuid();
  PERFORM public.append_platform_admin_audit(audit_id,binding.tenant_id,requested_actor,requested_session,'provider_status.requested','provider_binding',binding.id::text,btrim(requested_reason),jsonb_build_object('requested_status',requested_status,'binding_version',binding.version),requested_now);
  INSERT INTO public.platform_admin_outbox(id,scope_id,tenant_id,aggregate_type,aggregate_id,aggregate_version,event_type,payload,occurred_at,available_at)
  VALUES(event_id,binding.scope_id,binding.tenant_id,'provider_binding',binding.id::text,binding.version,'platform_admin.provider_status.requested',jsonb_build_object('event_id',event_id,'event_type','platform_admin.provider_status.requested','resource_type','provider_binding','resource_id',binding.id,'aggregate_version',binding.version,'occurred_at',requested_now,'details',jsonb_build_object('requested_status',requested_status)),requested_now,requested_now);
  RETURN QUERY SELECT * FROM public.provider_operation_change_requests WHERE id=requested_id;
END $$;
REVOKE ALL ON FUNCTION request_provider_operation_change(uuid,uuid,uuid,text,bigint,text,uuid,text,timestamptz,timestamptz) FROM PUBLIC;

CREATE FUNCTION decide_provider_operation_change(
  requested_id uuid, requested_tenant uuid, expected_request_version bigint, approve boolean,
  requested_reason text, requested_actor uuid, requested_session text,
  requested_step_up timestamptz, requested_now timestamptz
) RETURNS SETOF provider_operation_change_requests
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE change public.provider_operation_change_requests%ROWTYPE; binding public.provider_operation_bindings%ROWTYPE; event_id uuid; audit_id uuid; action text;
BEGIN
  requested_now:=clock_timestamp();
  IF current_setting('app.provider_ops_actor_id',true) IS DISTINCT FROM requested_actor::text
    OR current_setting('app.provider_ops_session_id',true) IS DISTINCT FROM requested_session
    OR requested_step_up<requested_now-interval '10 minutes' OR requested_step_up>requested_now+interval '10 seconds'
    OR NOT EXISTS(
      SELECT 1 FROM public.admin_role_bindings b JOIN public.admin_users u ON u.id=b.user_id
      JOIN public.admin_role_permissions rp ON rp.role_key=b.role_key
      WHERE b.user_id=requested_actor AND u.status='active' AND rp.permission_key='provider_ops:approve'
        AND b.revoked_at IS NULL AND (b.expires_at IS NULL OR b.expires_at>requested_now)
        AND (b.tenant_id IS NULL OR b.tenant_id=requested_tenant)
    ) THEN RAISE EXCEPTION 'provider operation decision denied' USING ERRCODE='42501'; END IF;
  IF length(btrim(requested_reason)) NOT BETWEEN 8 AND 1000 THEN RAISE EXCEPTION 'invalid provider operation decision' USING ERRCODE='22023'; END IF;
  SELECT * INTO change FROM public.provider_operation_change_requests
   WHERE id=requested_id AND scope_id=public.platform_scope_uuid(requested_tenant) FOR UPDATE;
  IF NOT FOUND THEN RAISE EXCEPTION 'provider change not found' USING ERRCODE='P0002'; END IF;
  IF change.status<>'pending_approval' OR change.version<>expected_request_version OR change.requested_by=requested_actor OR change.expires_at<=requested_now THEN RAISE EXCEPTION 'provider change conflict' USING ERRCODE='40001'; END IF;
  SELECT * INTO binding FROM public.provider_operation_bindings WHERE id=change.binding_id FOR UPDATE;
  IF binding.version<>change.expected_binding_version THEN RAISE EXCEPTION 'provider binding version conflict' USING ERRCODE='40001'; END IF;
  IF approve AND change.requested_status='active' AND (NOT public.provider_operation_binding_policy_current(binding.id) OR NOT public.provider_operation_binding_health_ready(binding.id,requested_now)) THEN RAISE EXCEPTION 'provider policy or health evidence is not approved and current' USING ERRCODE='40001'; END IF;
  IF approve THEN
    UPDATE public.provider_operation_bindings SET status=change.requested_status,version=version+1,updated_at=requested_now WHERE id=binding.id RETURNING * INTO binding;
    IF binding.provider_kind='hosted' THEN
      PERFORM set_config('app.provider_ops_status_apply','true',true);
      UPDATE public.hosted_provider_configs SET status=binding.status,version=version+1,updated_at=requested_now
       WHERE id=binding.provider_id AND tenant_id=binding.tenant_id AND merchant_id=binding.merchant_id;
    END IF;
    UPDATE public.provider_operation_change_requests SET status='completed',approved_by=requested_actor,decision_step_up_at=requested_step_up,decision_reason=btrim(requested_reason),decided_at=requested_now,updated_at=requested_now,version=version+1 WHERE id=change.id;
    action:='approved';
  ELSE
    UPDATE public.provider_operation_change_requests SET status='rejected',rejected_by=requested_actor,decision_step_up_at=requested_step_up,decision_reason=btrim(requested_reason),decided_at=requested_now,updated_at=requested_now,version=version+1 WHERE id=change.id;
    action:='rejected';
  END IF;
  audit_id:=gen_random_uuid(); event_id:=gen_random_uuid();
  PERFORM public.append_platform_admin_audit(audit_id,binding.tenant_id,requested_actor,requested_session,'provider_status.'||action,'provider_binding',binding.id::text,btrim(requested_reason),jsonb_build_object('requested_status',change.requested_status,'binding_version',binding.version,'request_id',change.id),requested_now);
  INSERT INTO public.platform_admin_outbox(id,scope_id,tenant_id,aggregate_type,aggregate_id,aggregate_version,event_type,payload,occurred_at,available_at)
  VALUES(event_id,binding.scope_id,binding.tenant_id,'provider_binding',binding.id::text,binding.version,'platform_admin.provider_status.'||action,jsonb_build_object('event_id',event_id,'event_type','platform_admin.provider_status.'||action,'resource_type','provider_binding','resource_id',binding.id,'aggregate_version',binding.version,'occurred_at',requested_now,'details',jsonb_build_object('requested_status',change.requested_status,'request_id',change.id)),requested_now,requested_now);
  RETURN QUERY SELECT * FROM public.provider_operation_change_requests WHERE id=change.id;
END $$;
REVOKE ALL ON FUNCTION decide_provider_operation_change(uuid,uuid,bigint,boolean,text,uuid,text,timestamptz,timestamptz) FROM PUBLIC;

CREATE FUNCTION request_hosted_provider_policy(
  requested_id uuid,requested_binding uuid,requested_tenant uuid,expected_binding_version bigint,
  requested_payload jsonb,requested_probe_reference text,requested_reason text,requested_actor uuid,requested_session text,
  requested_step_up timestamptz,requested_now timestamptz
) RETURNS SETOF provider_hosted_policy_versions
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE binding public.provider_operation_bindings%ROWTYPE; next_version bigint; event_id uuid; audit_id uuid;
DECLARE required text[]:=ARRAY['health','create','status','cancel','refund','reconciliation'];
BEGIN
  requested_now:=clock_timestamp();
  IF current_setting('app.provider_ops_actor_id',true) IS DISTINCT FROM requested_actor::text
    OR current_setting('app.provider_ops_session_id',true) IS DISTINCT FROM requested_session
    OR requested_step_up<requested_now-interval '10 minutes' OR requested_step_up>requested_now+interval '10 seconds'
    OR NOT EXISTS(
      SELECT 1 FROM public.admin_role_bindings b JOIN public.admin_users u ON u.id=b.user_id
      JOIN public.admin_role_permissions rp ON rp.role_key=b.role_key
      WHERE b.user_id=requested_actor AND u.status='active' AND rp.permission_key='provider_ops:request'
        AND b.revoked_at IS NULL AND (b.expires_at IS NULL OR b.expires_at>requested_now)
        AND (b.tenant_id IS NULL OR b.tenant_id=requested_tenant)
    ) THEN RAISE EXCEPTION 'hosted provider policy request denied' USING ERRCODE='42501'; END IF;
  IF length(btrim(requested_reason)) NOT BETWEEN 8 AND 1000
    OR NOT public.provider_operation_policy_payload_valid(requested_payload,required)
    OR requested_probe_reference !~ '^[A-Za-z0-9][A-Za-z0-9._:-]{0,254}$'
  THEN RAISE EXCEPTION 'invalid hosted provider policy' USING ERRCODE='22023'; END IF;
  SELECT * INTO binding FROM public.provider_operation_bindings
   WHERE id=requested_binding AND scope_id=public.platform_scope_uuid(requested_tenant) FOR UPDATE;
  IF NOT FOUND OR binding.provider_kind<>'hosted' THEN RAISE EXCEPTION 'hosted provider binding not found' USING ERRCODE='P0002'; END IF;
  IF binding.version<>expected_binding_version OR binding.status<>'paused' THEN RAISE EXCEPTION 'hosted provider must be paused at the expected version' USING ERRCODE='40001'; END IF;
  UPDATE public.provider_hosted_policy_versions SET status='expired',decision_reason='approval window expired',decided_at=requested_now,updated_at=requested_now,row_version=row_version+1
   WHERE binding_id=binding.id AND status='pending_approval' AND expires_at<=requested_now;
  IF EXISTS(SELECT 1 FROM public.provider_hosted_policy_versions WHERE binding_id=binding.id AND status='pending_approval') THEN RAISE EXCEPTION 'pending hosted provider policy exists' USING ERRCODE='40001'; END IF;
  SELECT COALESCE(max(policy_version),0)+1 INTO next_version FROM public.provider_hosted_policy_versions WHERE binding_id=binding.id;
  INSERT INTO public.provider_hosted_policy_versions(id,binding_id,scope_id,tenant_id,policy_version,policy_payload,payload_hash,bootstrap_probe_reference,status,expected_binding_version,reason,requested_by,request_step_up_at,created_at,expires_at,updated_at)
  VALUES(requested_id,binding.id,binding.scope_id,binding.tenant_id,next_version,requested_payload,digest(convert_to(requested_payload::text||E'\n'||requested_probe_reference,'UTF8'),'sha256'),requested_probe_reference,'pending_approval',binding.version,btrim(requested_reason),requested_actor,requested_step_up,requested_now,requested_now+interval '30 minutes',requested_now);
  audit_id:=gen_random_uuid(); event_id:=gen_random_uuid();
  PERFORM public.append_platform_admin_audit(audit_id,binding.tenant_id,requested_actor,requested_session,'provider_policy.requested','provider_policy',requested_id::text,btrim(requested_reason),jsonb_build_object('binding_id',binding.id,'policy_version',next_version,'payload_hash',encode(digest(convert_to(requested_payload::text||E'\n'||requested_probe_reference,'UTF8'),'sha256'),'hex')),requested_now);
  INSERT INTO public.platform_admin_outbox(id,scope_id,tenant_id,aggregate_type,aggregate_id,aggregate_version,event_type,payload,occurred_at,available_at)
  VALUES(event_id,binding.scope_id,binding.tenant_id,'provider_policy',requested_id::text,next_version,'platform_admin.provider_policy.requested',jsonb_build_object('event_id',event_id,'event_type','platform_admin.provider_policy.requested','resource_type','provider_policy','resource_id',requested_id,'aggregate_version',next_version,'occurred_at',requested_now,'details',jsonb_build_object('binding_id',binding.id,'payload_hash',encode(digest(convert_to(requested_payload::text||E'\n'||requested_probe_reference,'UTF8'),'sha256'),'hex'))),requested_now,requested_now);
  RETURN QUERY SELECT * FROM public.provider_hosted_policy_versions WHERE id=requested_id;
END $$;
REVOKE ALL ON FUNCTION request_hosted_provider_policy(uuid,uuid,uuid,bigint,jsonb,text,text,uuid,text,timestamptz,timestamptz) FROM PUBLIC;

CREATE FUNCTION decide_hosted_provider_policy(
  requested_id uuid,requested_tenant uuid,expected_row_version bigint,approve boolean,
  requested_reason text,requested_actor uuid,requested_session text,
  requested_step_up timestamptz,requested_now timestamptz
) RETURNS SETOF provider_hosted_policy_versions
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE change public.provider_hosted_policy_versions%ROWTYPE; binding public.provider_operation_bindings%ROWTYPE;
DECLARE op text; policy jsonb; event_id uuid; audit_id uuid; action text;
DECLARE required text[]:=ARRAY['health','create','status','cancel','refund','reconciliation'];
BEGIN
  requested_now:=clock_timestamp();
  IF current_setting('app.provider_ops_actor_id',true) IS DISTINCT FROM requested_actor::text
    OR current_setting('app.provider_ops_session_id',true) IS DISTINCT FROM requested_session
    OR requested_step_up<requested_now-interval '10 minutes' OR requested_step_up>requested_now+interval '10 seconds'
    OR NOT EXISTS(
      SELECT 1 FROM public.admin_role_bindings b JOIN public.admin_users u ON u.id=b.user_id
      JOIN public.admin_role_permissions rp ON rp.role_key=b.role_key
      WHERE b.user_id=requested_actor AND u.status='active' AND rp.permission_key='provider_ops:approve'
        AND b.revoked_at IS NULL AND (b.expires_at IS NULL OR b.expires_at>requested_now)
        AND (b.tenant_id IS NULL OR b.tenant_id=requested_tenant)
    ) THEN RAISE EXCEPTION 'hosted provider policy decision denied' USING ERRCODE='42501'; END IF;
  IF length(btrim(requested_reason)) NOT BETWEEN 8 AND 1000 THEN RAISE EXCEPTION 'invalid hosted provider policy decision' USING ERRCODE='22023'; END IF;
  SELECT * INTO change FROM public.provider_hosted_policy_versions
   WHERE id=requested_id AND scope_id=public.platform_scope_uuid(requested_tenant) FOR UPDATE;
  IF NOT FOUND THEN RAISE EXCEPTION 'hosted provider policy not found' USING ERRCODE='P0002'; END IF;
  IF change.status<>'pending_approval' OR change.row_version<>expected_row_version OR change.requested_by=requested_actor OR change.expires_at<=requested_now THEN RAISE EXCEPTION 'hosted provider policy conflict' USING ERRCODE='40001'; END IF;
  SELECT * INTO binding FROM public.provider_operation_bindings WHERE id=change.binding_id FOR UPDATE;
  IF binding.version<>change.expected_binding_version OR binding.status<>'paused'
    OR NOT public.provider_operation_policy_payload_valid(change.policy_payload,required)
    OR change.payload_hash<>digest(convert_to(change.policy_payload::text||E'\n'||change.bootstrap_probe_reference,'UTF8'),'sha256')
  THEN RAISE EXCEPTION 'hosted provider policy evidence changed' USING ERRCODE='40001'; END IF;
  IF approve THEN
	UPDATE public.provider_hosted_policy_versions SET status='superseded',updated_at=requested_now,row_version=row_version+1
	 WHERE binding_id=binding.id AND status='approved_pending_probe' AND id<>change.id;
    FOREACH op IN ARRAY required LOOP
      policy:=change.policy_payload->op;
      UPDATE public.provider_operation_policies SET
        timeout_ms=(policy->>'timeout_ms')::integer,max_attempts=(policy->>'max_attempts')::smallint,
        backoff_ms=(policy->>'backoff_ms')::integer,rate_limit=(policy->>'rate_limit')::integer,
        rate_window_seconds=(policy->>'rate_window_seconds')::integer,max_health_age_seconds=(policy->>'max_health_age_seconds')::integer,
        max_lag_blocks=(policy->>'max_lag_blocks')::bigint,failure_threshold=(policy->>'failure_threshold')::smallint,
        open_seconds=(policy->>'open_seconds')::integer,half_open_successes=(policy->>'half_open_successes')::smallint,
        priority=(policy->>'priority')::smallint,failure_domain=policy->>'failure_domain',
        policy_snapshot_id=NULL,policy_snapshot_version=NULL,policy_fence_token=NULL,
        hosted_policy_version_id=change.id,approved_at=requested_now,updated_at=requested_now,version=version+1
       WHERE binding_id=binding.id AND operation=op;
    END LOOP;
    UPDATE public.provider_circuit_states SET state='closed',consecutive_failures=0,half_open_successes=0,opened_until=NULL,
      probe_lease_owner=NULL,probe_lease_until=NULL,last_success_at=NULL,last_observed_at=NULL,updated_at=requested_now,fence_token=fence_token+1,version=version+1
     WHERE binding_id=binding.id;
    UPDATE public.provider_operation_rate_windows SET window_started_at=requested_now,used=0,version=version+1 WHERE binding_id=binding.id;
    UPDATE public.provider_hosted_policy_versions SET status='approved_pending_probe',approved_by=requested_actor,decision_step_up_at=requested_step_up,decision_reason=btrim(requested_reason),decided_at=requested_now,updated_at=requested_now,row_version=row_version+1 WHERE id=change.id;
    action:='approved_pending_probe';
  ELSE
    UPDATE public.provider_hosted_policy_versions SET status='rejected',rejected_by=requested_actor,decision_step_up_at=requested_step_up,decision_reason=btrim(requested_reason),decided_at=requested_now,updated_at=requested_now,row_version=row_version+1 WHERE id=change.id;
    action:='rejected';
  END IF;
  audit_id:=gen_random_uuid(); event_id:=gen_random_uuid();
  PERFORM public.append_platform_admin_audit(audit_id,binding.tenant_id,requested_actor,requested_session,'provider_policy.'||action,'provider_policy',change.id::text,btrim(requested_reason),jsonb_build_object('binding_id',binding.id,'policy_version',change.policy_version,'payload_hash',encode(change.payload_hash,'hex')),requested_now);
  INSERT INTO public.platform_admin_outbox(id,scope_id,tenant_id,aggregate_type,aggregate_id,aggregate_version,event_type,payload,occurred_at,available_at)
  VALUES(event_id,binding.scope_id,binding.tenant_id,'provider_policy',change.id::text,change.policy_version,'platform_admin.provider_policy.'||action,jsonb_build_object('event_id',event_id,'event_type','platform_admin.provider_policy.'||action,'resource_type','provider_policy','resource_id',change.id,'aggregate_version',change.policy_version,'occurred_at',requested_now,'details',jsonb_build_object('binding_id',binding.id,'payload_hash',encode(change.payload_hash,'hex'))),requested_now,requested_now);
  RETURN QUERY SELECT * FROM public.provider_hosted_policy_versions WHERE id=change.id;
END $$;
REVOKE ALL ON FUNCTION decide_hosted_provider_policy(uuid,uuid,bigint,boolean,text,uuid,text,timestamptz,timestamptz) FROM PUBLIC;

CREATE FUNCTION claim_provider_health_probes(requested_owner text, requested_limit integer, requested_now timestamptz)
RETURNS TABLE(binding_id uuid,scope_id uuid,tenant_id uuid,provider_kind text,provider_id text,merchant_id uuid,chain_id text,config_logical_key text,platform_snapshot_id uuid,operation text,timeout_ms integer,max_attempts smallint,backoff_ms integer,rate_limit integer,rate_window_seconds integer,max_health_age_seconds integer,max_lag_blocks bigint,failure_threshold smallint,open_seconds integer,half_open_required smallint,priority smallint,failure_domain text,state text,consecutive_failures integer,half_successes integer,opened_until timestamptz,last_success_at timestamptz,last_observed_at timestamptz,lease_token bigint,fence_token bigint,circuit_version bigint,probe_reference text)
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
BEGIN
  IF requested_owner !~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$' OR requested_limit NOT BETWEEN 2 AND 128 THEN RAISE EXCEPTION 'invalid health claim' USING ERRCODE='22023'; END IF;
  RETURN QUERY
  WITH configured AS (
    SELECT c.binding_id,c.operation,p.failure_domain,
      CASE WHEN b.provider_kind='on_chain' THEN 'chain:'||b.chain_id ELSE 'hosted:'||b.tenant_id::text||':'||b.merchant_id::text END AS group_key,
      v.bootstrap_probe_reference AS probe_reference
      FROM public.provider_circuit_states c
      JOIN public.provider_operation_policies p USING(binding_id,operation)
      JOIN public.provider_operation_bindings b ON b.id=c.binding_id
      LEFT JOIN public.provider_hosted_policy_versions v ON v.id=p.hosted_policy_version_id AND v.binding_id=b.id
     WHERE p.approved_at IS NOT NULL AND b.status<>'disabled' AND (
       (b.provider_kind='on_chain' AND b.chain_id IS NOT NULL AND public.provider_operation_binding_policy_current(b.id))
       OR (b.provider_kind='hosted' AND p.operation='status' AND v.status IN ('approved_pending_probe','active') AND v.bootstrap_probe_reference IS NOT NULL)
     )
  ), eligible AS (
    SELECT configured.*
      FROM configured
      JOIN public.provider_circuit_states c USING(binding_id,operation)
      JOIN public.provider_operation_rate_windows r USING(binding_id,operation)
      JOIN public.provider_operation_policies p USING(binding_id,operation)
     WHERE (c.probe_lease_until IS NULL OR c.probe_lease_until<requested_now)
       AND (c.state<>'open' OR c.opened_until<=requested_now)
       AND (r.window_started_at+p.rate_window_seconds*interval '1 second'<=requested_now OR r.used<p.rate_limit)
  ), chosen_group AS (
    SELECT e.group_key,e.operation
      FROM eligible e
     GROUP BY e.group_key,e.operation
    HAVING count(*) BETWEEN 2 AND requested_limit
       AND count(DISTINCT e.failure_domain)>=2
       AND count(*)=(SELECT count(*) FROM configured configured_peer WHERE configured_peer.group_key=e.group_key AND configured_peer.operation=e.operation)
       AND pg_try_advisory_xact_lock(hashtextextended(e.group_key||E'\x1f'||e.operation,0))
     ORDER BY min(e.binding_id) LIMIT 1
  ), due AS (
    SELECT c.binding_id,c.operation
      FROM public.provider_circuit_states c
      JOIN public.provider_operation_rate_windows r USING(binding_id,operation)
      JOIN eligible e USING(binding_id,operation)
      JOIN chosen_group g ON g.group_key=e.group_key AND g.operation=e.operation
     FOR UPDATE OF c,r SKIP LOCKED
  ), rated AS (
    UPDATE public.provider_operation_rate_windows r SET
      window_started_at=CASE WHEN r.window_started_at+p.rate_window_seconds*interval '1 second'<=requested_now THEN requested_now ELSE r.window_started_at END,
      used=CASE WHEN r.window_started_at+p.rate_window_seconds*interval '1 second'<=requested_now THEN 1 ELSE r.used+1 END,
      version=r.version+1
      FROM due d JOIN public.provider_operation_policies p ON p.binding_id=d.binding_id AND p.operation=d.operation
     WHERE r.binding_id=d.binding_id AND r.operation=d.operation RETURNING r.binding_id,r.operation
  ), claimed AS (
    UPDATE public.provider_circuit_states c SET
      state=CASE WHEN c.state='open' THEN 'half_open' ELSE c.state END,
      opened_until=NULL,probe_lease_owner=requested_owner,probe_lease_token=c.probe_lease_token+1,
      probe_lease_until=requested_now+LEAST(
        600000,
        p.timeout_ms*p.max_attempts+p.backoff_ms*(power(2,p.max_attempts-1)::integer-1)+5000
      )*interval '1 millisecond',
      fence_token=CASE WHEN c.state='open' THEN c.fence_token+1 ELSE c.fence_token END,
      updated_at=requested_now,version=c.version+1
      FROM rated d JOIN public.provider_operation_policies p ON p.binding_id=d.binding_id AND p.operation=d.operation
     WHERE c.binding_id=d.binding_id AND c.operation=d.operation RETURNING c.*
  )
  SELECT b.id,b.scope_id,b.tenant_id,b.provider_kind,b.provider_id,b.merchant_id,b.chain_id,b.config_logical_key,b.platform_snapshot_id,p.operation,p.timeout_ms,p.max_attempts,p.backoff_ms,p.rate_limit,p.rate_window_seconds,p.max_health_age_seconds,p.max_lag_blocks,p.failure_threshold,p.open_seconds,p.half_open_successes,p.priority,p.failure_domain,c.state,c.consecutive_failures,c.half_open_successes,c.opened_until,c.last_success_at,c.last_observed_at,c.probe_lease_token,c.fence_token,c.version,configured.probe_reference
    FROM claimed c JOIN public.provider_operation_bindings b ON b.id=c.binding_id JOIN public.provider_operation_policies p ON p.binding_id=c.binding_id AND p.operation=c.operation JOIN configured USING(binding_id,operation);
END $$;
REVOKE ALL ON FUNCTION claim_provider_health_probes(text,integer,timestamptz) FROM PUBLIC;

CREATE FUNCTION load_hosted_provider_health_probe(requested_binding uuid,requested_owner text,requested_lease bigint,requested_fence bigint)
RETURNS TABLE (
  id text,tenant_id uuid,merchant_id uuid,adapter_kind text,api_origin text,
  create_path text,cancel_path text,status_path text,refund_path text,reconcile_path text,
  payment_url_origins text[],api_credential_ref text,api_key_id text,callback_secret_ref text,
  callback_key_id text,signature_scheme text,asset_id text,asset_decimals smallint,currency text,status text,
  provider_reference text
)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
  SELECT cfg.id,cfg.tenant_id,cfg.merchant_id,cfg.adapter_kind,cfg.api_origin,cfg.create_path,cfg.cancel_path,cfg.status_path,cfg.refund_path,cfg.reconcile_path,
    cfg.payment_url_origins,cfg.api_credential_ref,cfg.api_key_id,cfg.callback_secret_ref,cfg.callback_key_id,cfg.signature_scheme,cfg.asset_id,cfg.asset_decimals,cfg.currency::text,cfg.status,
    version.bootstrap_probe_reference
  FROM public.provider_operation_bindings b
  JOIN public.provider_circuit_states circuit ON circuit.binding_id=b.id AND circuit.operation='status'
  JOIN public.provider_operation_policies policy ON policy.binding_id=b.id AND policy.operation='status'
  JOIN public.provider_hosted_policy_versions version ON version.id=policy.hosted_policy_version_id AND version.status IN ('approved_pending_probe','active')
  JOIN public.hosted_provider_configs cfg ON cfg.id=b.provider_id AND cfg.tenant_id=b.tenant_id AND cfg.merchant_id=b.merchant_id
  WHERE b.id=requested_binding AND b.provider_kind='hosted' AND b.status<>'disabled' AND cfg.status<>'disabled'
    AND circuit.probe_lease_owner=requested_owner AND circuit.probe_lease_token=requested_lease AND circuit.fence_token=requested_fence
    AND circuit.probe_lease_until>=clock_timestamp()
$$;
REVOKE ALL ON FUNCTION load_hosted_provider_health_probe(uuid,text,bigint,bigint) FROM PUBLIC;

CREATE FUNCTION complete_provider_health_probe(requested_binding uuid,requested_operation text,requested_owner text,requested_lease bigint,requested_fence bigint,success boolean,category text,latency integer,lag bigint,observed timestamptz) RETURNS boolean
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE c public.provider_circuit_states%ROWTYPE; p public.provider_operation_policies%ROWTYPE; binding public.provider_operation_bindings%ROWTYPE; next_state text; next_failures integer; next_half integer; next_open timestamptz; event_id uuid;
BEGIN
  SELECT * INTO c FROM public.provider_circuit_states WHERE binding_id=requested_binding AND operation=requested_operation FOR UPDATE;
  IF NOT FOUND OR c.probe_lease_owner IS DISTINCT FROM requested_owner OR c.probe_lease_token<>requested_lease OR c.fence_token<>requested_fence OR c.probe_lease_until<GREATEST(observed,clock_timestamp()) THEN RETURN false; END IF;
  SELECT * INTO p FROM public.provider_operation_policies WHERE binding_id=requested_binding AND operation=requested_operation;
  IF category NOT IN ('none','timeout','dns','tls','connect','rate_limited','auth_rejected','upstream_4xx','upstream_5xx','invalid_response','chain_mismatch','genesis_mismatch','stale_head','divergent_response','policy_denied')
    OR success<>(category='none') OR latency NOT BETWEEN 0 AND 30000 OR lag<0 THEN RAISE EXCEPTION 'invalid health observation' USING ERRCODE='22023'; END IF;
  next_state:=c.state; next_failures:=CASE WHEN success THEN 0 ELSE c.consecutive_failures+1 END; next_half:=CASE WHEN success AND c.state='half_open' THEN c.half_open_successes+1 ELSE 0 END; next_open:=NULL;
  IF success AND c.state='half_open' AND next_half>=p.half_open_successes THEN next_state:='closed'; next_half:=0;
  ELSIF NOT success AND (c.state='half_open' OR next_failures>=p.failure_threshold) THEN next_state:='open'; next_open:=observed+p.open_seconds*interval '1 second'; END IF;
  INSERT INTO public.provider_health_observations(id,binding_id,scope_id,tenant_id,operation,outcome,error_category,latency_ms,lag_blocks,observed_at,expires_at)
  VALUES(gen_random_uuid(),c.binding_id,c.scope_id,c.tenant_id,c.operation,CASE WHEN success THEN 'success' ELSE 'failure' END,category,latency,lag,observed,observed+interval '24 hours');
  UPDATE public.provider_circuit_states SET state=next_state,consecutive_failures=next_failures,half_open_successes=next_half,opened_until=next_open,probe_lease_owner=NULL,probe_lease_until=NULL,last_success_at=CASE WHEN success THEN observed ELSE last_success_at END,last_observed_at=observed,updated_at=observed,fence_token=CASE WHEN state<>next_state THEN fence_token+1 ELSE fence_token END,version=version+1 WHERE binding_id=c.binding_id AND operation=c.operation;
  SELECT * INTO binding FROM public.provider_operation_bindings WHERE id=c.binding_id;
  IF success AND binding.provider_kind='hosted' AND requested_operation='status' AND p.hosted_policy_version_id IS NOT NULL THEN
    UPDATE public.provider_hosted_policy_versions SET status='superseded',updated_at=observed,row_version=row_version+1
     WHERE binding_id=binding.id AND status='active' AND id<>p.hosted_policy_version_id;
    UPDATE public.provider_hosted_policy_versions SET status='active',activated_at=observed,updated_at=observed,row_version=row_version+1
     WHERE id=p.hosted_policy_version_id AND binding_id=binding.id AND status='approved_pending_probe';
    IF FOUND THEN
      event_id:=gen_random_uuid();
      INSERT INTO public.platform_admin_outbox(id,scope_id,tenant_id,aggregate_type,aggregate_id,aggregate_version,event_type,payload,occurred_at,available_at)
      SELECT event_id,binding.scope_id,binding.tenant_id,'provider_policy',v.id::text,v.policy_version,'platform_admin.provider_policy.activated',jsonb_build_object('event_id',event_id,'event_type','platform_admin.provider_policy.activated','resource_type','provider_policy','resource_id',v.id,'aggregate_version',v.policy_version,'occurred_at',observed,'details',jsonb_build_object('binding_id',binding.id,'health_operation','status')),observed,observed
        FROM public.provider_hosted_policy_versions v WHERE v.id=p.hosted_policy_version_id;
    END IF;
  END IF;
  DELETE FROM public.provider_health_observations WHERE binding_id=c.binding_id AND operation=c.operation AND (expires_at<=observed OR id IN (SELECT id FROM public.provider_health_observations WHERE binding_id=c.binding_id AND operation=c.operation ORDER BY observed_at DESC,id DESC OFFSET 128));
  RETURN true;
END $$;
REVOKE ALL ON FUNCTION complete_provider_health_probe(uuid,text,text,bigint,bigint,boolean,text,integer,bigint,timestamptz) FROM PUBLIC;

-- Readiness exposes bounded aggregate state only. It cannot enumerate provider,
-- tenant, endpoint, credential or upstream error identities.
CREATE FUNCTION provider_health_worker_status(requested_now timestamptz)
RETURNS TABLE(ready boolean,admissible_peer_groups bigint,open_circuits bigint)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
  WITH current_policies AS (
    SELECT b.id,
      CASE WHEN b.provider_kind='on_chain' THEN 'chain:'||b.chain_id ELSE 'hosted:'||b.tenant_id::text||':'||b.merchant_id::text END AS peer_group,
      p.operation,p.failure_domain,p.max_health_age_seconds,c.state,c.last_success_at
      FROM public.provider_operation_bindings b
      JOIN public.provider_operation_policies p ON p.binding_id=b.id
      JOIN public.provider_circuit_states c ON c.binding_id=p.binding_id AND c.operation=p.operation
     WHERE b.status='active' AND p.approved_at IS NOT NULL
       AND public.provider_operation_binding_policy_current(b.id)
       AND ((b.provider_kind='on_chain' AND b.chain_id IS NOT NULL)
         OR (b.provider_kind='hosted' AND p.operation='status'))
  ), admitted_groups AS (
    SELECT peer_group,operation
      FROM current_policies
     WHERE state='closed' AND last_success_at IS NOT NULL
       AND last_success_at<=requested_now
       AND last_success_at+max_health_age_seconds*interval '1 second'>=requested_now
     GROUP BY peer_group,operation
    HAVING count(*)>=2 AND count(DISTINCT failure_domain)>=2
  ), totals AS (
    SELECT
      (SELECT count(*) FROM admitted_groups) AS groups,
      (SELECT count(*) FROM current_policies WHERE state='open') AS opened
  )
  SELECT groups>0,groups,opened FROM totals
$$;
REVOKE ALL ON FUNCTION provider_health_worker_status(timestamptz) FROM PUBLIC;

INSERT INTO admin_permissions(permission_key,description) VALUES
('provider_ops:read','Read secret-free provider operations state'),
('provider_ops:request','Request provider policy, pause or unpause'),
('provider_ops:approve','Independently approve provider policy, pause or unpause');
INSERT INTO admin_role_permissions(role_key,permission_key) VALUES
('auditor','provider_ops:read'),('treasury_operator','provider_ops:read'),
('security_admin','provider_ops:read'),('security_admin','provider_ops:request'),
('senior_approver','provider_ops:read'),('senior_approver','provider_ops:approve');

DO $$ BEGIN
  IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='platform_admin_runtime') THEN
    GRANT SELECT ON provider_operation_bindings,provider_operation_change_requests,provider_operation_idempotency,provider_hosted_policy_versions TO platform_admin_runtime;
    GRANT INSERT ON provider_operation_idempotency TO platform_admin_runtime;
    GRANT SELECT ON provider_operation_policies,provider_circuit_states,provider_health_observations TO platform_admin_runtime;
    GRANT EXECUTE ON FUNCTION request_provider_operation_change(uuid,uuid,uuid,text,bigint,text,uuid,text,timestamptz,timestamptz) TO platform_admin_runtime;
    GRANT EXECUTE ON FUNCTION decide_provider_operation_change(uuid,uuid,bigint,boolean,text,uuid,text,timestamptz,timestamptz) TO platform_admin_runtime;
    GRANT EXECUTE ON FUNCTION request_hosted_provider_policy(uuid,uuid,uuid,bigint,jsonb,text,text,uuid,text,timestamptz,timestamptz) TO platform_admin_runtime;
    GRANT EXECUTE ON FUNCTION decide_hosted_provider_policy(uuid,uuid,bigint,boolean,text,uuid,text,timestamptz,timestamptz) TO platform_admin_runtime;
  END IF;
  IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='merchant_scanner_worker') THEN
    GRANT SELECT ON provider_operation_bindings,provider_operation_policies,provider_circuit_states TO merchant_scanner_worker;
    GRANT EXECUTE ON FUNCTION provider_operation_binding_policy_current(uuid) TO merchant_scanner_worker;
  END IF;
  IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='merchant_provider_health_worker') THEN
    GRANT SELECT ON platform_config_heads,platform_config_snapshots,platform_emergency_pause_events TO merchant_provider_health_worker;
    GRANT EXECUTE ON FUNCTION claim_provider_health_probes(text,integer,timestamptz) TO merchant_provider_health_worker;
    GRANT EXECUTE ON FUNCTION complete_provider_health_probe(uuid,text,text,bigint,bigint,boolean,text,integer,bigint,timestamptz) TO merchant_provider_health_worker;
    GRANT EXECUTE ON FUNCTION provider_health_worker_status(timestamptz) TO merchant_provider_health_worker;
    GRANT EXECUTE ON FUNCTION load_hosted_provider_health_probe(uuid,text,bigint,bigint) TO merchant_provider_health_worker;
  END IF;
  IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='merchant_api_runtime') THEN
    GRANT EXECUTE ON FUNCTION admit_hosted_provider_operation(uuid,uuid,text,text,timestamptz) TO merchant_api_runtime;
    GRANT EXECUTE ON FUNCTION hosted_provider_callback_admitted(text) TO merchant_api_runtime;
  END IF;
  IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='merchant_plan_worker') THEN
    GRANT EXECUTE ON FUNCTION admit_hosted_provider_operation(uuid,uuid,text,text,timestamptz) TO merchant_plan_worker;
  END IF;
END $$;

COMMIT;
