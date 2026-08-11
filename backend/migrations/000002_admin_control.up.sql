BEGIN;

CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE admin_users (
    id uuid PRIMARY KEY,
    oidc_issuer text NOT NULL CHECK (oidc_issuer LIKE 'https://%' AND right(oidc_issuer, 1) <> '/'),
    oidc_subject text NOT NULL CHECK (length(oidc_subject) BETWEEN 1 AND 255),
    display_name text NOT NULL CHECK (length(display_name) BETWEEN 1 AND 255),
    email text,
    status text NOT NULL CHECK (status IN ('active', 'disabled', 'locked')),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    UNIQUE (oidc_issuer, oidc_subject)
);

CREATE TABLE admin_roles (
    role_key text PRIMARY KEY CHECK (role_key IN (
        'support_read_only','payment_operator','senior_approver',
        'treasury_operator','security_admin','auditor'
    )),
    description text NOT NULL
);

CREATE TABLE admin_permissions (
    permission_key text PRIMARY KEY CHECK (permission_key ~ '^[a-z][a-z_]*:[a-z][a-z_]*$'),
    description text NOT NULL
);

CREATE TABLE admin_role_permissions (
    role_key text NOT NULL REFERENCES admin_roles(role_key) ON DELETE RESTRICT,
    permission_key text NOT NULL REFERENCES admin_permissions(permission_key) ON DELETE RESTRICT,
    PRIMARY KEY (role_key, permission_key)
);

CREATE TABLE admin_role_bindings (
    id uuid PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES admin_users(id) ON DELETE RESTRICT,
    role_key text NOT NULL REFERENCES admin_roles(role_key) ON DELETE RESTRICT,
    tenant_id uuid REFERENCES tenants(id) ON DELETE RESTRICT,
    merchant_id uuid,
    granted_by uuid REFERENCES admin_users(id) ON DELETE RESTRICT,
    granted_at timestamptz NOT NULL,
    expires_at timestamptz,
    revoked_at timestamptz,
    reason text NOT NULL CHECK (length(reason) BETWEEN 1 AND 1000),
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    FOREIGN KEY (merchant_id, tenant_id) REFERENCES merchants(id, tenant_id),
    CHECK (merchant_id IS NULL OR tenant_id IS NOT NULL),
    CHECK (expires_at IS NULL OR expires_at > granted_at),
    UNIQUE NULLS NOT DISTINCT (user_id, role_key, tenant_id, merchant_id)
);
CREATE INDEX admin_role_bindings_user_idx ON admin_role_bindings (user_id, expires_at) WHERE revoked_at IS NULL;

CREATE TABLE admin_login_attempts (
    state_hash bytea PRIMARY KEY CHECK (octet_length(state_hash) = 32),
    nonce text NOT NULL CHECK (length(nonce) BETWEEN 43 AND 128),
    encrypted_verifier bytea NOT NULL CHECK (octet_length(encrypted_verifier) BETWEEN 72 AND 512),
    purpose text NOT NULL CHECK (purpose IN ('login', 'step_up')),
    expected_user_id uuid REFERENCES admin_users(id) ON DELETE CASCADE,
    existing_session_hash bytea,
    return_path text NOT NULL CHECK (left(return_path, 1) = '/' AND left(return_path, 2) <> '//' AND position(chr(10) in return_path)=0 AND position(chr(13) in return_path)=0 AND position(chr(92) in return_path)=0),
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    CHECK (expires_at > created_at AND expires_at <= created_at + interval '15 minutes'),
    CHECK ((purpose = 'login' AND expected_user_id IS NULL AND existing_session_hash IS NULL) OR
           (purpose = 'step_up' AND expected_user_id IS NOT NULL AND octet_length(existing_session_hash) = 32))
);
CREATE INDEX admin_login_attempts_expiry_idx ON admin_login_attempts (expires_at);

