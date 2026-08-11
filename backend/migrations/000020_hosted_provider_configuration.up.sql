BEGIN;

-- Hosted provider configuration is an immutable, secret-free control plane.
-- Secret material remains in externally mounted files; only bounded opaque
-- references and provider key identifiers are admitted here.
CREATE TABLE hosted_provider_config_manifests (
    id uuid PRIMARY KEY,
    scope_id uuid NOT NULL,
    tenant_id uuid NOT NULL,
    merchant_id uuid NOT NULL,
    provider_id text NOT NULL CHECK(provider_id ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$'),
    manifest_version bigint NOT NULL CHECK(manifest_version>0),
    change_kind text NOT NULL CHECK(change_kind IN ('provision','rotate','rollback','disable')),
    expected_head_version bigint NOT NULL CHECK(expected_head_version>=0),
    adapter_kind text NOT NULL CHECK(adapter_kind='hmac_json_v1'),
    api_origin text NOT NULL CHECK(api_origin ~ '^https://[^/?#]+$' AND length(api_origin)<=512),
    create_path text NOT NULL CHECK(create_path ~ '^/[A-Za-z0-9._~!$&''()*+,;=:@%/-]{0,255}$'),
    cancel_path text NOT NULL CHECK(cancel_path ~ '^/[A-Za-z0-9._~!$&''()*+,;=:@%/-]{0,255}$'),
    status_path text NOT NULL CHECK(status_path ~ '^/[A-Za-z0-9._~!$&''()*+,;=:@%/-]{0,255}$'),
    refund_path text NOT NULL CHECK(refund_path ~ '^/[A-Za-z0-9._~!$&''()*+,;=:@%/-]{0,255}$'),
    reconcile_path text NOT NULL CHECK(reconcile_path ~ '^/[A-Za-z0-9._~!$&''()*+,;=:@%/-]{0,255}$'),
    payment_url_origins text[] NOT NULL CHECK(hosted_https_origins_valid(payment_url_origins)),
    api_credential_ref text NOT NULL CHECK(api_credential_ref ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$'),
    api_key_id text NOT NULL CHECK(length(api_key_id) BETWEEN 1 AND 128 AND api_key_id !~ '[[:cntrl:]]'),
    callback_secret_ref text NOT NULL CHECK(callback_secret_ref ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$'),
    callback_key_id text NOT NULL CHECK(length(callback_key_id) BETWEEN 1 AND 128 AND callback_key_id !~ '[[:cntrl:]]'),
    signature_scheme text NOT NULL CHECK(signature_scheme='hmac-sha256'),
    asset_id text NOT NULL REFERENCES assets(id) ON DELETE RESTRICT,
    asset_decimals smallint NOT NULL CHECK(asset_decimals BETWEEN 0 AND 77),
    currency char(3) NOT NULL CHECK(currency ~ '^[A-Z]{3}$'),
    callback_overlap_seconds integer NOT NULL CHECK(callback_overlap_seconds BETWEEN 0 AND 86400),
    probe_reference text NOT NULL CHECK(probe_reference ~ '^[A-Za-z0-9][A-Za-z0-9._:-]{0,254}$'),
    payload_hash bytea NOT NULL CHECK(octet_length(payload_hash)=32),
    created_at timestamptz NOT NULL,
    CHECK(scope_id=platform_scope_uuid(tenant_id)),
    FOREIGN KEY(merchant_id,tenant_id) REFERENCES merchants(id,tenant_id) ON DELETE RESTRICT,
    UNIQUE(provider_id,manifest_version),
    UNIQUE(id,tenant_id,merchant_id),
    UNIQUE(id,provider_id,tenant_id,merchant_id),
    UNIQUE(id,provider_id,scope_id,tenant_id,merchant_id,manifest_version),
    UNIQUE(id,provider_id,scope_id,tenant_id,merchant_id)
);
CREATE INDEX hosted_provider_config_callback_key_lookup
  ON hosted_provider_config_manifests(provider_id,callback_key_id,manifest_version DESC);

CREATE TABLE hosted_provider_config_workflows (
    manifest_id uuid PRIMARY KEY REFERENCES hosted_provider_config_manifests(id) ON DELETE RESTRICT,
    provider_id text NOT NULL CHECK(provider_id ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$'),
    scope_id uuid NOT NULL,
    tenant_id uuid NOT NULL,
    merchant_id uuid NOT NULL,
    status text NOT NULL CHECK(status IN ('pending_approval','approved_pending_probe','active','rejected','superseded','expired','probe_failed','legacy_unadmitted','legacy_superseded')),
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
    callback_accept_until timestamptz,
    probe_lease_owner text,
    probe_lease_token bigint NOT NULL DEFAULT 0 CHECK(probe_lease_token>=0),
    probe_lease_until timestamptz,
    probe_attempts integer NOT NULL DEFAULT 0 CHECK(probe_attempts BETWEEN 0 AND 1000),
    probe_error_category text CHECK(probe_error_category IS NULL OR probe_error_category IN ('timeout','dns','tls','connect','rate_limited','auth_rejected','upstream_4xx','upstream_5xx','invalid_response','policy_denied')),
    probe_response_digest bytea CHECK(probe_response_digest IS NULL OR octet_length(probe_response_digest)=32),
    probe_tls_spki_digest bytea CHECK(probe_tls_spki_digest IS NULL OR octet_length(probe_tls_spki_digest)=32),
    probe_observed_at timestamptz,
    next_probe_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    version bigint NOT NULL DEFAULT 1 CHECK(version>0),
    CHECK(scope_id=platform_scope_uuid(tenant_id)),
    FOREIGN KEY(manifest_id,provider_id,scope_id,tenant_id,merchant_id)
      REFERENCES hosted_provider_config_manifests(id,provider_id,scope_id,tenant_id,merchant_id) ON DELETE RESTRICT,
    CHECK(expires_at>created_at AND expires_at<=created_at+interval '30 minutes'),
    CHECK(approved_by IS NULL OR approved_by<>requested_by),
    CHECK(rejected_by IS NULL OR rejected_by<>requested_by),
    CHECK((probe_lease_owner IS NULL)=(probe_lease_until IS NULL)),
    CHECK((probe_response_digest IS NULL)=(probe_tls_spki_digest IS NULL)),
    CHECK((status IN ('active','superseded'))=(activated_at IS NOT NULL)),
    CHECK(status<>'active' OR activated_at IS NOT NULL)
);
CREATE UNIQUE INDEX hosted_provider_config_pending_unique ON hosted_provider_config_workflows(provider_id)
  WHERE status IN ('pending_approval','approved_pending_probe');

CREATE TABLE hosted_provider_config_heads (
    provider_id text PRIMARY KEY,
    scope_id uuid NOT NULL,
    tenant_id uuid NOT NULL,
    merchant_id uuid NOT NULL,
    active_manifest_id uuid NOT NULL REFERENCES hosted_provider_config_manifests(id) ON DELETE RESTRICT,
    active_manifest_version bigint NOT NULL CHECK(active_manifest_version>0),
    head_version bigint NOT NULL CHECK(head_version>0),
    updated_at timestamptz NOT NULL,
    CHECK(scope_id=platform_scope_uuid(tenant_id)),
    FOREIGN KEY(active_manifest_id,provider_id,scope_id,tenant_id,merchant_id,active_manifest_version)
      REFERENCES hosted_provider_config_manifests(id,provider_id,scope_id,tenant_id,merchant_id,manifest_version) ON DELETE RESTRICT,
    FOREIGN KEY(merchant_id,tenant_id) REFERENCES merchants(id,tenant_id) ON DELETE RESTRICT
);

ALTER TABLE provider_hosted_policy_versions ADD COLUMN config_manifest_id uuid;
CREATE FUNCTION provider_hosted_policy_bind_config_manifest() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
BEGIN
  SELECT h.active_manifest_id INTO NEW.config_manifest_id
  FROM public.provider_operation_bindings b
  JOIN public.hosted_provider_config_heads h ON h.provider_id=b.provider_id AND h.tenant_id=b.tenant_id AND h.merchant_id=b.merchant_id
  WHERE b.id=NEW.binding_id AND b.provider_kind='hosted';
  IF NEW.config_manifest_id IS NULL THEN
    RAISE EXCEPTION 'hosted provider configuration head is unavailable' USING ERRCODE='40001';
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER provider_hosted_policy_bind_config_manifest_before_insert
BEFORE INSERT ON provider_hosted_policy_versions FOR EACH ROW EXECUTE FUNCTION provider_hosted_policy_bind_config_manifest();
REVOKE ALL ON FUNCTION provider_hosted_policy_bind_config_manifest() FROM PUBLIC;

CREATE TABLE hosted_provider_config_probe_incidents (
    id uuid PRIMARY KEY,
    manifest_id uuid NOT NULL,
    scope_id uuid NOT NULL,
    tenant_id uuid NOT NULL,
    merchant_id uuid NOT NULL,
    provider_id text NOT NULL,
    error_category text NOT NULL CHECK(error_category IN ('timeout','dns','tls','connect','rate_limited','auth_rejected','upstream_4xx','upstream_5xx','invalid_response','policy_denied')),
    attempt_count integer NOT NULL CHECK(attempt_count BETWEEN 1 AND 1000),
    created_at timestamptz NOT NULL,
    CHECK(scope_id=platform_scope_uuid(tenant_id)),
    FOREIGN KEY(manifest_id,provider_id,scope_id,tenant_id,merchant_id)
      REFERENCES hosted_provider_config_manifests(id,provider_id,scope_id,tenant_id,merchant_id) ON DELETE RESTRICT,
    UNIQUE(manifest_id)
);

CREATE TABLE hosted_provider_config_idempotency (
    scope_id uuid NOT NULL,
    tenant_id uuid NOT NULL,
    actor_id uuid NOT NULL,
    operation text NOT NULL CHECK(operation IN ('provider_config.request','provider_config.approve','provider_config.reject')),
    idempotency_key text NOT NULL CHECK(length(idempotency_key) BETWEEN 8 AND 255 AND idempotency_key=btrim(idempotency_key)),
    request_hash bytea NOT NULL CHECK(octet_length(request_hash)=32),
    response_body jsonb NOT NULL CHECK(jsonb_typeof(response_body)='object' AND pg_column_size(response_body)<=65536),
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    CHECK(scope_id=platform_scope_uuid(tenant_id)),
    PRIMARY KEY(scope_id,actor_id,operation,idempotency_key)
);

CREATE TYPE hosted_provider_config_public AS (
  id uuid,provider_id text,tenant_id uuid,merchant_id uuid,manifest_version bigint,change_kind text,
  expected_head_version bigint,status text,adapter_kind text,asset_id text,asset_decimals smallint,currency text,
  api_key_id text,callback_key_id text,callback_overlap_seconds integer,payload_hash text,reason text,
  requested_by uuid,approved_by uuid,rejected_by uuid,decision_reason text,created_at timestamptz,expires_at timestamptz,
  decided_at timestamptz,activated_at timestamptz,callback_accept_until timestamptz,
  probe_response_digest text,probe_tls_spki_digest text,probe_observed_at timestamptz,head_version bigint,row_version bigint
);

CREATE FUNCTION hosted_provider_config_manifest_immutable() RETURNS trigger
LANGUAGE plpgsql SET search_path=pg_catalog,public AS $$
BEGIN RAISE EXCEPTION 'hosted provider configuration manifests are immutable' USING ERRCODE='55000'; END $$;
CREATE TRIGGER hosted_provider_config_manifest_reject_mutation BEFORE UPDATE OR DELETE ON hosted_provider_config_manifests
  FOR EACH ROW EXECUTE FUNCTION hosted_provider_config_manifest_immutable();

ALTER TABLE hosted_provider_config_manifests ENABLE ROW LEVEL SECURITY;
ALTER TABLE hosted_provider_config_workflows ENABLE ROW LEVEL SECURITY;
ALTER TABLE hosted_provider_config_heads ENABLE ROW LEVEL SECURITY;
ALTER TABLE hosted_provider_config_idempotency ENABLE ROW LEVEL SECURITY;
ALTER TABLE hosted_provider_config_probe_incidents ENABLE ROW LEVEL SECURITY;
ALTER TABLE hosted_provider_config_manifests FORCE ROW LEVEL SECURITY;
ALTER TABLE hosted_provider_config_workflows FORCE ROW LEVEL SECURITY;
ALTER TABLE hosted_provider_config_heads FORCE ROW LEVEL SECURITY;
ALTER TABLE hosted_provider_config_idempotency FORCE ROW LEVEL SECURITY;
ALTER TABLE hosted_provider_config_probe_incidents FORCE ROW LEVEL SECURITY;
CREATE POLICY hosted_provider_config_manifest_scope ON hosted_provider_config_manifests
 USING ((current_setting('app.platform_admin_global',true)='true') OR tenant_id::text=ANY(string_to_array(current_setting('app.platform_admin_tenants',true),',')))
 WITH CHECK ((current_setting('app.platform_admin_global',true)='true') OR tenant_id::text=ANY(string_to_array(current_setting('app.platform_admin_tenants',true),',')));
CREATE POLICY hosted_provider_config_workflow_scope ON hosted_provider_config_workflows
 USING ((current_setting('app.platform_admin_global',true)='true') OR tenant_id::text=ANY(string_to_array(current_setting('app.platform_admin_tenants',true),',')))
 WITH CHECK ((current_setting('app.platform_admin_global',true)='true') OR tenant_id::text=ANY(string_to_array(current_setting('app.platform_admin_tenants',true),',')));
CREATE POLICY hosted_provider_config_head_scope ON hosted_provider_config_heads
 USING ((current_setting('app.platform_admin_global',true)='true') OR tenant_id::text=ANY(string_to_array(current_setting('app.platform_admin_tenants',true),',')))
 WITH CHECK ((current_setting('app.platform_admin_global',true)='true') OR tenant_id::text=ANY(string_to_array(current_setting('app.platform_admin_tenants',true),',')));
CREATE POLICY hosted_provider_config_idempotency_scope ON hosted_provider_config_idempotency
 USING ((current_setting('app.platform_admin_global',true)='true') OR tenant_id::text=ANY(string_to_array(current_setting('app.platform_admin_tenants',true),',')))
 WITH CHECK ((current_setting('app.platform_admin_global',true)='true') OR tenant_id::text=ANY(string_to_array(current_setting('app.platform_admin_tenants',true),',')));
CREATE POLICY hosted_provider_config_probe_incident_scope ON hosted_provider_config_probe_incidents
 USING ((current_setting('app.platform_admin_global',true)='true') OR tenant_id::text=ANY(string_to_array(current_setting('app.platform_admin_tenants',true),',')))
 WITH CHECK ((current_setting('app.platform_admin_global',true)='true') OR tenant_id::text=ANY(string_to_array(current_setting('app.platform_admin_tenants',true),',')));

-- Exact-key validation prevents accepting unknown fields which might otherwise
-- look configured while being ignored by the runtime.
CREATE FUNCTION hosted_provider_config_manifest_valid(payload jsonb) RETURNS boolean
LANGUAGE sql IMMUTABLE STRICT SET search_path=pg_catalog,public AS $$
  SELECT jsonb_typeof(payload)='object'
    AND (SELECT array_agg(k ORDER BY k) FROM jsonb_object_keys(payload) k)=ARRAY[
      'adapter_kind','api_credential_ref','api_key_id','api_origin','asset_decimals','asset_id',
      'callback_key_id','callback_overlap_seconds','callback_secret_ref','cancel_path','change_kind',
      'create_path','currency','payment_url_origins','probe_reference','reconcile_path','refund_path',
      'signature_scheme','status_path']::text[]
    AND NOT EXISTS(
      SELECT 1 FROM jsonb_each(payload) entry
      WHERE entry.key=ANY(ARRAY[
        'adapter_kind','api_credential_ref','api_key_id','api_origin','asset_id','callback_key_id',
        'callback_secret_ref','cancel_path','change_kind','create_path','currency','probe_reference',
        'reconcile_path','refund_path','signature_scheme','status_path']::text[])
        AND jsonb_typeof(entry.value)<>'string')
    AND payload->>'adapter_kind'='hmac_json_v1'
    AND payload->>'signature_scheme'='hmac-sha256'
    AND payload->>'change_kind' IN ('provision','rotate','rollback','disable')
    AND payload->>'api_origin' ~ '^https://[^/?#@[:space:]]+$' AND length(payload->>'api_origin')<=512
    AND payload->>'create_path' ~ '^/[A-Za-z0-9._~!$&''()*+,;=:@%/-]{0,255}$'
    AND payload->>'cancel_path' ~ '^/[A-Za-z0-9._~!$&''()*+,;=:@%/-]{0,255}$'
    AND payload->>'status_path' ~ '^/[A-Za-z0-9._~!$&''()*+,;=:@%/-]{0,255}$'
    AND payload->>'refund_path' ~ '^/[A-Za-z0-9._~!$&''()*+,;=:@%/-]{0,255}$'
    AND payload->>'reconcile_path' ~ '^/[A-Za-z0-9._~!$&''()*+,;=:@%/-]{0,255}$'
    AND payload->>'api_credential_ref' ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$'
    AND payload->>'callback_secret_ref' ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$'
    AND length(payload->>'api_key_id') BETWEEN 1 AND 128
    AND length(payload->>'callback_key_id') BETWEEN 1 AND 128
    AND payload->>'probe_reference' ~ '^[A-Za-z0-9][A-Za-z0-9._:-]{0,254}$'
    AND payload->>'asset_id' ~ '^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$'
    AND payload->>'currency' ~ '^[A-Z]{3}$'
    AND jsonb_typeof(payload->'asset_decimals')='number' AND (payload->>'asset_decimals') ~ '^(0|[1-9][0-9]?)$' AND (payload->>'asset_decimals')::integer BETWEEN 0 AND 77
    AND jsonb_typeof(payload->'callback_overlap_seconds')='number' AND (payload->>'callback_overlap_seconds') ~ '^(0|[1-9][0-9]{0,4})$' AND (payload->>'callback_overlap_seconds')::integer BETWEEN 0 AND 86400
    AND jsonb_typeof(payload->'payment_url_origins')='array'
    AND NOT EXISTS(SELECT 1 FROM jsonb_array_elements(payload->'payment_url_origins') v WHERE jsonb_typeof(v)<>'string')
    AND public.hosted_https_origins_valid(ARRAY(SELECT jsonb_array_elements_text(payload->'payment_url_origins')))
$$;
REVOKE ALL ON FUNCTION hosted_provider_config_manifest_valid(jsonb) FROM PUBLIC;

CREATE FUNCTION provider_config_public_rows(requested_tenant uuid,requested_cursor uuid,requested_limit integer,requested_id uuid DEFAULT NULL)
RETURNS SETOF hosted_provider_config_public
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
SELECT m.id,m.provider_id,m.tenant_id,m.merchant_id,m.manifest_version,m.change_kind,m.expected_head_version,w.status,
       m.adapter_kind,m.asset_id,m.asset_decimals,m.currency::text,m.api_key_id,m.callback_key_id,m.callback_overlap_seconds,
       encode(m.payload_hash,'hex'),w.reason,w.requested_by,w.approved_by,w.rejected_by,w.decision_reason,w.created_at,w.expires_at,
       w.decided_at,w.activated_at,w.callback_accept_until,encode(w.probe_response_digest,'hex'),encode(w.probe_tls_spki_digest,'hex'),
       w.probe_observed_at,COALESCE(h.head_version,0),w.version
FROM public.hosted_provider_config_manifests m
JOIN public.hosted_provider_config_workflows w ON w.manifest_id=m.id
LEFT JOIN public.hosted_provider_config_heads h ON h.provider_id=m.provider_id AND h.active_manifest_id=m.id
WHERE m.scope_id=public.platform_scope_uuid(requested_tenant)
  AND requested_limit BETWEEN 1 AND 201
  AND (current_setting('app.platform_admin_global',true)='true'
    OR requested_tenant::text=ANY(string_to_array(current_setting('app.platform_admin_tenants',true),',')))
  AND (requested_id IS NULL OR m.id=requested_id)
  AND (requested_cursor IS NULL OR m.id>requested_cursor)
ORDER BY m.id LIMIT requested_limit
$$;
REVOKE ALL ON FUNCTION provider_config_public_rows(uuid,uuid,integer,uuid) FROM PUBLIC;

CREATE FUNCTION request_hosted_provider_config(
 requested_id uuid,requested_tenant uuid,requested_merchant uuid,requested_provider text,expected_head bigint,
 requested_payload jsonb,requested_reason text,requested_actor uuid,requested_session text,
 requested_step_up timestamptz,requested_now timestamptz
) RETURNS SETOF hosted_provider_config_public
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE next_version bigint; head public.hosted_provider_config_heads%ROWTYPE; event_id uuid; audit_id uuid; digest_value bytea;
BEGIN
  requested_now:=clock_timestamp();
  IF current_setting('app.provider_config_actor_id',true) IS DISTINCT FROM requested_actor::text
    OR current_setting('app.provider_config_session_id',true) IS DISTINCT FROM requested_session
    OR NOT (COALESCE(current_setting('app.platform_admin_global',true),'')='true'
      OR requested_tenant::text=ANY(string_to_array(COALESCE(current_setting('app.platform_admin_tenants',true),''),',')))
    OR requested_step_up IS NULL OR requested_step_up<requested_now-interval '10 minutes' OR requested_step_up>requested_now+interval '10 seconds'
    OR NOT EXISTS(SELECT 1 FROM public.admin_role_bindings b JOIN public.admin_users u ON u.id=b.user_id
      JOIN public.admin_role_permissions rp ON rp.role_key=b.role_key
      WHERE b.user_id=requested_actor AND u.status='active' AND rp.permission_key='provider_config:request'
        AND b.revoked_at IS NULL AND (b.expires_at IS NULL OR b.expires_at>requested_now)
        AND (b.tenant_id IS NULL OR b.tenant_id=requested_tenant))
  THEN RAISE EXCEPTION 'provider configuration request denied' USING ERRCODE='42501'; END IF;
  IF requested_provider !~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$'
    OR length(btrim(requested_reason)) NOT BETWEEN 8 AND 1000
    OR NOT public.hosted_provider_config_manifest_valid(requested_payload)
    OR NOT EXISTS(SELECT 1 FROM public.merchants WHERE id=requested_merchant AND tenant_id=requested_tenant AND status='active')
    OR NOT EXISTS(SELECT 1 FROM public.assets WHERE id=requested_payload->>'asset_id' AND decimals=(requested_payload->>'asset_decimals')::smallint AND status='active')
  THEN RAISE EXCEPTION 'invalid provider configuration request' USING ERRCODE='22023'; END IF;
  PERFORM pg_advisory_xact_lock(hashtextextended(requested_provider,20));
  SELECT * INTO head FROM public.hosted_provider_config_heads WHERE provider_id=requested_provider FOR UPDATE;
  IF NOT FOUND THEN
    IF expected_head<>0 OR requested_payload->>'change_kind'<>'provision' OR EXISTS(SELECT 1 FROM public.hosted_provider_configs WHERE id=requested_provider) THEN
      RAISE EXCEPTION 'provider configuration head conflict' USING ERRCODE='40001'; END IF;
  ELSIF head.head_version<>expected_head OR head.tenant_id<>requested_tenant OR head.merchant_id<>requested_merchant OR requested_payload->>'change_kind'='provision' THEN
    RAISE EXCEPTION 'provider configuration head conflict' USING ERRCODE='40001';
  END IF;
  UPDATE public.hosted_provider_config_workflows SET status='expired',decision_reason='approval window expired',decided_at=requested_now,
    updated_at=requested_now,version=version+1 WHERE provider_id=requested_provider AND status='pending_approval' AND expires_at<=requested_now;
  IF EXISTS(SELECT 1 FROM public.hosted_provider_config_manifests m JOIN public.hosted_provider_config_workflows w ON w.manifest_id=m.id
    WHERE m.provider_id=requested_provider AND w.status IN ('pending_approval','approved_pending_probe'))
  THEN RAISE EXCEPTION 'pending provider configuration exists' USING ERRCODE='40001'; END IF;
  IF EXISTS(SELECT 1 FROM public.hosted_provider_config_manifests m WHERE m.provider_id=requested_provider
    AND ((m.api_key_id=requested_payload->>'api_key_id' AND m.api_credential_ref<>requested_payload->>'api_credential_ref')
      OR (m.callback_key_id=requested_payload->>'callback_key_id' AND m.callback_secret_ref<>requested_payload->>'callback_secret_ref')))
  THEN RAISE EXCEPTION 'provider key identifier cannot be rebound to another external reference' USING ERRCODE='40001'; END IF;
  SELECT COALESCE(max(manifest_version),0)+1 INTO next_version FROM public.hosted_provider_config_manifests WHERE provider_id=requested_provider;
  digest_value:=digest(convert_to(requested_payload::text,'UTF8'),'sha256');
  INSERT INTO public.hosted_provider_config_manifests(id,scope_id,tenant_id,merchant_id,provider_id,manifest_version,change_kind,expected_head_version,
    adapter_kind,api_origin,create_path,cancel_path,status_path,refund_path,reconcile_path,payment_url_origins,api_credential_ref,api_key_id,
    callback_secret_ref,callback_key_id,signature_scheme,asset_id,asset_decimals,currency,callback_overlap_seconds,probe_reference,payload_hash,created_at)
  SELECT requested_id,public.platform_scope_uuid(requested_tenant),requested_tenant,requested_merchant,requested_provider,next_version,
    requested_payload->>'change_kind',expected_head,requested_payload->>'adapter_kind',requested_payload->>'api_origin',requested_payload->>'create_path',
    requested_payload->>'cancel_path',requested_payload->>'status_path',requested_payload->>'refund_path',requested_payload->>'reconcile_path',
    ARRAY(SELECT jsonb_array_elements_text(requested_payload->'payment_url_origins')),requested_payload->>'api_credential_ref',requested_payload->>'api_key_id',
    requested_payload->>'callback_secret_ref',requested_payload->>'callback_key_id',requested_payload->>'signature_scheme',requested_payload->>'asset_id',
    (requested_payload->>'asset_decimals')::smallint,requested_payload->>'currency',(requested_payload->>'callback_overlap_seconds')::integer,
    requested_payload->>'probe_reference',digest_value,requested_now;
  INSERT INTO public.hosted_provider_config_workflows(manifest_id,provider_id,scope_id,tenant_id,merchant_id,status,reason,requested_by,request_step_up_at,created_at,expires_at,next_probe_at,updated_at)
  VALUES(requested_id,requested_provider,public.platform_scope_uuid(requested_tenant),requested_tenant,requested_merchant,'pending_approval',btrim(requested_reason),requested_actor,requested_step_up,requested_now,requested_now+interval '30 minutes',requested_now,requested_now);
  audit_id:=gen_random_uuid(); event_id:=gen_random_uuid();
  PERFORM public.append_platform_admin_audit(audit_id,requested_tenant,requested_actor,requested_session,'provider_config.requested','hosted_provider_config',requested_id::text,btrim(requested_reason),jsonb_build_object('provider_id',requested_provider,'manifest_version',next_version,'payload_hash',encode(digest_value,'hex'),'change_kind',requested_payload->>'change_kind'),requested_now);
  INSERT INTO public.platform_admin_outbox(id,scope_id,tenant_id,aggregate_type,aggregate_id,aggregate_version,event_type,payload,occurred_at,available_at)
  VALUES(event_id,public.platform_scope_uuid(requested_tenant),requested_tenant,'hosted_provider_config',requested_id::text,next_version,'platform_admin.provider_config.requested',jsonb_build_object('event_id',event_id,'event_type','platform_admin.provider_config.requested','resource_type','hosted_provider_config','resource_id',requested_id,'aggregate_version',next_version,'occurred_at',requested_now,'details',jsonb_build_object('provider_id',requested_provider,'payload_hash',encode(digest_value,'hex'),'change_kind',requested_payload->>'change_kind')),requested_now,requested_now);
  RETURN QUERY SELECT * FROM public.provider_config_public_rows(requested_tenant,NULL,1,requested_id);
END $$;

-- PostgreSQL requires a concrete record descriptor at the call site; the Go
-- repository supplies the exact public projection and never reads manifests.
REVOKE ALL ON FUNCTION request_hosted_provider_config(uuid,uuid,uuid,text,bigint,jsonb,text,uuid,text,timestamptz,timestamptz) FROM PUBLIC;

CREATE FUNCTION decide_hosted_provider_config(
 requested_id uuid,requested_tenant uuid,expected_row_version bigint,approve boolean,requested_reason text,
 requested_actor uuid,requested_session text,requested_step_up timestamptz,requested_now timestamptz
) RETURNS SETOF hosted_provider_config_public
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE workflow public.hosted_provider_config_workflows%ROWTYPE; manifest public.hosted_provider_config_manifests%ROWTYPE;
DECLARE current_head bigint; old_manifest uuid; event_id uuid; audit_id uuid; action text;
BEGIN
 requested_now:=clock_timestamp();
 IF current_setting('app.provider_config_actor_id',true) IS DISTINCT FROM requested_actor::text
   OR current_setting('app.provider_config_session_id',true) IS DISTINCT FROM requested_session
   OR NOT (COALESCE(current_setting('app.platform_admin_global',true),'')='true'
     OR requested_tenant::text=ANY(string_to_array(COALESCE(current_setting('app.platform_admin_tenants',true),''),',')))
   OR requested_step_up IS NULL OR requested_step_up<requested_now-interval '10 minutes' OR requested_step_up>requested_now+interval '10 seconds'
   OR NOT EXISTS(SELECT 1 FROM public.admin_role_bindings b JOIN public.admin_users u ON u.id=b.user_id
     JOIN public.admin_role_permissions rp ON rp.role_key=b.role_key WHERE b.user_id=requested_actor AND u.status='active'
       AND rp.permission_key='provider_config:approve' AND b.revoked_at IS NULL AND (b.expires_at IS NULL OR b.expires_at>requested_now)
       AND (b.tenant_id IS NULL OR b.tenant_id=requested_tenant))
 THEN RAISE EXCEPTION 'provider configuration decision denied' USING ERRCODE='42501'; END IF;
 IF approve IS NULL OR expected_row_version IS NULL OR requested_reason IS NULL OR length(btrim(requested_reason)) NOT BETWEEN 8 AND 1000
 THEN RAISE EXCEPTION 'invalid provider configuration decision' USING ERRCODE='22023'; END IF;
 SELECT * INTO workflow FROM public.hosted_provider_config_workflows WHERE manifest_id=requested_id AND scope_id=public.platform_scope_uuid(requested_tenant) FOR UPDATE;
 IF NOT FOUND THEN RAISE EXCEPTION 'provider configuration decision conflict' USING ERRCODE='40001'; END IF;
 SELECT * INTO manifest FROM public.hosted_provider_config_manifests
 WHERE id=requested_id AND tenant_id=requested_tenant AND provider_id=workflow.provider_id AND merchant_id=workflow.merchant_id;
 IF NOT FOUND OR workflow.status<>'pending_approval' OR workflow.version<>expected_row_version OR workflow.requested_by=requested_actor OR workflow.expires_at<=requested_now
 THEN RAISE EXCEPTION 'provider configuration decision conflict' USING ERRCODE='40001'; END IF;
 SELECT head_version INTO current_head FROM public.hosted_provider_config_heads WHERE provider_id=manifest.provider_id FOR UPDATE;
 IF COALESCE(current_head,0)<>manifest.expected_head_version OR manifest.payload_hash<>digest(convert_to(jsonb_build_object(
    'adapter_kind',manifest.adapter_kind,'api_credential_ref',manifest.api_credential_ref,'api_key_id',manifest.api_key_id,'api_origin',manifest.api_origin,
    'asset_decimals',manifest.asset_decimals,'asset_id',manifest.asset_id,'callback_key_id',manifest.callback_key_id,
    'callback_overlap_seconds',manifest.callback_overlap_seconds,'callback_secret_ref',manifest.callback_secret_ref,'cancel_path',manifest.cancel_path,
    'change_kind',manifest.change_kind,'create_path',manifest.create_path,'currency',manifest.currency::text,'payment_url_origins',to_jsonb(manifest.payment_url_origins),
    'probe_reference',manifest.probe_reference,'reconcile_path',manifest.reconcile_path,'refund_path',manifest.refund_path,
    'signature_scheme',manifest.signature_scheme,'status_path',manifest.status_path)::text,'UTF8'),'sha256')
 THEN RAISE EXCEPTION 'provider configuration evidence changed' USING ERRCODE='40001'; END IF;
 IF approve AND manifest.change_kind='disable' THEN
   SELECT active_manifest_id INTO old_manifest FROM public.hosted_provider_config_heads WHERE provider_id=manifest.provider_id FOR UPDATE;
   UPDATE public.hosted_provider_config_workflows
     SET status=CASE WHEN status='legacy_unadmitted' THEN 'legacy_superseded' ELSE 'superseded' END,
       callback_accept_until=requested_now,updated_at=requested_now,version=version+1
     WHERE manifest_id=old_manifest AND status IN ('active','legacy_unadmitted');
   UPDATE public.hosted_provider_config_heads SET active_manifest_id=manifest.id,active_manifest_version=manifest.manifest_version,
     head_version=head_version+1,updated_at=requested_now WHERE provider_id=manifest.provider_id;
   UPDATE public.hosted_provider_configs SET adapter_kind=manifest.adapter_kind,api_origin=manifest.api_origin,create_path=manifest.create_path,
     cancel_path=manifest.cancel_path,status_path=manifest.status_path,refund_path=manifest.refund_path,reconcile_path=manifest.reconcile_path,
     payment_url_origins=manifest.payment_url_origins,api_credential_ref=manifest.api_credential_ref,api_key_id=manifest.api_key_id,
     callback_secret_ref=manifest.callback_secret_ref,callback_key_id=manifest.callback_key_id,signature_scheme=manifest.signature_scheme,
     asset_id=manifest.asset_id,asset_decimals=manifest.asset_decimals,currency=manifest.currency,status='disabled',updated_at=requested_now,version=version+1
     WHERE id=manifest.provider_id AND tenant_id=manifest.tenant_id AND merchant_id=manifest.merchant_id;
   IF NOT FOUND THEN RAISE EXCEPTION 'provider configuration head conflict' USING ERRCODE='40001'; END IF;
   UPDATE public.provider_operation_bindings SET status='disabled',updated_at=requested_now,version=version+1
     WHERE provider_kind='hosted' AND provider_id=manifest.provider_id AND tenant_id=manifest.tenant_id AND merchant_id=manifest.merchant_id;
   UPDATE public.hosted_provider_config_workflows SET status='active',approved_by=requested_actor,decision_step_up_at=requested_step_up,
     decision_reason=btrim(requested_reason),decided_at=requested_now,activated_at=requested_now,updated_at=requested_now,version=version+1
     WHERE manifest_id=requested_id;
   action:='activated_disabled';
 ELSIF approve THEN
   UPDATE public.hosted_provider_config_workflows SET status='approved_pending_probe',approved_by=requested_actor,decision_step_up_at=requested_step_up,
     decision_reason=btrim(requested_reason),decided_at=requested_now,next_probe_at=requested_now,updated_at=requested_now,version=version+1 WHERE manifest_id=requested_id;
   action:='approved_pending_probe';
 ELSE
   UPDATE public.hosted_provider_config_workflows SET status='rejected',rejected_by=requested_actor,decision_step_up_at=requested_step_up,
     decision_reason=btrim(requested_reason),decided_at=requested_now,updated_at=requested_now,version=version+1 WHERE manifest_id=requested_id;
   action:='rejected';
 END IF;
 audit_id:=gen_random_uuid(); event_id:=gen_random_uuid();
 PERFORM public.append_platform_admin_audit(audit_id,manifest.tenant_id,requested_actor,requested_session,'provider_config.'||action,'hosted_provider_config',manifest.id::text,btrim(requested_reason),jsonb_build_object('provider_id',manifest.provider_id,'manifest_version',manifest.manifest_version,'payload_hash',encode(manifest.payload_hash,'hex')),requested_now);
 INSERT INTO public.platform_admin_outbox(id,scope_id,tenant_id,aggregate_type,aggregate_id,aggregate_version,event_type,payload,occurred_at,available_at)
 VALUES(event_id,manifest.scope_id,manifest.tenant_id,'hosted_provider_config',manifest.id::text,manifest.manifest_version,'platform_admin.provider_config.'||action,jsonb_build_object('event_id',event_id,'event_type','platform_admin.provider_config.'||action,'resource_type','hosted_provider_config','resource_id',manifest.id,'aggregate_version',manifest.manifest_version,'occurred_at',requested_now,'details',jsonb_build_object('provider_id',manifest.provider_id,'payload_hash',encode(manifest.payload_hash,'hex'))),requested_now,requested_now);
 RETURN QUERY SELECT * FROM public.provider_config_public_rows(requested_tenant,NULL,1,requested_id);
END $$;
REVOKE ALL ON FUNCTION decide_hosted_provider_config(uuid,uuid,bigint,boolean,text,uuid,text,timestamptz,timestamptz) FROM PUBLIC;

CREATE FUNCTION claim_hosted_provider_config_probes(requested_owner text,requested_limit integer,requested_now timestamptz)
RETURNS TABLE(manifest_id uuid,tenant_id uuid,merchant_id uuid,provider_id text,manifest_version bigint,lease_token bigint,
 api_origin text,status_path text,api_credential_ref text,api_key_id text,probe_reference text,adapter_kind text,asset_id text,asset_decimals smallint)
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
BEGIN
 requested_now:=clock_timestamp();
 IF requested_owner !~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$' OR requested_limit NOT BETWEEN 1 AND 64
 THEN RAISE EXCEPTION 'invalid provider configuration probe claim' USING ERRCODE='22023'; END IF;
 RETURN QUERY WITH picked AS (
   SELECT w.manifest_id FROM public.hosted_provider_config_workflows w JOIN public.hosted_provider_config_manifests m ON m.id=w.manifest_id
   WHERE w.status='approved_pending_probe' AND w.next_probe_at<=requested_now AND w.probe_attempts<8
     AND (w.probe_lease_until IS NULL OR w.probe_lease_until<requested_now)
   ORDER BY w.updated_at,w.manifest_id FOR UPDATE OF w SKIP LOCKED LIMIT requested_limit
 ), claimed AS (
   UPDATE public.hosted_provider_config_workflows w SET probe_lease_owner=requested_owner,probe_lease_token=w.probe_lease_token+1,
     probe_lease_until=requested_now+interval '45 seconds',probe_attempts=w.probe_attempts+1,updated_at=requested_now,version=w.version+1
   FROM picked WHERE w.manifest_id=picked.manifest_id RETURNING w.*
 )
 SELECT m.id,m.tenant_id,m.merchant_id,m.provider_id,m.manifest_version,w.probe_lease_token,m.api_origin,m.status_path,
   m.api_credential_ref,m.api_key_id,m.probe_reference,m.adapter_kind,m.asset_id,m.asset_decimals
 FROM claimed w JOIN public.hosted_provider_config_manifests m ON m.id=w.manifest_id ORDER BY m.id;
END $$;
REVOKE ALL ON FUNCTION claim_hosted_provider_config_probes(text,integer,timestamptz) FROM PUBLIC;

CREATE FUNCTION complete_hosted_provider_config_probe(
 requested_manifest uuid,requested_owner text,requested_lease_token bigint,success boolean,error_category text,
 response_digest bytea,tls_spki_digest bytea,observed_at timestamptz
) RETURNS boolean
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE workflow public.hosted_provider_config_workflows%ROWTYPE; manifest public.hosted_provider_config_manifests%ROWTYPE;
DECLARE old_manifest uuid; requested_now timestamptz:=clock_timestamp(); audit_id uuid; event_id uuid; incident_id uuid; terminal_failure boolean;
BEGIN
 IF success IS NULL OR error_category IS NULL OR observed_at IS NULL OR requested_lease_token IS NULL
   OR error_category NOT IN ('none','timeout','dns','tls','connect','rate_limited','auth_rejected','upstream_4xx','upstream_5xx','invalid_response','policy_denied')
   OR observed_at<requested_now-interval '5 minutes' OR observed_at>requested_now+interval '10 seconds'
   OR (success AND (error_category<>'none' OR octet_length(response_digest)<>32 OR octet_length(tls_spki_digest)<>32))
   OR (NOT success AND error_category='none')
 THEN RAISE EXCEPTION 'invalid provider configuration probe result' USING ERRCODE='22023'; END IF;
 SELECT * INTO workflow FROM public.hosted_provider_config_workflows WHERE manifest_id=requested_manifest FOR UPDATE;
 SELECT * INTO manifest FROM public.hosted_provider_config_manifests WHERE id=requested_manifest;
 IF workflow.status<>'approved_pending_probe' OR workflow.probe_lease_owner IS DISTINCT FROM requested_owner
   OR workflow.probe_lease_token<>requested_lease_token OR workflow.probe_lease_until<requested_now
 THEN RAISE EXCEPTION 'provider configuration probe lease lost' USING ERRCODE='40001'; END IF;
 IF NOT success THEN
   terminal_failure:=workflow.probe_attempts>=8 OR error_category='policy_denied';
   UPDATE public.hosted_provider_config_workflows SET status=CASE WHEN terminal_failure THEN 'probe_failed' ELSE status END,
     probe_lease_owner=NULL,probe_lease_until=NULL,probe_error_category=error_category,
     probe_response_digest=NULL,probe_tls_spki_digest=NULL,probe_observed_at=observed_at,
     next_probe_at=CASE WHEN terminal_failure THEN requested_now ELSE requested_now+least(interval '1 hour',interval '1 minute'*power(2,least(workflow.probe_attempts-1,6))) END,
     updated_at=requested_now,version=version+1
   WHERE manifest_id=requested_manifest;
   IF terminal_failure THEN
     incident_id:=gen_random_uuid(); audit_id:=gen_random_uuid(); event_id:=gen_random_uuid();
     INSERT INTO public.hosted_provider_config_probe_incidents(id,manifest_id,scope_id,tenant_id,merchant_id,provider_id,error_category,attempt_count,created_at)
     VALUES(incident_id,manifest.id,manifest.scope_id,manifest.tenant_id,manifest.merchant_id,manifest.provider_id,error_category,workflow.probe_attempts,requested_now)
     ON CONFLICT(manifest_id) DO NOTHING;
     PERFORM public.append_platform_admin_audit(audit_id,manifest.tenant_id,'00000000-0000-0000-0000-000000000020'::uuid,'provider-config-worker:'||requested_owner,
       'provider_config.probe_failed','hosted_provider_config',manifest.id::text,'configuration probe exhausted',
       jsonb_build_object('provider_id',manifest.provider_id,'manifest_version',manifest.manifest_version,'payload_hash',encode(manifest.payload_hash,'hex'),'error_category',error_category,'attempt_count',workflow.probe_attempts),requested_now);
     INSERT INTO public.platform_admin_outbox(id,scope_id,tenant_id,aggregate_type,aggregate_id,aggregate_version,event_type,payload,occurred_at,available_at)
     VALUES(event_id,manifest.scope_id,manifest.tenant_id,'hosted_provider_config',manifest.id::text,manifest.manifest_version,'platform_admin.provider_config.probe_failed',
       jsonb_build_object('event_id',event_id,'event_type','platform_admin.provider_config.probe_failed','resource_type','hosted_provider_config','resource_id',manifest.id,
         'aggregate_version',manifest.manifest_version,'occurred_at',requested_now,'details',jsonb_build_object('provider_id',manifest.provider_id,'payload_hash',encode(manifest.payload_hash,'hex'),'error_category',error_category,'attempt_count',workflow.probe_attempts)),requested_now,requested_now);
   END IF;
   RETURN false;
 END IF;
 SELECT active_manifest_id INTO old_manifest FROM public.hosted_provider_config_heads WHERE provider_id=manifest.provider_id FOR UPDATE;
 IF COALESCE((SELECT head_version FROM public.hosted_provider_config_heads WHERE provider_id=manifest.provider_id),0)<>manifest.expected_head_version
 THEN RAISE EXCEPTION 'provider configuration head changed during probe' USING ERRCODE='40001'; END IF;
 IF EXISTS(SELECT 1 FROM public.hosted_provider_config_heads h WHERE h.provider_id=manifest.provider_id AND (h.tenant_id<>manifest.tenant_id OR h.merchant_id<>manifest.merchant_id))
 THEN RAISE EXCEPTION 'provider configuration scope changed during probe' USING ERRCODE='40001'; END IF;
 IF old_manifest IS NOT NULL THEN
   UPDATE public.hosted_provider_config_workflows
   SET status=CASE WHEN status='legacy_unadmitted' THEN 'legacy_superseded' ELSE 'superseded' END,
     callback_accept_until=requested_now+make_interval(secs=>manifest.callback_overlap_seconds),updated_at=requested_now,version=version+1
   WHERE manifest_id=old_manifest AND status IN ('active','legacy_unadmitted');
 END IF;
 INSERT INTO public.hosted_provider_config_heads(provider_id,scope_id,tenant_id,merchant_id,active_manifest_id,active_manifest_version,head_version,updated_at)
 VALUES(manifest.provider_id,manifest.scope_id,manifest.tenant_id,manifest.merchant_id,manifest.id,manifest.manifest_version,1,requested_now)
 ON CONFLICT(provider_id) DO UPDATE SET active_manifest_id=EXCLUDED.active_manifest_id,active_manifest_version=EXCLUDED.active_manifest_version,
   head_version=hosted_provider_config_heads.head_version+1,updated_at=EXCLUDED.updated_at
   WHERE hosted_provider_config_heads.tenant_id=EXCLUDED.tenant_id AND hosted_provider_config_heads.merchant_id=EXCLUDED.merchant_id;
 INSERT INTO public.hosted_provider_configs(id,tenant_id,merchant_id,adapter_kind,api_origin,create_path,cancel_path,status_path,refund_path,reconcile_path,
   payment_url_origins,api_credential_ref,api_key_id,callback_secret_ref,callback_key_id,signature_scheme,asset_id,asset_decimals,currency,status,created_at,updated_at,version)
 VALUES(manifest.provider_id,manifest.tenant_id,manifest.merchant_id,manifest.adapter_kind,manifest.api_origin,manifest.create_path,manifest.cancel_path,manifest.status_path,
   manifest.refund_path,manifest.reconcile_path,manifest.payment_url_origins,manifest.api_credential_ref,manifest.api_key_id,manifest.callback_secret_ref,
   manifest.callback_key_id,manifest.signature_scheme,manifest.asset_id,manifest.asset_decimals,manifest.currency,
   CASE WHEN manifest.change_kind='disable' THEN 'disabled' ELSE 'paused' END,requested_now,requested_now,1)
 ON CONFLICT(id) DO UPDATE SET adapter_kind=EXCLUDED.adapter_kind,api_origin=EXCLUDED.api_origin,create_path=EXCLUDED.create_path,
   cancel_path=EXCLUDED.cancel_path,status_path=EXCLUDED.status_path,refund_path=EXCLUDED.refund_path,reconcile_path=EXCLUDED.reconcile_path,
   payment_url_origins=EXCLUDED.payment_url_origins,api_credential_ref=EXCLUDED.api_credential_ref,api_key_id=EXCLUDED.api_key_id,
   callback_secret_ref=EXCLUDED.callback_secret_ref,callback_key_id=EXCLUDED.callback_key_id,signature_scheme=EXCLUDED.signature_scheme,
   asset_id=EXCLUDED.asset_id,asset_decimals=EXCLUDED.asset_decimals,currency=EXCLUDED.currency,
   status='paused',
   updated_at=requested_now,version=hosted_provider_configs.version+1;
 UPDATE public.provider_operation_bindings SET status='paused',updated_at=requested_now,version=version+1
 WHERE provider_kind='hosted' AND provider_id=manifest.provider_id AND tenant_id=manifest.tenant_id AND merchant_id=manifest.merchant_id;
 UPDATE public.hosted_provider_config_workflows SET status='active',activated_at=requested_now,callback_accept_until=NULL,
   probe_lease_owner=NULL,probe_lease_until=NULL,probe_error_category=NULL,probe_response_digest=response_digest,
   probe_tls_spki_digest=tls_spki_digest,probe_observed_at=observed_at,updated_at=requested_now,version=version+1
 WHERE manifest_id=requested_manifest;
 audit_id:=gen_random_uuid(); event_id:=gen_random_uuid();
 PERFORM public.append_platform_admin_audit(audit_id,manifest.tenant_id,'00000000-0000-0000-0000-000000000020'::uuid,'provider-config-worker:'||requested_owner,
   'provider_config.activated','hosted_provider_config',manifest.id::text,'approved configuration probe verified',
   jsonb_build_object('provider_id',manifest.provider_id,'manifest_version',manifest.manifest_version,'payload_hash',encode(manifest.payload_hash,'hex'),
     'response_digest',encode(response_digest,'hex'),'tls_spki_digest',encode(tls_spki_digest,'hex')),requested_now);
 INSERT INTO public.platform_admin_outbox(id,scope_id,tenant_id,aggregate_type,aggregate_id,aggregate_version,event_type,payload,occurred_at,available_at)
 VALUES(event_id,manifest.scope_id,manifest.tenant_id,'hosted_provider_config',manifest.id::text,manifest.manifest_version,'platform_admin.provider_config.activated',
   jsonb_build_object('event_id',event_id,'event_type','platform_admin.provider_config.activated','resource_type','hosted_provider_config','resource_id',manifest.id,
     'aggregate_version',manifest.manifest_version,'occurred_at',requested_now,'details',jsonb_build_object('provider_id',manifest.provider_id,'payload_hash',encode(manifest.payload_hash,'hex'),
       'response_digest',encode(response_digest,'hex'),'tls_spki_digest',encode(tls_spki_digest,'hex'))),requested_now,requested_now);
 RETURN true;
END $$;
REVOKE ALL ON FUNCTION complete_hosted_provider_config_probe(uuid,text,bigint,boolean,text,bytea,bytea,timestamptz) FROM PUBLIC;

-- Exact key-id admission. Unknown/missing/duplicate key IDs are rejected in
-- the runtime before this lookup; no key trial or downgrade fallback exists.
CREATE FUNCTION hosted_provider_callback_config_admitted(requested_provider_id text,requested_key_id text)
RETURNS TABLE (
 id text,tenant_id uuid,merchant_id uuid,adapter_kind text,api_origin text,create_path text,cancel_path text,status_path text,
 refund_path text,reconcile_path text,payment_url_origins text[],api_credential_ref text,api_key_id text,callback_secret_ref text,
 callback_key_id text,signature_scheme text,asset_id text,asset_decimals smallint,currency text,status text,config_manifest_id uuid,config_version bigint
)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
SELECT m.provider_id,m.tenant_id,m.merchant_id,m.adapter_kind,m.api_origin,m.create_path,m.cancel_path,m.status_path,m.refund_path,m.reconcile_path,
 m.payment_url_origins,m.api_credential_ref,m.api_key_id,m.callback_secret_ref,m.callback_key_id,m.signature_scheme,m.asset_id,m.asset_decimals,m.currency::text,c.status,m.id,m.manifest_version
FROM public.hosted_provider_config_manifests m
JOIN public.hosted_provider_config_workflows w ON w.manifest_id=m.id
JOIN public.hosted_provider_config_heads h ON h.provider_id=m.provider_id
JOIN public.hosted_provider_configs c ON c.id=h.provider_id AND c.tenant_id=h.tenant_id AND c.merchant_id=h.merchant_id
JOIN public.tenants t ON t.id=m.tenant_id AND t.status='active'
JOIN public.merchants merchant ON merchant.id=m.merchant_id AND merchant.tenant_id=m.tenant_id AND merchant.status='active'
WHERE m.provider_id=requested_provider_id AND m.callback_key_id=requested_key_id AND c.status IN ('active','paused')
 AND ((w.status IN ('active','legacy_unadmitted') AND h.active_manifest_id=m.id)
   OR (w.status IN ('superseded','legacy_superseded') AND w.callback_accept_until>clock_timestamp()))
 AND public.admit_hosted_provider_operation(m.tenant_id,m.merchant_id,m.provider_id,'callback',clock_timestamp())
ORDER BY (h.active_manifest_id=m.id) DESC
LIMIT 1
$$;
REVOKE ALL ON FUNCTION hosted_provider_callback_config_admitted(text,text) FROM PUBLIC;

CREATE FUNCTION hosted_provider_outbound_config_admitted(requested_tenant uuid,requested_merchant uuid,requested_provider text,requested_operation text)
RETURNS TABLE (
 id text,tenant_id uuid,merchant_id uuid,adapter_kind text,api_origin text,create_path text,cancel_path text,status_path text,
 refund_path text,reconcile_path text,payment_url_origins text[],api_credential_ref text,api_key_id text,callback_secret_ref text,
 callback_key_id text,signature_scheme text,asset_id text,asset_decimals smallint,currency text,status text,config_manifest_id uuid,config_version bigint
)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
SELECT c.id,c.tenant_id,c.merchant_id,c.adapter_kind,c.api_origin,c.create_path,c.cancel_path,c.status_path,c.refund_path,c.reconcile_path,
 c.payment_url_origins,c.api_credential_ref,c.api_key_id,c.callback_secret_ref,c.callback_key_id,c.signature_scheme,c.asset_id,c.asset_decimals,
 c.currency::text,c.status,m.id,m.manifest_version
FROM public.hosted_provider_configs c
JOIN public.hosted_provider_config_heads h ON h.provider_id=c.id AND h.tenant_id=c.tenant_id AND h.merchant_id=c.merchant_id
JOIN public.hosted_provider_config_manifests m ON m.id=h.active_manifest_id AND m.provider_id=h.provider_id
JOIN public.hosted_provider_config_workflows w ON w.manifest_id=m.id AND w.status='active'
WHERE c.id=requested_provider AND c.tenant_id=requested_tenant AND c.merchant_id=requested_merchant AND c.status='active'
 AND requested_operation IN ('create','status','cancel','refund','reconciliation')
 AND public.admit_hosted_provider_operation(c.tenant_id,c.merchant_id,c.id,requested_operation,clock_timestamp())
$$;
REVOKE ALL ON FUNCTION hosted_provider_outbound_config_admitted(uuid,uuid,text,text) FROM PUBLIC;

ALTER TABLE provider_inbox ADD COLUMN config_manifest_id uuid;
ALTER TABLE provider_inbox ADD COLUMN config_version bigint;
ALTER TABLE provider_prebind_inbox ADD COLUMN config_manifest_id uuid;
ALTER TABLE provider_prebind_inbox ADD COLUMN config_version bigint;

-- Existing owner-provisioned rows are pinned only as legacy callback evidence.
-- They are never treated as approved or probed: outbound is paused and a new
-- two-human manifest plus a real private probe is required before admission.
WITH inserted AS (
 INSERT INTO hosted_provider_config_manifests(id,scope_id,tenant_id,merchant_id,provider_id,manifest_version,change_kind,expected_head_version,
  adapter_kind,api_origin,create_path,cancel_path,status_path,refund_path,reconcile_path,payment_url_origins,api_credential_ref,api_key_id,
  callback_secret_ref,callback_key_id,signature_scheme,asset_id,asset_decimals,currency,callback_overlap_seconds,probe_reference,payload_hash,created_at)
 SELECT gen_random_uuid(),platform_scope_uuid(c.tenant_id),c.tenant_id,c.merchant_id,c.id,1,'provision',0,c.adapter_kind,c.api_origin,c.create_path,c.cancel_path,
  c.status_path,c.refund_path,c.reconcile_path,c.payment_url_origins,c.api_credential_ref,c.api_key_id,c.callback_secret_ref,c.callback_key_id,c.signature_scheme,
  c.asset_id,c.asset_decimals,c.currency,0,'legacy-unadmitted',digest(convert_to('legacy-unadmitted:'||c.id||':'||c.version::text,'UTF8'),'sha256'),c.created_at
 FROM hosted_provider_configs c RETURNING *
), workflows AS (
 INSERT INTO hosted_provider_config_workflows(manifest_id,provider_id,scope_id,tenant_id,merchant_id,status,reason,requested_by,request_step_up_at,
  created_at,expires_at,next_probe_at,updated_at,version)
 SELECT i.id,i.provider_id,i.scope_id,i.tenant_id,i.merchant_id,'legacy_unadmitted','legacy configuration requires independent reprovisioning',
  '00000000-0000-0000-0000-000000000020'::uuid,i.created_at,i.created_at,i.created_at+interval '1 minute',i.created_at,i.created_at,1
 FROM inserted i RETURNING *
)
INSERT INTO hosted_provider_config_heads(provider_id,scope_id,tenant_id,merchant_id,active_manifest_id,active_manifest_version,head_version,updated_at)
SELECT m.provider_id,m.scope_id,m.tenant_id,m.merchant_id,m.id,m.manifest_version,1,m.created_at
FROM hosted_provider_config_manifests m JOIN workflows w ON w.manifest_id=m.id;

UPDATE hosted_provider_configs
SET status=CASE WHEN status='disabled' THEN 'disabled' ELSE 'paused' END,
    updated_at=clock_timestamp(),version=version+1;
UPDATE provider_operation_bindings b
SET status=CASE WHEN c.status='disabled' THEN 'disabled' ELSE 'paused' END,
    updated_at=clock_timestamp(),version=b.version+1
FROM hosted_provider_configs c
WHERE b.provider_kind='hosted' AND b.provider_id=c.id AND b.tenant_id=c.tenant_id AND b.merchant_id=c.merchant_id;

UPDATE provider_hosted_policy_versions v SET config_manifest_id=h.active_manifest_id
FROM provider_operation_bindings b JOIN hosted_provider_config_heads h
  ON h.provider_id=b.provider_id AND h.tenant_id=b.tenant_id AND h.merchant_id=b.merchant_id
WHERE v.binding_id=b.id;
ALTER TABLE provider_hosted_policy_versions ALTER COLUMN config_manifest_id SET NOT NULL;
ALTER TABLE provider_hosted_policy_versions ADD CONSTRAINT provider_hosted_policy_config_manifest_fk
  FOREIGN KEY(config_manifest_id) REFERENCES hosted_provider_config_manifests(id) ON DELETE RESTRICT;

CREATE OR REPLACE FUNCTION provider_operation_binding_policy_current(requested_binding uuid) RETURNS boolean
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
         AND v.status='active' AND v.config_manifest_id=config_head.active_manifest_id
         FROM public.provider_operation_policies p
         JOIN public.provider_hosted_policy_versions v ON v.id=p.hosted_policy_version_id AND v.binding_id=b.id
         JOIN public.hosted_provider_config_heads config_head ON config_head.provider_id=b.provider_id AND config_head.tenant_id=b.tenant_id AND config_head.merchant_id=b.merchant_id
         JOIN public.hosted_provider_config_workflows config_workflow ON config_workflow.manifest_id=config_head.active_manifest_id AND config_workflow.status='active'
        WHERE p.binding_id=b.id AND p.operation=ANY(ARRAY['health','create','status','cancel','refund','reconciliation'])
        GROUP BY v.id,v.status,v.config_manifest_id,config_head.active_manifest_id,config_workflow.manifest_id),false)
    ELSE false END
  FROM public.provider_operation_bindings b WHERE b.id=requested_binding
$$;
REVOKE ALL ON FUNCTION provider_operation_binding_policy_current(uuid) FROM PUBLIC;

UPDATE provider_inbox i SET config_manifest_id=h.active_manifest_id,config_version=h.active_manifest_version
FROM hosted_provider_config_heads h WHERE h.provider_id=i.provider_id;
UPDATE provider_prebind_inbox i SET config_manifest_id=h.active_manifest_id,config_version=h.active_manifest_version
FROM hosted_provider_config_heads h WHERE h.provider_id=i.provider_id;
ALTER TABLE provider_inbox ALTER COLUMN config_manifest_id SET NOT NULL;
ALTER TABLE provider_inbox ALTER COLUMN config_version SET NOT NULL;
ALTER TABLE provider_prebind_inbox ALTER COLUMN config_manifest_id SET NOT NULL;
ALTER TABLE provider_prebind_inbox ALTER COLUMN config_version SET NOT NULL;
ALTER TABLE provider_inbox ADD CONSTRAINT provider_inbox_config_manifest_fk FOREIGN KEY(config_manifest_id,provider_id,tenant_id,merchant_id) REFERENCES hosted_provider_config_manifests(id,provider_id,tenant_id,merchant_id) ON DELETE RESTRICT;
ALTER TABLE provider_prebind_inbox ADD CONSTRAINT provider_prebind_config_manifest_fk FOREIGN KEY(config_manifest_id,provider_id,tenant_id,merchant_id) REFERENCES hosted_provider_config_manifests(id,provider_id,tenant_id,merchant_id) ON DELETE RESTRICT;

DROP FUNCTION claim_hosted_prebind_recoveries(timestamptz,timestamptz,integer);
CREATE FUNCTION claim_hosted_prebind_recoveries(claim_now timestamptz,lease_until timestamptz,claim_limit integer)
RETURNS TABLE (
 id uuid,claim_token uuid,attempt integer,tenant_id uuid,merchant_id uuid,provider_id text,route_id text,
 provider_event_id text,provider_reference text,provider_status text,asset_id text,amount_atomic text,
 asset_decimals smallint,raw_body bytea,raw_body_digest bytea,signature_scheme text,signature_key_id text,
 signature_digest bytea,config_manifest_id uuid,config_version bigint,provider_paused_at_receipt boolean,
 provider_occurred_at timestamptz,received_at timestamptz
)
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
BEGIN
 IF claim_limit<1 OR claim_limit>100 OR lease_until<=claim_now OR lease_until>claim_now+interval '5 minutes' THEN
   RAISE EXCEPTION 'invalid hosted recovery claim bounds' USING ERRCODE='22023';
 END IF;
 RETURN QUERY WITH picked AS (
   SELECT p.id FROM public.provider_prebind_inbox p
   LEFT JOIN public.provider_orders o ON o.provider_id=p.provider_id AND o.provider_reference=p.provider_reference
     AND o.tenant_id=p.tenant_id AND o.merchant_id=p.merchant_id
   WHERE p.state='pending' AND p.next_attempt_at<=claim_now AND(p.claim_token IS NULL OR p.claim_until<claim_now)
     AND(o.id IS NOT NULL OR p.expires_at<=claim_now)
   ORDER BY CASE WHEN o.id IS NOT NULL THEN 0 ELSE 1 END,p.next_attempt_at,p.id
   FOR UPDATE OF p SKIP LOCKED LIMIT claim_limit
 ), claimed AS (
   UPDATE public.provider_prebind_inbox p SET claim_token=gen_random_uuid(),claim_until=lease_until,
     attempt_count=p.attempt_count+1,updated_at=claim_now,version=p.version+1
   FROM picked WHERE p.id=picked.id RETURNING p.*
 )
 SELECT p.id,p.claim_token,p.attempt_count,p.tenant_id,p.merchant_id,p.provider_id,
   COALESCE((SELECT o.route_id::text FROM public.provider_orders o WHERE o.provider_id=p.provider_id
     AND o.provider_reference=p.provider_reference AND o.tenant_id=p.tenant_id AND o.merchant_id=p.merchant_id),''),
   p.provider_event_id,p.provider_reference,p.provider_status,p.asset_id,p.amount_atomic::text,p.asset_decimals,
   p.raw_body,p.raw_body_digest,p.signature_scheme,p.signature_key_id,p.signature_digest,p.config_manifest_id,p.config_version,
   p.provider_paused_at_receipt,p.provider_occurred_at,p.received_at
 FROM claimed p ORDER BY p.next_attempt_at,p.id;
END $$;
REVOKE ALL ON FUNCTION claim_hosted_prebind_recoveries(timestamptz,timestamptz,integer) FROM PUBLIC;

INSERT INTO admin_permissions(permission_key,description) VALUES
 ('provider_config:read','Read secret-free hosted provider configuration versions'),
 ('provider_config:request','Request hosted provider configuration provisioning or rotation'),
 ('provider_config:approve','Independently approve hosted provider configuration provisioning or rotation');
INSERT INTO admin_role_permissions(role_key,permission_key) VALUES
 ('auditor','provider_config:read'),('security_admin','provider_config:read'),('security_admin','provider_config:request'),
 ('senior_approver','provider_config:read'),('senior_approver','provider_config:approve');

REVOKE ALL ON hosted_provider_config_manifests,hosted_provider_config_workflows,hosted_provider_config_heads,hosted_provider_config_idempotency,hosted_provider_config_probe_incidents FROM PUBLIC;
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='platform_admin_runtime') THEN
   GRANT SELECT,INSERT ON hosted_provider_config_idempotency TO platform_admin_runtime;
   GRANT EXECUTE ON FUNCTION provider_config_public_rows(uuid,uuid,integer,uuid) TO platform_admin_runtime;
   GRANT EXECUTE ON FUNCTION request_hosted_provider_config(uuid,uuid,uuid,text,bigint,jsonb,text,uuid,text,timestamptz,timestamptz) TO platform_admin_runtime;
   GRANT EXECUTE ON FUNCTION decide_hosted_provider_config(uuid,uuid,bigint,boolean,text,uuid,text,timestamptz,timestamptz) TO platform_admin_runtime;
 END IF;
 IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='merchant_provider_health_worker') THEN
   GRANT EXECUTE ON FUNCTION claim_hosted_provider_config_probes(text,integer,timestamptz) TO merchant_provider_health_worker;
   GRANT EXECUTE ON FUNCTION complete_hosted_provider_config_probe(uuid,text,bigint,boolean,text,bytea,bytea,timestamptz) TO merchant_provider_health_worker;
 END IF;
 IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='merchant_api_runtime') THEN
   REVOKE EXECUTE ON FUNCTION hosted_provider_callback_admitted(text) FROM merchant_api_runtime;
   GRANT EXECUTE ON FUNCTION hosted_provider_callback_config_admitted(text,text),hosted_provider_outbound_config_admitted(uuid,uuid,text,text) TO merchant_api_runtime;
 END IF;
 IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='merchant_plan_worker') THEN
   GRANT EXECUTE ON FUNCTION hosted_provider_outbound_config_admitted(uuid,uuid,text,text) TO merchant_plan_worker;
 END IF;
 IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='merchant_plan_worker') THEN
   GRANT EXECUTE ON FUNCTION claim_hosted_prebind_recoveries(timestamptz,timestamptz,integer) TO merchant_plan_worker;
 END IF;
END $$;

COMMIT;
