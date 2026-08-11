BEGIN;

CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TYPE platform_config_kind AS ENUM (
    'tenant','merchant_environment','chain','asset_contract','wallet_pool',
    'rpc_provider','rate_source','rate_policy','finality_policy','matching_policy',
    'quota','notification_channel','feature_flag','maintenance_window'
);

CREATE TYPE platform_change_status AS ENUM (
    'draft','approval_requested','approved','rejected','scheduled','active','superseded','cancelled'
);

CREATE FUNCTION platform_scope_uuid(requested_tenant uuid) RETURNS uuid
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT COALESCE(requested_tenant, '00000000-0000-0000-0000-000000000000'::uuid)
$$;

-- Financial amounts are JSON strings. This recursively rejects numeric values
-- under money-like keys, avoiding JSON/IEEE-754 rounding at the control plane.
CREATE FUNCTION platform_exact_money_strings(value jsonb) RETURNS boolean
LANGUAGE plpgsql IMMUTABLE PARALLEL SAFE AS $$
DECLARE k text; v jsonb;
BEGIN
    IF jsonb_typeof(value) = 'object' THEN
        FOR k,v IN SELECT key,value FROM jsonb_each(value) LOOP
            IF k ~* '(amount|balance|minimum|maximum|threshold|dust|fee|limit)'
               AND jsonb_typeof(v) NOT IN ('string','null') THEN RETURN false; END IF;
            IF jsonb_typeof(v) = 'string' AND k ~* '(amount|balance|minimum|maximum|threshold|dust|fee|limit)'
               AND trim(both '"' from v::text) !~ '^(0|[1-9][0-9]{0,77})$' THEN RETURN false; END IF;
            IF NOT platform_exact_money_strings(v) THEN RETURN false; END IF;
        END LOOP;
    ELSIF jsonb_typeof(value) = 'array' THEN
        FOR v IN SELECT value FROM jsonb_array_elements(value) LOOP
            IF NOT platform_exact_money_strings(v) THEN RETURN false; END IF;
        END LOOP;
    END IF;
    RETURN true;
END $$;

CREATE FUNCTION platform_payload_has_no_secrets(value jsonb) RETURNS boolean
LANGUAGE plpgsql IMMUTABLE PARALLEL SAFE AS $$
DECLARE k text; v jsonb; s text;
BEGIN
    IF jsonb_typeof(value) = 'object' THEN
        FOR k,v IN SELECT key,value FROM jsonb_each(value) LOOP
            IF k ~* '(^|_)(private_?key|mnemonic|seed|password|secret|api_?key|access_?token|credential|signing_?key)($|_)'
               AND k !~* '(_ref|_reference)$' THEN RETURN false; END IF;
            IF NOT platform_payload_has_no_secrets(v) THEN RETURN false; END IF;
        END LOOP;
    ELSIF jsonb_typeof(value) = 'array' THEN
        FOR v IN SELECT value FROM jsonb_array_elements(value) LOOP
            IF NOT platform_payload_has_no_secrets(v) THEN RETURN false; END IF;
        END LOOP;
    ELSIF jsonb_typeof(value) = 'string' THEN
        s := trim(both '"' from value::text);
        IF s ~* '(BEGIN (RSA |EC |OPENSSH )?PRIVATE KEY|(^|[[:space:]])(xprv|[KL][1-9A-HJ-NP-Za-km-z]{50,})($|[[:space:]]))' THEN RETURN false; END IF;
    END IF;
    RETURN true;
END $$;

CREATE TABLE platform_admin_assertion_nonces (
    audience text NOT NULL CHECK (length(audience) BETWEEN 3 AND 128),
    nonce uuid NOT NULL,
    consumed_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    expires_at timestamptz NOT NULL,
    PRIMARY KEY (audience,nonce),
    CHECK (expires_at > consumed_at)
);
CREATE INDEX platform_admin_assertion_nonces_expiry_idx ON platform_admin_assertion_nonces(expires_at);