CREATE TABLE admin_sessions (
    id uuid PRIMARY KEY,
    session_hash bytea NOT NULL UNIQUE CHECK (octet_length(session_hash) = 32),
    csrf_hash bytea NOT NULL CHECK (octet_length(csrf_hash) = 32),
    user_id uuid NOT NULL REFERENCES admin_users(id) ON DELETE CASCADE,
    oidc_issuer text NOT NULL,
    oidc_subject text NOT NULL,
    acr text NOT NULL,
    amr text[] NOT NULL CHECK (cardinality(amr) > 0),
    created_at timestamptz NOT NULL,
    last_seen_at timestamptz NOT NULL,
    idle_expires_at timestamptz NOT NULL,
    absolute_expires_at timestamptz NOT NULL,
    step_up_until timestamptz,
    rotated_at timestamptz NOT NULL,
    revoked_at timestamptz,
    revocation_reason text,
    replaced_by_hash bytea,
    source_address inet,
    user_agent_hash bytea CHECK (user_agent_hash IS NULL OR octet_length(user_agent_hash) = 32),
    CHECK (last_seen_at >= created_at AND rotated_at >= created_at),
    CHECK (idle_expires_at > last_seen_at AND idle_expires_at <= absolute_expires_at),
    CHECK (absolute_expires_at > created_at AND absolute_expires_at <= created_at + interval '12 hours'),
    CHECK (step_up_until IS NULL OR step_up_until <= absolute_expires_at),
    CHECK ((revoked_at IS NULL AND revocation_reason IS NULL) OR
           (revoked_at IS NOT NULL AND length(revocation_reason) BETWEEN 1 AND 255)),
    CHECK (replaced_by_hash IS NULL OR octet_length(replaced_by_hash) = 32)
);
CREATE INDEX admin_sessions_active_user_idx ON admin_sessions (user_id, absolute_expires_at) WHERE revoked_at IS NULL;
CREATE INDEX admin_sessions_expiry_idx ON admin_sessions (idle_expires_at, absolute_expires_at) WHERE revoked_at IS NULL;

-- Manual resolutions can be initiated by either a merchant API client or a
-- human admin identity. A security-definer constraint trigger preserves the
-- tenant-scoped polymorphic actor invariant without inventing browser-visible
-- merchant credentials.
ALTER TABLE manual_resolutions DROP CONSTRAINT manual_resolutions_requested_by_tenant_id_fkey;
ALTER TABLE manual_resolutions DROP CONSTRAINT manual_resolutions_approved_by_tenant_id_fkey;
DO $$ DECLARE item record; BEGIN
  FOR item IN SELECT conname FROM pg_constraint
    WHERE conrelid='manual_resolutions'::regclass AND contype='c'
      AND (pg_get_constraintdef(oid) LIKE '%approved_by%requested_by%' OR pg_get_constraintdef(oid) LIKE '%accept_shortfall%accept_cross_asset%')
  LOOP EXECUTE format('ALTER TABLE manual_resolutions DROP CONSTRAINT %I',item.conname); END LOOP;
END $$;
ALTER TABLE manual_resolutions ADD CONSTRAINT manual_resolution_distinct_actors CHECK (approved_by IS NULL OR approved_by <> requested_by);
ALTER TABLE manual_resolutions ADD CONSTRAINT manual_resolution_approval_state CHECK (
    status IN ('approval_required','invalid','conflict') OR NOT (accept_shortfall OR accept_cross_asset) OR approved_by IS NOT NULL
);

CREATE FUNCTION validate_manual_resolution_actor() RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
DECLARE actor_allowed boolean; prior_admin_user text := current_setting('app.admin_user_id',true);
BEGIN
  actor_allowed := EXISTS (SELECT 1 FROM public.api_clients WHERE id=NEW.requested_by AND tenant_id=NEW.tenant_id);
  IF NOT actor_allowed THEN
    PERFORM set_config('app.admin_user_id',NEW.requested_by::text,true);
    actor_allowed := EXISTS (SELECT 1 FROM public.admin_role_bindings b JOIN public.admin_users u ON u.id=b.user_id
       JOIN public.admin_role_permissions rp ON rp.role_key=b.role_key AND rp.permission_key='resolution:request'
       WHERE u.id=NEW.requested_by AND u.status='active' AND b.revoked_at IS NULL
         AND (b.expires_at IS NULL OR b.expires_at>clock_timestamp())
         AND (b.tenant_id IS NULL OR b.tenant_id=NEW.tenant_id));
  END IF;
  IF NOT actor_allowed THEN RAISE EXCEPTION 'manual resolution requester is not authorized for tenant'; END IF;
  IF NEW.approved_by IS NOT NULL THEN
    actor_allowed := EXISTS (SELECT 1 FROM public.api_clients WHERE id=NEW.approved_by AND tenant_id=NEW.tenant_id);
    IF NOT actor_allowed THEN
      PERFORM set_config('app.admin_user_id',NEW.approved_by::text,true);
      actor_allowed := EXISTS (SELECT 1 FROM public.admin_role_bindings b JOIN public.admin_users u ON u.id=b.user_id
       JOIN public.admin_role_permissions rp ON rp.role_key=b.role_key AND rp.permission_key='resolution:approve'
       WHERE u.id=NEW.approved_by AND u.status='active' AND b.revoked_at IS NULL
         AND (b.expires_at IS NULL OR b.expires_at>clock_timestamp())
         AND (b.tenant_id IS NULL OR b.tenant_id=NEW.tenant_id));
    END IF;
    IF NOT actor_allowed THEN RAISE EXCEPTION 'manual resolution approver is not authorized for tenant'; END IF;
  END IF;
  PERFORM set_config('app.admin_user_id',coalesce(prior_admin_user,''),true);
  RETURN NEW;
END $$;
CREATE CONSTRAINT TRIGGER manual_resolution_actor_guard
AFTER INSERT OR UPDATE OF requested_by,approved_by ON manual_resolutions
DEFERRABLE INITIALLY IMMEDIATE FOR EACH ROW EXECUTE FUNCTION validate_manual_resolution_actor();

CREATE TABLE admin_action_requests (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE RESTRICT,
    merchant_id uuid,
    kind text NOT NULL CHECK (kind = 'manual_resolution'),
	core_resolution_id uuid NOT NULL UNIQUE REFERENCES manual_resolutions(id) ON DELETE RESTRICT,
    resource_type text NOT NULL CHECK (length(resource_type) BETWEEN 1 AND 80),
    resource_id text NOT NULL CHECK (length(resource_id) BETWEEN 1 AND 255),
    object_version bigint NOT NULL CHECK (object_version > 0),
    requested_by uuid NOT NULL REFERENCES admin_users(id) ON DELETE RESTRICT,
    approved_by uuid REFERENCES admin_users(id) ON DELETE RESTRICT,
    rejected_by uuid REFERENCES admin_users(id) ON DELETE RESTRICT,
    request_reason text NOT NULL CHECK (length(request_reason) BETWEEN 1 AND 1000),
    decision_reason text,
    payload jsonb NOT NULL CHECK (jsonb_typeof(payload) = 'object' AND pg_column_size(payload) <= 16384),
    payload_hash bytea NOT NULL CHECK (octet_length(payload_hash) = 32),
    status text NOT NULL CHECK (status IN ('pending_approval','approved','rejected','expired','executing','completed','failed','cancelled')),
    requires_step_up boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    decided_at timestamptz,
    executed_at timestamptz,
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    FOREIGN KEY (merchant_id, tenant_id) REFERENCES merchants(id, tenant_id),
    CHECK (merchant_id IS NULL OR tenant_id IS NOT NULL),
    CHECK (expires_at > created_at AND expires_at <= created_at + interval '24 hours'),
    CHECK (approved_by IS NULL OR approved_by <> requested_by),
    CHECK (rejected_by IS NULL OR rejected_by <> requested_by),
    CHECK (NOT (approved_by IS NOT NULL AND rejected_by IS NOT NULL)),
    CHECK ((status = 'pending_approval' AND approved_by IS NULL AND rejected_by IS NULL AND decided_at IS NULL AND decision_reason IS NULL) OR
           (status IN ('approved','executing','completed','failed') AND approved_by IS NOT NULL AND rejected_by IS NULL AND decided_at IS NOT NULL AND length(decision_reason) BETWEEN 1 AND 1000) OR
           (status = 'rejected' AND rejected_by IS NOT NULL AND approved_by IS NULL AND decided_at IS NOT NULL AND length(decision_reason) BETWEEN 1 AND 1000) OR
           (status IN ('expired','cancelled') AND decided_at IS NOT NULL AND length(decision_reason) BETWEEN 1 AND 1000))
);
CREATE INDEX admin_action_requests_queue_idx ON admin_action_requests (tenant_id, status, expires_at, created_at);

CREATE TABLE admin_operator_idempotency (
    tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    actor_user_id uuid NOT NULL REFERENCES admin_users(id) ON DELETE CASCADE,
    operation text NOT NULL CHECK (length(operation) BETWEEN 1 AND 80),
    idempotency_key text NOT NULL CHECK (length(idempotency_key) BETWEEN 8 AND 255),
    request_hash bytea NOT NULL CHECK (octet_length(request_hash) = 32),
    response_status integer NOT NULL CHECK (response_status BETWEEN 200 AND 299),
    response_body jsonb NOT NULL CHECK (pg_column_size(response_body) <= 65536),
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, actor_user_id, operation, idempotency_key),
    CHECK (expires_at > created_at)
);
CREATE INDEX admin_operator_idempotency_expiry_idx ON admin_operator_idempotency (expires_at);