CREATE TABLE platform_admin_service_identities (
    id uuid PRIMARY KEY,
    name text NOT NULL UNIQUE CHECK(length(name) BETWEEN 3 AND 128),
    purpose text NOT NULL CHECK(purpose IN ('scheduled_activation','outbox_publisher')),
    enabled boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL
);
ALTER TABLE platform_admin_service_identities ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform_admin_service_identities FORCE ROW LEVEL SECURITY;
CREATE POLICY platform_admin_service_identity_global ON platform_admin_service_identities
USING (current_setting('app.platform_admin_global',true)='true')
WITH CHECK (current_setting('app.platform_admin_global',true)='true');

CREATE FUNCTION consume_platform_admin_assertion(requested_audience text, requested_nonce uuid, requested_expiry timestamptz)
RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,public SET row_security=off AS $$
BEGIN
    IF requested_expiry <= clock_timestamp() OR requested_expiry > clock_timestamp()+interval '2 minutes' THEN RETURN false; END IF;
    INSERT INTO public.platform_admin_assertion_nonces(audience,nonce,expires_at)
    VALUES(requested_audience,requested_nonce,requested_expiry)
    ON CONFLICT DO NOTHING;
    RETURN FOUND;
END $$;
REVOKE ALL ON FUNCTION consume_platform_admin_assertion(text,uuid,timestamptz) FROM PUBLIC;