CREATE TABLE admin_audit_log (
    chain_position bigint GENERATED BY DEFAULT AS IDENTITY UNIQUE,
    event_id uuid PRIMARY KEY,
    tenant_id uuid REFERENCES tenants(id) ON DELETE RESTRICT,
    merchant_id uuid,
    actor_user_id uuid REFERENCES admin_users(id) ON DELETE RESTRICT,
    session_id uuid,
    action text NOT NULL CHECK (length(action) BETWEEN 1 AND 120),
    resource_type text NOT NULL CHECK (length(resource_type) BETWEEN 1 AND 80),
    resource_id text NOT NULL CHECK (length(resource_id) BETWEEN 1 AND 255),
    request_id text NOT NULL CHECK (length(request_id) BETWEEN 1 AND 255),
    reason text NOT NULL CHECK (length(reason) BETWEEN 1 AND 1000),
    before_digest bytea CHECK (before_digest IS NULL OR octet_length(before_digest) = 32),
    after_digest bytea CHECK (after_digest IS NULL OR octet_length(after_digest) = 32),
    details jsonb NOT NULL CHECK (jsonb_typeof(details) = 'object' AND pg_column_size(details) <= 16384),
    source_address inet,
    user_agent_hash bytea CHECK (user_agent_hash IS NULL OR octet_length(user_agent_hash) = 32),
    occurred_at timestamptz NOT NULL,
    recorded_at timestamptz NOT NULL,
    previous_hash bytea CHECK (previous_hash IS NULL OR octet_length(previous_hash) = 32),
    entry_hash bytea NOT NULL UNIQUE CHECK (octet_length(entry_hash) = 32),
    FOREIGN KEY (merchant_id, tenant_id) REFERENCES merchants(id, tenant_id),
    CHECK (merchant_id IS NULL OR tenant_id IS NOT NULL),
    CHECK (recorded_at >= occurred_at - interval '5 minutes')
);
CREATE INDEX admin_audit_scope_idx ON admin_audit_log (tenant_id, merchant_id, occurred_at DESC, event_id DESC);

CREATE FUNCTION prevent_admin_audit_mutation() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'admin audit log is append-only';
END $$;
CREATE TRIGGER admin_audit_append_only
BEFORE UPDATE OR DELETE ON admin_audit_log
FOR EACH ROW EXECUTE FUNCTION prevent_admin_audit_mutation();

CREATE FUNCTION append_admin_audit(
    p_event_id uuid, p_tenant_id uuid, p_merchant_id uuid, p_actor_user_id uuid,
    p_session_id uuid, p_action text, p_resource_type text, p_resource_id text,
    p_request_id text, p_reason text, p_before_digest bytea, p_after_digest bytea,
    p_details jsonb, p_source_address inet, p_user_agent_hash bytea, p_occurred_at timestamptz
) RETURNS bytea
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
DECLARE
    prior bytea;
    calculated bytea;
    recorded timestamptz := clock_timestamp();
    prior_admin_tenant text := current_setting('app.admin_tenant_id',true);
    prior_admin_merchants text := current_setting('app.admin_merchant_ids',true);
    prior_admin_tenant_wide text := current_setting('app.admin_allow_tenant_wide',true);
    prior_admin_audit_global text := current_setting('app.admin_audit_global',true);
BEGIN
	PERFORM set_config('app.admin_tenant_id',coalesce(p_tenant_id::text,''),true);
	PERFORM set_config('app.admin_merchant_ids',CASE WHEN p_merchant_id IS NULL THEN '{}' ELSE '{'||p_merchant_id::text||'}' END,true);
	PERFORM set_config('app.admin_allow_tenant_wide','true',true);
	PERFORM set_config('app.admin_audit_global','true',true);
    PERFORM pg_advisory_xact_lock(hashtext('merchant-admin-audit-chain-v1'));
    SELECT entry_hash INTO prior FROM public.admin_audit_log ORDER BY chain_position DESC LIMIT 1;
    calculated := digest(convert_to(concat_ws(chr(31),
        coalesce(encode(prior, 'hex'), ''), p_event_id::text, coalesce(p_tenant_id::text, ''),
        coalesce(p_merchant_id::text, ''), coalesce(p_actor_user_id::text, ''),
        coalesce(p_session_id::text, ''), p_action, p_resource_type, p_resource_id,
        p_request_id, p_reason, coalesce(encode(p_before_digest, 'hex'), ''),
        coalesce(encode(p_after_digest, 'hex'), ''), p_details::text,
        coalesce(p_source_address::text, ''), coalesce(encode(p_user_agent_hash, 'hex'), ''),
        p_occurred_at::text, recorded::text), 'UTF8'), 'sha256');
    INSERT INTO public.admin_audit_log (
        event_id,tenant_id,merchant_id,actor_user_id,session_id,action,resource_type,
        resource_id,request_id,reason,before_digest,after_digest,details,source_address,
        user_agent_hash,occurred_at,recorded_at,previous_hash,entry_hash
    ) VALUES (
        p_event_id,p_tenant_id,p_merchant_id,p_actor_user_id,p_session_id,p_action,p_resource_type,
        p_resource_id,p_request_id,p_reason,p_before_digest,p_after_digest,p_details,p_source_address,
        p_user_agent_hash,p_occurred_at,recorded,prior,calculated
    );
	PERFORM set_config('app.admin_tenant_id',coalesce(prior_admin_tenant,''),true);
	PERFORM set_config('app.admin_merchant_ids',coalesce(prior_admin_merchants,''),true);
	PERFORM set_config('app.admin_allow_tenant_wide',coalesce(prior_admin_tenant_wide,''),true);
	PERFORM set_config('app.admin_audit_global',coalesce(prior_admin_audit_global,''),true);
    RETURN calculated;
END $$;
REVOKE ALL ON FUNCTION append_admin_audit(uuid,uuid,uuid,uuid,uuid,text,text,text,text,text,bytea,bytea,jsonb,inet,bytea,timestamptz) FROM PUBLIC;

CREATE FUNCTION enforce_admin_action_transition() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.tenant_id <> OLD.tenant_id OR NEW.merchant_id IS DISTINCT FROM OLD.merchant_id OR
       NEW.kind <> OLD.kind OR NEW.resource_type <> OLD.resource_type OR NEW.resource_id <> OLD.resource_id OR
       NEW.object_version <> OLD.object_version OR NEW.requested_by <> OLD.requested_by OR
       NEW.request_reason <> OLD.request_reason OR NEW.payload <> OLD.payload OR NEW.payload_hash <> OLD.payload_hash OR
       NEW.created_at <> OLD.created_at OR NEW.expires_at <> OLD.expires_at OR NEW.requires_step_up <> OLD.requires_step_up THEN
        RAISE EXCEPTION 'immutable admin action request fields cannot be changed';
    END IF;
    IF NEW.status <> OLD.status AND NOT (
       (OLD.status = 'pending_approval' AND NEW.status IN ('approved','rejected','expired','cancelled')) OR
       (OLD.status = 'approved' AND NEW.status IN ('executing','cancelled')) OR
       (OLD.status = 'executing' AND NEW.status IN ('completed','failed'))
    ) THEN
        RAISE EXCEPTION 'invalid admin action request state transition';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER admin_action_transition_guard
BEFORE UPDATE ON admin_action_requests
FOR EACH ROW EXECUTE FUNCTION enforce_admin_action_transition();

ALTER TABLE admin_role_bindings ENABLE ROW LEVEL SECURITY;
ALTER TABLE admin_role_bindings FORCE ROW LEVEL SECURITY;
ALTER TABLE admin_action_requests ENABLE ROW LEVEL SECURITY;
ALTER TABLE admin_action_requests FORCE ROW LEVEL SECURITY;
ALTER TABLE admin_operator_idempotency ENABLE ROW LEVEL SECURITY;
ALTER TABLE admin_operator_idempotency FORCE ROW LEVEL SECURITY;
ALTER TABLE admin_audit_log ENABLE ROW LEVEL SECURITY;
ALTER TABLE admin_audit_log FORCE ROW LEVEL SECURITY;

CREATE POLICY admin_role_bindings_self_policy ON admin_role_bindings
    USING (user_id = nullif(current_setting('app.admin_user_id', true), '')::uuid);