CREATE TABLE platform_config_change_requests (
    id uuid PRIMARY KEY,
    tenant_id uuid,
    scope_id uuid NOT NULL,
    kind platform_config_kind NOT NULL,
    logical_key text NOT NULL CHECK (logical_key ~ '^[a-z0-9][a-z0-9._:/-]{0,126}[a-z0-9]$'),
    version bigint NOT NULL CHECK (version > 0),
    based_on_version bigint CHECK (based_on_version IS NULL OR based_on_version >= 0),
    rollback_of_snapshot_id uuid,
    payload jsonb NOT NULL CHECK (jsonb_typeof(payload)='object' AND pg_column_size(payload)<=65536),
    payload_hash bytea NOT NULL CHECK (octet_length(payload_hash)=32),
    status platform_change_status NOT NULL DEFAULT 'draft',
    reason text NOT NULL CHECK (length(reason) BETWEEN 3 AND 1000),
    requested_by uuid NOT NULL,
    approved_by uuid,
    rejected_by uuid,
    scheduled_by uuid,
    activated_by uuid,
    requested_at timestamptz,
    decided_at timestamptz,
    scheduled_for timestamptz,
    activated_at timestamptz,
    activation_lease_owner text,
    activation_lease_until timestamptz,
    activation_attempts integer NOT NULL DEFAULT 0 CHECK(activation_attempts>=0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    row_version bigint NOT NULL DEFAULT 1 CHECK (row_version > 0),
    CHECK (scope_id=platform_scope_uuid(tenant_id)),
    CHECK (platform_payload_has_no_secrets(payload)),
    CHECK (platform_exact_money_strings(payload)),
    CHECK (payload_hash=digest(payload::text,'sha256')),
    CHECK (approved_by IS NULL OR approved_by<>requested_by),
    CHECK (rejected_by IS NULL OR rejected_by<>requested_by),
    CHECK ((scheduled_for IS NULL)=(status NOT IN ('scheduled','active','superseded'))),
    CHECK ((activated_at IS NULL)=(status NOT IN ('active','superseded'))),
    CHECK ((activation_lease_owner IS NULL)=(activation_lease_until IS NULL)),
    UNIQUE(id,scope_id),
    UNIQUE(id,scope_id,kind,logical_key),
    UNIQUE(scope_id,kind,logical_key,version),
    FOREIGN KEY(tenant_id) REFERENCES tenants(id)
);
CREATE INDEX platform_changes_list_idx ON platform_config_change_requests(scope_id,status,kind,logical_key,created_at DESC);
CREATE INDEX platform_changes_due_idx ON platform_config_change_requests(scheduled_for,id) WHERE status='scheduled';

CREATE TABLE platform_config_snapshots (
    id uuid PRIMARY KEY,
    scope_id uuid NOT NULL,
    tenant_id uuid,
    change_request_id uuid NOT NULL,
    kind platform_config_kind NOT NULL,
    logical_key text NOT NULL,
    version bigint NOT NULL CHECK(version>0),
    payload jsonb NOT NULL CHECK(jsonb_typeof(payload)='object' AND pg_column_size(payload)<=65536),
    payload_hash bytea NOT NULL CHECK(octet_length(payload_hash)=32),
    rollback_of_snapshot_id uuid,
    activated_by uuid NOT NULL,
    activated_at timestamptz NOT NULL,
    CHECK(scope_id=platform_scope_uuid(tenant_id)),
    CHECK(platform_payload_has_no_secrets(payload)),
    CHECK(platform_exact_money_strings(payload)),
    CHECK(payload_hash=digest(payload::text,'sha256')),
    UNIQUE(id,scope_id),
    UNIQUE(id,scope_id,kind,logical_key),
    UNIQUE(scope_id,kind,logical_key,version),
    FOREIGN KEY(change_request_id,scope_id,kind,logical_key) REFERENCES platform_config_change_requests(id,scope_id,kind,logical_key),
    FOREIGN KEY(rollback_of_snapshot_id,scope_id,kind,logical_key) REFERENCES platform_config_snapshots(id,scope_id,kind,logical_key)
);
ALTER TABLE platform_config_change_requests
ADD CONSTRAINT platform_change_rollback_same_resource_fk
FOREIGN KEY(rollback_of_snapshot_id,scope_id,kind,logical_key)
REFERENCES platform_config_snapshots(id,scope_id,kind,logical_key);

CREATE TABLE platform_config_heads (
    scope_id uuid NOT NULL,
    tenant_id uuid,
    kind platform_config_kind NOT NULL,
    logical_key text NOT NULL,
    snapshot_id uuid NOT NULL,
    fence_token bigint NOT NULL CHECK(fence_token>0),
    updated_at timestamptz NOT NULL,
    CHECK(scope_id=platform_scope_uuid(tenant_id)),
    PRIMARY KEY(scope_id,kind,logical_key),
    FOREIGN KEY(snapshot_id,scope_id,kind,logical_key) REFERENCES platform_config_snapshots(id,scope_id,kind,logical_key)
);

CREATE TABLE platform_config_activations (
    id uuid PRIMARY KEY,
    scope_id uuid NOT NULL,
    tenant_id uuid,
    kind platform_config_kind NOT NULL,
    logical_key text NOT NULL,
    snapshot_id uuid NOT NULL,
    previous_snapshot_id uuid,
    fence_token bigint NOT NULL CHECK(fence_token>0),
    activation_type text NOT NULL CHECK(activation_type IN ('activate','rollback')),
    actor_id uuid NOT NULL,
    occurred_at timestamptz NOT NULL,
    CHECK(scope_id=platform_scope_uuid(tenant_id)),
    UNIQUE(scope_id,kind,logical_key,fence_token),
    FOREIGN KEY(snapshot_id,scope_id,kind,logical_key) REFERENCES platform_config_snapshots(id,scope_id,kind,logical_key),
    FOREIGN KEY(previous_snapshot_id,scope_id,kind,logical_key) REFERENCES platform_config_snapshots(id,scope_id,kind,logical_key)
);

CREATE TABLE platform_emergency_pause_events (
    id uuid PRIMARY KEY,
    scope_id uuid NOT NULL,
    tenant_id uuid,
    kind platform_config_kind NOT NULL,
    logical_key text NOT NULL,
    action text NOT NULL CHECK(action IN ('pause','resume')),
    reason text NOT NULL CHECK(length(reason) BETWEEN 8 AND 1000),
    actor_id uuid NOT NULL,
    step_up_at timestamptz NOT NULL,
    occurred_at timestamptz NOT NULL,
    previous_event_id uuid,
    CHECK(scope_id=platform_scope_uuid(tenant_id)),
    UNIQUE(id,scope_id),
    UNIQUE(id,scope_id,kind,logical_key),
    FOREIGN KEY(previous_event_id,scope_id,kind,logical_key) REFERENCES platform_emergency_pause_events(id,scope_id,kind,logical_key)
);
CREATE INDEX platform_pause_state_idx ON platform_emergency_pause_events(scope_id,kind,logical_key,occurred_at DESC);

CREATE TABLE platform_admin_idempotency (
    scope_id uuid NOT NULL,
    tenant_id uuid,
    actor_id uuid NOT NULL,
    operation text NOT NULL,
    idempotency_key text NOT NULL CHECK(length(idempotency_key) BETWEEN 8 AND 255),
    request_hash bytea NOT NULL CHECK(octet_length(request_hash)=32),
    response_status integer NOT NULL,
    response_body jsonb NOT NULL CHECK(pg_column_size(response_body)<=65536),
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    CHECK(scope_id=platform_scope_uuid(tenant_id)),
    PRIMARY KEY(scope_id,actor_id,operation,idempotency_key)
);

CREATE TABLE platform_admin_audit (
    sequence_id bigserial PRIMARY KEY,
    id uuid NOT NULL UNIQUE,
    scope_id uuid NOT NULL,
    tenant_id uuid,
    actor_id uuid NOT NULL,
    session_id text NOT NULL CHECK(length(session_id) BETWEEN 1 AND 255),
    action text NOT NULL,
    resource_type text NOT NULL,
    resource_id text NOT NULL,
    reason text NOT NULL,
    details jsonb NOT NULL CHECK(jsonb_typeof(details)='object' AND pg_column_size(details)<=16384),
    occurred_at timestamptz NOT NULL,
    previous_hash bytea NOT NULL CHECK(octet_length(previous_hash)=32),
    entry_hash bytea NOT NULL CHECK(octet_length(entry_hash)=32),
    CHECK(scope_id=platform_scope_uuid(tenant_id)),
    CHECK(platform_payload_has_no_secrets(details))
);

CREATE TABLE platform_admin_outbox (
    id uuid PRIMARY KEY,
    scope_id uuid NOT NULL,
    tenant_id uuid,
    aggregate_type text NOT NULL,
    aggregate_id text NOT NULL,
    aggregate_version bigint NOT NULL CHECK(aggregate_version>0),
    event_type text NOT NULL,
    payload jsonb NOT NULL CHECK(jsonb_typeof(payload)='object' AND pg_column_size(payload)<=65536),
    occurred_at timestamptz NOT NULL,
    available_at timestamptz NOT NULL,
    published_at timestamptz,
    lease_owner text,
    lease_until timestamptz,
    claim_token bigint NOT NULL DEFAULT 0 CHECK(claim_token>=0),
    attempts integer NOT NULL DEFAULT 0 CHECK(attempts>=0),
    CHECK(scope_id=platform_scope_uuid(tenant_id)),
    CHECK(platform_payload_has_no_secrets(payload)),
    CHECK((lease_owner IS NULL)=(lease_until IS NULL))
);
CREATE INDEX platform_admin_outbox_pending_idx ON platform_admin_outbox(available_at,id) WHERE published_at IS NULL;

INSERT INTO admin_permissions(permission_key,description) VALUES
('platform_config:read','Read versioned platform configuration'),
('platform_config:write','Draft versioned platform configuration'),
('platform_config:request','Request approval for platform configuration'),
('platform_config:approve','Second-person approval of platform configuration'),
('platform_config:schedule','Schedule approved platform configuration'),
('platform_config:activate','Manually activate due platform configuration'),
('platform_config:rollback','Draft rollback as a new version'),
('platform_config:emergency','Emergency pause or resume platform resources');

INSERT INTO admin_role_permissions(role_key,permission_key) VALUES
('auditor','platform_config:read'),
('treasury_operator','platform_config:read'),
('security_admin','platform_config:read'),('security_admin','platform_config:write'),
('security_admin','platform_config:request'),('security_admin','platform_config:emergency'),
('senior_approver','platform_config:read'),('senior_approver','platform_config:approve'),
('senior_approver','platform_config:schedule'),('senior_approver','platform_config:activate'),
('senior_approver','platform_config:rollback');

CREATE FUNCTION platform_immutable_row() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'platform configuration history is immutable' USING ERRCODE='55000'; END $$;
CREATE TRIGGER platform_snapshots_immutable BEFORE UPDATE OR DELETE ON platform_config_snapshots FOR EACH ROW EXECUTE FUNCTION platform_immutable_row();
CREATE TRIGGER platform_activations_immutable BEFORE UPDATE OR DELETE ON platform_config_activations FOR EACH ROW EXECUTE FUNCTION platform_immutable_row();
CREATE TRIGGER platform_audit_immutable BEFORE UPDATE OR DELETE ON platform_admin_audit FOR EACH ROW EXECUTE FUNCTION platform_immutable_row();
CREATE TRIGGER platform_pause_events_immutable BEFORE UPDATE OR DELETE ON platform_emergency_pause_events FOR EACH ROW EXECUTE FUNCTION platform_immutable_row();

CREATE FUNCTION append_platform_admin_audit(
    requested_id uuid, requested_tenant uuid, requested_actor uuid, requested_session text,
    requested_action text, requested_resource_type text, requested_resource_id text,
    requested_reason text, requested_details jsonb, requested_at timestamptz
) RETURNS bytea LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,public SET row_security=on AS $$
DECLARE prior bytea; computed_hash bytea; requested_scope uuid; prior_global text; prior_tenants text;
BEGIN
    requested_scope := public.platform_scope_uuid(requested_tenant);
    prior_global := current_setting('app.platform_admin_global',true);
    prior_tenants := current_setting('app.platform_admin_tenants',true);
    PERFORM set_config('app.platform_admin_global','true',true);
    PERFORM set_config('app.platform_admin_tenants','',true);
    PERFORM pg_advisory_xact_lock(746193205);
    SELECT entry_hash INTO prior FROM public.platform_admin_audit ORDER BY sequence_id DESC LIMIT 1;
    prior := COALESCE(prior,digest('platform-admin-audit-v1','sha256'));
    computed_hash := public.digest(prior || convert_to(concat_ws(E'\n',requested_id::text,requested_scope::text,
        requested_actor::text,requested_session,requested_action,requested_resource_type,
        requested_resource_id,requested_reason,requested_details::text,requested_at::text),'UTF8'),'sha256');
    INSERT INTO public.platform_admin_audit(id,scope_id,tenant_id,actor_id,session_id,action,resource_type,resource_id,reason,details,occurred_at,previous_hash,entry_hash)
    VALUES(requested_id,requested_scope,requested_tenant,requested_actor,requested_session,requested_action,requested_resource_type,requested_resource_id,requested_reason,requested_details,requested_at,prior,computed_hash);
    PERFORM set_config('app.platform_admin_global',COALESCE(prior_global,''),true);
    PERFORM set_config('app.platform_admin_tenants',COALESCE(prior_tenants,''),true);
    RETURN computed_hash;
END $$;
REVOKE ALL ON FUNCTION append_platform_admin_audit(uuid,uuid,uuid,text,text,text,text,text,jsonb,timestamptz) FROM PUBLIC;
REVOKE INSERT,UPDATE,DELETE,TRUNCATE ON platform_admin_audit FROM PUBLIC;
REVOKE UPDATE,DELETE,TRUNCATE ON platform_config_snapshots,platform_config_activations,platform_emergency_pause_events FROM PUBLIC;
DO $$ BEGIN
  IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='platform_admin_runtime') THEN
    EXECUTE 'REVOKE INSERT,UPDATE,DELETE,TRUNCATE ON platform_admin_audit FROM platform_admin_runtime';
    EXECUTE 'GRANT EXECUTE ON FUNCTION append_platform_admin_audit(uuid,uuid,uuid,text,text,text,text,text,jsonb,timestamptz) TO platform_admin_runtime';
  END IF;
END $$;

-- Tenant/global RLS is transaction-local and must be set by the repository.
-- Global visibility is only granted to the separately permission-checked service path.
DO $$ DECLARE table_name text; BEGIN
  FOREACH table_name IN ARRAY ARRAY['platform_config_change_requests','platform_config_snapshots','platform_config_heads','platform_config_activations','platform_emergency_pause_events','platform_admin_idempotency','platform_admin_audit','platform_admin_outbox'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY',table_name);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY',table_name);
    EXECUTE format('CREATE POLICY %I_scope ON %I USING ((current_setting(''app.platform_admin_global'',true)=''true'') OR tenant_id::text = ANY(string_to_array(current_setting(''app.platform_admin_tenants'',true),'',''))) WITH CHECK ((current_setting(''app.platform_admin_global'',true)=''true'') OR tenant_id::text = ANY(string_to_array(current_setting(''app.platform_admin_tenants'',true),'','')))',table_name,table_name);
  END LOOP;
END $$;

COMMIT;