CREATE POLICY admin_action_scope_policy ON admin_action_requests
    USING (tenant_id = nullif(current_setting('app.admin_tenant_id', true), '')::uuid AND
           ((merchant_id IS NULL AND coalesce(nullif(current_setting('app.admin_allow_tenant_wide', true), '')::boolean,false)) OR merchant_id = ANY(coalesce(nullif(current_setting('app.admin_merchant_ids', true), ''), '{}')::uuid[])))
    WITH CHECK (tenant_id = nullif(current_setting('app.admin_tenant_id', true), '')::uuid AND
                ((merchant_id IS NULL AND coalesce(nullif(current_setting('app.admin_allow_tenant_wide', true), '')::boolean,false)) OR merchant_id = ANY(coalesce(nullif(current_setting('app.admin_merchant_ids', true), ''), '{}')::uuid[])));
CREATE POLICY admin_operator_idempotency_scope_policy ON admin_operator_idempotency
    USING (tenant_id = nullif(current_setting('app.admin_tenant_id', true), '')::uuid AND
           actor_user_id = nullif(current_setting('app.admin_user_id', true), '')::uuid)
    WITH CHECK (tenant_id = nullif(current_setting('app.admin_tenant_id', true), '')::uuid AND
                actor_user_id = nullif(current_setting('app.admin_user_id', true), '')::uuid);
CREATE POLICY admin_audit_scope_policy ON admin_audit_log
    USING (coalesce(nullif(current_setting('app.admin_audit_global', true), '')::boolean,false) OR
           (tenant_id = nullif(current_setting('app.admin_tenant_id', true), '')::uuid AND
           ((merchant_id IS NULL AND coalesce(nullif(current_setting('app.admin_allow_tenant_wide', true), '')::boolean,false)) OR merchant_id = ANY(coalesce(nullif(current_setting('app.admin_merchant_ids', true), ''), '{}')::uuid[]))));

INSERT INTO admin_roles(role_key, description) VALUES
('support_read_only','Read-only payment support'),('payment_operator','Payment operations'),
('senior_approver','Second-person payment approval'),('treasury_operator','Treasury operations'),
('security_admin','Identity and infrastructure security'),('auditor','Read-only compliance audit');

INSERT INTO admin_permissions(permission_key, description) VALUES
('dashboard:read','Read operational overview'),('payments:read','Read intents and transfers'),
('unmatched:read','Read unmatched queue'),('unmatched:claim','Claim or release unmatched work'),
('resolution:request','Request a manual resolution'),('resolution:approve','Approve or reject a manual resolution'),
('webhooks:read','Read webhook health'),('webhooks:replay','Replay a persisted delivery'),
('infrastructure:read','Read assets and provider health'),('infrastructure:edit','Request infrastructure changes'),
('reconciliation:read','Read reconciliation runs'),('audit:read','Read tamper-evident audit'),
('team:admin','Manage admin access bindings');

INSERT INTO admin_role_permissions(role_key, permission_key) VALUES
('support_read_only','dashboard:read'),('support_read_only','payments:read'),('support_read_only','unmatched:read'),('support_read_only','webhooks:read'),
('payment_operator','dashboard:read'),('payment_operator','payments:read'),('payment_operator','unmatched:read'),('payment_operator','unmatched:claim'),('payment_operator','resolution:request'),('payment_operator','webhooks:read'),('payment_operator','webhooks:replay'),('payment_operator','reconciliation:read'),
('senior_approver','dashboard:read'),('senior_approver','payments:read'),('senior_approver','unmatched:read'),('senior_approver','resolution:approve'),('senior_approver','webhooks:read'),('senior_approver','reconciliation:read'),('senior_approver','audit:read'),
('treasury_operator','dashboard:read'),('treasury_operator','payments:read'),('treasury_operator','infrastructure:read'),('treasury_operator','reconciliation:read'),('treasury_operator','audit:read'),
('security_admin','dashboard:read'),('security_admin','infrastructure:read'),('security_admin','infrastructure:edit'),('security_admin','audit:read'),('security_admin','team:admin'),
('auditor','dashboard:read'),('auditor','payments:read'),('auditor','unmatched:read'),('auditor','webhooks:read'),('auditor','infrastructure:read'),('auditor','reconciliation:read'),('auditor','audit:read');

REVOKE ALL ON admin_login_attempts, admin_sessions, admin_action_requests, admin_operator_idempotency, admin_audit_log FROM PUBLIC;

COMMIT;
