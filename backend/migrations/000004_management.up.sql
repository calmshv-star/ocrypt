BEGIN;

CREATE EXTENSION IF NOT EXISTS pgcrypto;

INSERT INTO admin_permissions(permission_key,description) VALUES
('payment_links:read','Read merchant payment links'),
('payment_links:write','Create and disable merchant payment links'),
('checkout:write','Issue checkout capabilities for merchant intents'),
('webhook_settings:read','Read webhook settings and deliveries'),
('webhook_settings:write','Create, update, and verify webhook endpoints'),
('webhook_settings:rotate','Rotate webhook signing secrets'),
('webhook_settings:disable','Disable webhook endpoints with approval'),
('api_clients:read','Read API client metadata'),
('api_clients:write','Create API clients'),
('api_clients:rotate','Rotate API client credentials'),
('api_clients:revoke','Revoke API clients with approval'),
('management_audit:read','Read the management audit chain')
ON CONFLICT(permission_key) DO NOTHING;
INSERT INTO admin_role_permissions(role_key,permission_key) VALUES
('payment_operator','payment_links:read'),('payment_operator','payment_links:write'),('payment_operator','checkout:write'),('payment_operator','webhook_settings:read'),
('senior_approver','payment_links:read'),('senior_approver','payment_links:write'),('senior_approver','checkout:write'),('senior_approver','webhook_settings:read'),('senior_approver','webhook_settings:write'),('senior_approver','webhook_settings:rotate'),('senior_approver','webhook_settings:disable'),('senior_approver','api_clients:read'),('senior_approver','api_clients:write'),('senior_approver','api_clients:rotate'),('senior_approver','api_clients:revoke'),('senior_approver','management_audit:read'),
('security_admin','webhook_settings:read'),('security_admin','webhook_settings:write'),('security_admin','webhook_settings:rotate'),('security_admin','webhook_settings:disable'),('security_admin','api_clients:read'),('security_admin','api_clients:write'),('security_admin','api_clients:rotate'),('security_admin','api_clients:revoke'),('security_admin','management_audit:read'),
('auditor','payment_links:read'),('auditor','webhook_settings:read'),('auditor','api_clients:read'),('auditor','management_audit:read')
ON CONFLICT DO NOTHING;

CREATE TABLE payment_links (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    merchant_id uuid NOT NULL,
    name text NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
    public_token_hash bytea NOT NULL UNIQUE CHECK (octet_length(public_token_hash) = 32),
    amount_minor uint256 NOT NULL CHECK (amount_minor > 0),
    currency char(3) NOT NULL,
    currency_scale smallint NOT NULL CHECK (currency_scale BETWEEN 0 AND 9),
    description text NOT NULL DEFAULT '' CHECK (length(description) <= 1000),
    -- Payment links are immediately funded with one deterministic route.
    -- Authenticated checkout sessions are the multi-route selection surface.
    allowed_routes jsonb NOT NULL CHECK (jsonb_typeof(allowed_routes) = 'array' AND jsonb_array_length(allowed_routes) = 1),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object' AND pg_column_size(metadata) <= 16384),
    allowed_origin text CHECK (allowed_origin IS NULL OR allowed_origin ~ '^https://[^/?#]+$'),
    success_url text NOT NULL CHECK (success_url LIKE 'https://%'),
    cancel_url text NOT NULL CHECK (cancel_url LIKE 'https://%'),
    max_uses bigint NOT NULL CHECK (max_uses BETWEEN 1 AND 1000000),
    use_count bigint NOT NULL DEFAULT 0 CHECK (use_count BETWEEN 0 AND max_uses),
    status text NOT NULL CHECK (status IN ('active','disabled','expired')),
    expires_at timestamptz,
    created_by uuid NOT NULL,
    disabled_by uuid,
    disabled_at timestamptz,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    FOREIGN KEY (merchant_id, tenant_id) REFERENCES merchants(id, tenant_id),
    UNIQUE (id, tenant_id),
    UNIQUE (id, tenant_id, merchant_id),
    CHECK (disabled_at IS NULL OR status = 'disabled')
);
CREATE INDEX payment_links_list_idx ON payment_links (tenant_id, merchant_id, id DESC);

ALTER TABLE payment_routes ADD CONSTRAINT payment_routes_intent_tenant_unique UNIQUE(id,intent_id,tenant_id);
ALTER TABLE webhook_endpoints ADD CONSTRAINT webhook_endpoints_tenant_merchant_unique UNIQUE(id,tenant_id,merchant_id);

CREATE TABLE payment_link_redemptions (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    merchant_id uuid NOT NULL,
    payment_link_id uuid NOT NULL,
    intent_id uuid NOT NULL,
    checkout_token_hash bytea NOT NULL CHECK (octet_length(checkout_token_hash) = 32),
    idempotency_key text NOT NULL CHECK (length(idempotency_key) BETWEEN 8 AND 255),
    request_hash bytea NOT NULL CHECK (octet_length(request_hash) = 32),
    status text NOT NULL CHECK (status IN ('bound','settled','reversed')),
    created_at timestamptz NOT NULL,
    settled_at timestamptz,
    FOREIGN KEY (payment_link_id, tenant_id) REFERENCES payment_links(id, tenant_id),
    FOREIGN KEY (intent_id, tenant_id) REFERENCES payment_intents(id, tenant_id),
    FOREIGN KEY (merchant_id, tenant_id) REFERENCES merchants(id, tenant_id),
    UNIQUE (payment_link_id, idempotency_key),
    UNIQUE (intent_id, tenant_id),
    UNIQUE (checkout_token_hash)
);
CREATE INDEX payment_link_redemptions_link_idx ON payment_link_redemptions (tenant_id, payment_link_id, created_at DESC);

-- Multiple independently revocable short-lived tokens may refer to one intent.
ALTER TABLE checkout_sessions DROP CONSTRAINT IF EXISTS checkout_sessions_intent_id_tenant_id_key;
ALTER TABLE checkout_sessions
    ADD COLUMN audience text NOT NULL DEFAULT 'legacy_hosted_checkout' CHECK (audience IN ('legacy_hosted_checkout','hosted_checkout','embedded_checkout','payment_link')),
    ADD COLUMN allowed_origin text CHECK (allowed_origin IS NULL OR allowed_origin ~ '^https://[^/?#]+$'),
    ADD COLUMN allowed_actions text[] NOT NULL DEFAULT ARRAY['read']::text[] CHECK (allowed_actions <@ ARRAY['read','select_route']::text[] AND cardinality(allowed_actions) > 0),
    ADD COLUMN selected_route_id uuid,
    ADD COLUMN payment_link_id uuid,
    ADD COLUMN session_nonce uuid,
    ADD COLUMN selection_idempotency_key text,
    ADD COLUMN selection_request_hash bytea CHECK (selection_request_hash IS NULL OR octet_length(selection_request_hash)=32),
    ADD COLUMN version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    ADD CONSTRAINT checkout_sessions_selected_route_fk FOREIGN KEY (selected_route_id, intent_id, tenant_id) REFERENCES payment_routes(id, intent_id, tenant_id),
    ADD CONSTRAINT checkout_sessions_payment_link_fk FOREIGN KEY (payment_link_id, tenant_id, merchant_id) REFERENCES payment_links(id, tenant_id, merchant_id),
    ADD CONSTRAINT checkout_sessions_nonce_unique UNIQUE (session_nonce);
ALTER TABLE checkout_sessions ADD CONSTRAINT checkout_sessions_redemption_identity_unique UNIQUE(token_hash,intent_id,payment_link_id,tenant_id,merchant_id);
CREATE INDEX checkout_sessions_intent_multi_idx ON checkout_sessions (tenant_id, intent_id, expires_at DESC);

CREATE OR REPLACE FUNCTION lookup_checkout_session(requested_hash bytea)
RETURNS TABLE (tenant_id uuid, merchant_id uuid, intent_id uuid)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, public
SET row_security = off
AS $$
    SELECT cs.tenant_id,cs.merchant_id,cs.intent_id
      FROM public.checkout_sessions cs
      JOIN public.tenants t ON t.id=cs.tenant_id AND t.status='active'
      JOIN public.merchants m ON m.id=cs.merchant_id AND m.tenant_id=cs.tenant_id AND m.status='active'
     WHERE cs.token_hash=requested_hash
       AND cs.revoked_at IS NULL
       AND cs.expires_at>clock_timestamp()
       AND 'read'=ANY(cs.allowed_actions)
     LIMIT 1
$$;
REVOKE ALL ON FUNCTION lookup_checkout_session(bytea) FROM PUBLIC;

CREATE FUNCTION lookup_payment_link(requested_hash bytea)
RETURNS TABLE (tenant_id uuid, merchant_id uuid, payment_link_id uuid)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, public
SET row_security = off
AS $$
    SELECT l.tenant_id,l.merchant_id,l.id
      FROM public.payment_links l
      JOIN public.tenants t ON t.id=l.tenant_id AND t.status='active'
      JOIN public.merchants m ON m.id=l.merchant_id AND m.tenant_id=l.tenant_id AND m.status='active'
     -- Resolve the capability without leaking its state. The tenant-scoped,
     -- serializable redemption transaction rechecks status/expiry/capacity
     -- after replay lookup so an already committed one-time redemption can
     -- still be replayed idempotently.
     WHERE l.public_token_hash=requested_hash
     LIMIT 1
$$;
REVOKE ALL ON FUNCTION lookup_payment_link(bytea) FROM PUBLIC;

CREATE TABLE management_webhook_signing_keys (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    merchant_id uuid NOT NULL,
    endpoint_id uuid NOT NULL,
    key_id text NOT NULL,
    encrypted_secret bytea NOT NULL,
    status text NOT NULL CHECK (status IN ('current','overlap','revoked')),
    valid_from timestamptz NOT NULL,
    valid_until timestamptz,
    created_by uuid,
    replaced_by uuid,
    created_at timestamptz NOT NULL,
    revoked_at timestamptz,
    FOREIGN KEY (endpoint_id, tenant_id, merchant_id) REFERENCES webhook_endpoints(id, tenant_id, merchant_id),
    FOREIGN KEY (merchant_id, tenant_id) REFERENCES merchants(id, tenant_id),
    FOREIGN KEY (replaced_by) REFERENCES management_webhook_signing_keys(id),
    UNIQUE (endpoint_id, key_id),
    UNIQUE (endpoint_id, key_id, tenant_id),
    UNIQUE (id, tenant_id),
    CHECK (valid_until IS NULL OR valid_until>valid_from)
);
CREATE UNIQUE INDEX management_webhook_current_key_idx ON management_webhook_signing_keys(endpoint_id) WHERE status='current';
CREATE INDEX management_webhook_key_lookup_idx ON management_webhook_signing_keys(tenant_id,endpoint_id,key_id);

INSERT INTO management_webhook_signing_keys
(id,tenant_id,merchant_id,endpoint_id,key_id,encrypted_secret,status,valid_from,created_at)
SELECT gen_random_uuid(),tenant_id,merchant_id,id,signing_key_id,encrypted_signing_secret,'current',created_at,created_at
FROM webhook_endpoints;

-- Signature generation is frozen per endpoint delivery, not per merchant
-- event. One canonical event may fan out to endpoints with different keys.
ALTER TABLE callback_deliveries ADD COLUMN signing_key_id text;
UPDATE callback_deliveries d SET signing_key_id=w.signing_key_id
  FROM webhook_endpoints w
 WHERE w.id=d.endpoint_id AND w.tenant_id=d.tenant_id;
ALTER TABLE callback_deliveries ALTER COLUMN signing_key_id SET NOT NULL;
ALTER TABLE callback_deliveries ADD CONSTRAINT callback_delivery_signing_key_fk
  FOREIGN KEY(endpoint_id,signing_key_id,tenant_id)
  REFERENCES management_webhook_signing_keys(endpoint_id,key_id,tenant_id);

CREATE TABLE management_webhook_verifications (
    endpoint_id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    encrypted_challenge bytea NOT NULL,
    challenge_hash bytea NOT NULL CHECK (octet_length(challenge_hash) = 32),
    issued_at timestamptz NOT NULL,
    verified_at timestamptz,
    FOREIGN KEY (endpoint_id, tenant_id) REFERENCES webhook_endpoints(id, tenant_id)
);

CREATE TABLE management_api_clients (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    merchant_id uuid NOT NULL,
    name text NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
    status text NOT NULL CHECK (status IN ('active','revoked')),
    created_by uuid,
    revoked_by uuid,
    revoked_at timestamptz,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    FOREIGN KEY (merchant_id, tenant_id) REFERENCES merchants(id, tenant_id),
    UNIQUE (id, tenant_id)
);

CREATE TABLE management_api_client_versions (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    management_client_id uuid NOT NULL,
    api_client_id uuid NOT NULL,
    key_id text NOT NULL,
    version_number bigint NOT NULL CHECK (version_number > 0),
    status text NOT NULL CHECK (status IN ('current','overlap','revoked')),
    valid_from timestamptz NOT NULL,
    valid_until timestamptz,
    revoked_at timestamptz,
    created_at timestamptz NOT NULL,
    FOREIGN KEY (management_client_id, tenant_id) REFERENCES management_api_clients(id, tenant_id),
    FOREIGN KEY (api_client_id, tenant_id) REFERENCES api_clients(id, tenant_id),
    UNIQUE (management_client_id, version_number),
    UNIQUE (key_id),
    UNIQUE (id, tenant_id)
);
CREATE UNIQUE INDEX management_api_client_current_idx ON management_api_client_versions(management_client_id) WHERE status='current';

-- Existing credentials remain manageable without changing their authentication identity.
INSERT INTO management_api_clients(id,tenant_id,merchant_id,name,status,created_at,updated_at,version)
SELECT id,tenant_id,merchant_id,key_id,CASE WHEN revoked_at IS NULL THEN 'active' ELSE 'revoked' END,created_at,updated_at,version
FROM api_clients;
INSERT INTO management_api_client_versions(id,tenant_id,management_client_id,api_client_id,key_id,version_number,status,valid_from,valid_until,revoked_at,created_at)
SELECT gen_random_uuid(),tenant_id,id,id,key_id,1,CASE WHEN revoked_at IS NULL THEN 'current' ELSE 'revoked' END,valid_from,valid_until,revoked_at,created_at
FROM api_clients;

CREATE TABLE management_idempotency_records (
    tenant_id uuid NOT NULL,
    merchant_id uuid NOT NULL,
    operation text NOT NULL,
    idempotency_key text NOT NULL CHECK (length(idempotency_key) BETWEEN 8 AND 255),
    request_hash bytea NOT NULL CHECK (octet_length(request_hash) = 32),
    resource_type text NOT NULL,
    resource_id uuid NOT NULL,
    response_status integer NOT NULL,
    encrypted_response bytea NOT NULL,
    response_hash bytea NOT NULL CHECK (octet_length(response_hash) = 32),
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    FOREIGN KEY (merchant_id, tenant_id) REFERENCES merchants(id, tenant_id),
    PRIMARY KEY (merchant_id,operation,idempotency_key)
);
CREATE INDEX management_idempotency_expiry_idx ON management_idempotency_records(expires_at);

-- Dangerous management mutations are never authorized by browser-supplied
-- approver claims. The exact typed mutation is frozen here and executed only
-- after a distinct, recently stepped-up administrator approves it.
CREATE TABLE management_action_requests (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    merchant_id uuid NOT NULL,
    operation text NOT NULL CHECK (operation IN ('webhook.disable','api_client.revoke')),
    resource_type text NOT NULL CHECK (resource_type IN ('webhook_endpoint','api_client')),
    resource_id uuid NOT NULL,
    resource_version bigint NOT NULL CHECK (resource_version > 0),
    request_body jsonb NOT NULL CHECK (jsonb_typeof(request_body)='object' AND pg_column_size(request_body)<=4096),
    request_hash bytea NOT NULL CHECK (octet_length(request_hash)=32),
    mutation_idempotency_key text NOT NULL CHECK (length(mutation_idempotency_key) BETWEEN 8 AND 255),
    requested_by uuid NOT NULL,
    requested_session text NOT NULL CHECK (length(requested_session) BETWEEN 1 AND 255),
    requested_step_up_at timestamptz NOT NULL,
    request_reason text NOT NULL CHECK (length(btrim(request_reason)) BETWEEN 1 AND 1000),
    approved_by uuid,
    approval_reason text CHECK (approval_reason IS NULL OR length(btrim(approval_reason)) BETWEEN 1 AND 1000),
    approval_hash bytea CHECK (approval_hash IS NULL OR octet_length(approval_hash)=32),
    status text NOT NULL CHECK (status IN ('pending_approval','executing','completed','rejected','failed')),
    failure_code text,
    lease_token uuid,
    lease_until timestamptz,
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    approved_at timestamptz,
    completed_at timestamptz,
    updated_at timestamptz NOT NULL,
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    FOREIGN KEY (merchant_id,tenant_id) REFERENCES merchants(id,tenant_id),
    UNIQUE (merchant_id,operation,mutation_idempotency_key),
    UNIQUE (id,tenant_id),
    CHECK (expires_at>created_at),
    CHECK (approved_by IS NULL OR approved_by<>requested_by),
    CHECK ((status='pending_approval' AND approved_by IS NULL AND approval_hash IS NULL) OR status<>'pending_approval'),
    CHECK ((status='executing' AND lease_token IS NOT NULL AND lease_until IS NOT NULL) OR status<>'executing')
);
CREATE INDEX management_action_requests_list_idx ON management_action_requests(tenant_id,merchant_id,operation,id DESC);

CREATE TABLE management_audit_log (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    merchant_id uuid NOT NULL,
    sequence bigint NOT NULL CHECK (sequence > 0),
    actor_id uuid NOT NULL,
    session_id text,
    approval_actor_id uuid,
    action text NOT NULL,
    resource_type text NOT NULL,
    resource_id uuid NOT NULL,
    reason text,
    details jsonb NOT NULL CHECK (jsonb_typeof(details)='object' AND pg_column_size(details)<=16384),
    previous_hash bytea NOT NULL CHECK (octet_length(previous_hash) IN (0,32)),
    entry_hash bytea NOT NULL CHECK (octet_length(entry_hash)=32),
    occurred_at timestamptz NOT NULL,
    FOREIGN KEY (merchant_id,tenant_id) REFERENCES merchants(id,tenant_id),
    UNIQUE (tenant_id,merchant_id,sequence),
    UNIQUE (tenant_id,entry_hash)
);
CREATE INDEX management_audit_cursor_idx ON management_audit_log(tenant_id,merchant_id,id DESC);

CREATE FUNCTION reject_management_audit_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'management audit is append-only';
END $$;
CREATE TRIGGER management_audit_immutable BEFORE UPDATE OR DELETE ON management_audit_log FOR EACH ROW EXECUTE FUNCTION reject_management_audit_mutation();

CREATE FUNCTION validate_management_actor(p_actor uuid,p_tenant uuid,p_merchant uuid,p_permission text)
RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE allowed boolean;
BEGIN
    SELECT EXISTS(SELECT 1 FROM public.api_clients c WHERE c.id=p_actor AND c.tenant_id=p_tenant AND c.merchant_id=p_merchant AND c.revoked_at IS NULL AND c.valid_from<=clock_timestamp() AND (c.valid_until IS NULL OR c.valid_until>clock_timestamp())
      AND ('management:*'=ANY(c.scopes) OR CASE p_permission
        WHEN 'payment_links:read' THEN c.scopes && ARRAY['payment-links:read','payment-links:write']::text[]
        WHEN 'payment_links:write' THEN 'payment-links:write'=ANY(c.scopes)
        WHEN 'checkout:write' THEN 'checkout:write'=ANY(c.scopes)
        WHEN 'webhook_settings:read' THEN c.scopes && ARRAY['webhooks:read','webhooks:write']::text[]
        WHEN 'webhook_settings:write' THEN 'webhooks:write'=ANY(c.scopes)
        WHEN 'webhook_settings:rotate' THEN 'webhooks:rotate'=ANY(c.scopes)
        WHEN 'webhook_settings:disable' THEN 'webhooks:disable'=ANY(c.scopes)
        WHEN 'api_clients:read' THEN c.scopes && ARRAY['credentials:read','credentials:write']::text[]
        WHEN 'api_clients:write' THEN 'credentials:write'=ANY(c.scopes)
        WHEN 'api_clients:rotate' THEN 'credentials:rotate'=ANY(c.scopes)
        WHEN 'api_clients:revoke' THEN 'credentials:revoke'=ANY(c.scopes)
        WHEN 'management_audit:read' THEN 'audit:read'=ANY(c.scopes)
        ELSE false END)) INTO allowed;
    IF NOT allowed THEN
        SELECT EXISTS(SELECT 1 FROM public.admin_users u JOIN public.admin_role_bindings b ON b.user_id=u.id
          JOIN public.admin_role_permissions rp ON rp.role_key=b.role_key AND rp.permission_key=p_permission
          WHERE u.id=p_actor AND u.status='active' AND b.revoked_at IS NULL AND (b.expires_at IS NULL OR b.expires_at>clock_timestamp())
            AND (b.tenant_id IS NULL OR b.tenant_id=p_tenant) AND (b.merchant_id IS NULL OR b.merchant_id=p_merchant)) INTO allowed;
    END IF;
    RETURN allowed;
END $$;
REVOKE ALL ON FUNCTION validate_management_actor(uuid,uuid,uuid,text) FROM PUBLIC;

CREATE FUNCTION management_permission_for_action(p_action text)
RETURNS text LANGUAGE sql IMMUTABLE SET search_path=pg_catalog,public AS $$
    SELECT CASE
      WHEN p_action LIKE 'payment_link.%' THEN 'payment_links:write'
      WHEN p_action LIKE 'checkout.%' THEN 'checkout:write'
      WHEN p_action='webhook.secret_rotated' THEN 'webhook_settings:rotate'
      WHEN p_action='webhook.disabled' THEN 'webhook_settings:disable'
      WHEN p_action IN ('webhook.disable_requested','webhook.disable_rejected') THEN 'webhook_settings:disable'
      WHEN p_action LIKE 'webhook.%' THEN 'webhook_settings:write'
      WHEN p_action='api_client.rotated' THEN 'api_clients:rotate'
      WHEN p_action='api_client.revoked' THEN 'api_clients:revoke'
      WHEN p_action IN ('api_client.revoke_requested','api_client.revoke_rejected') THEN 'api_clients:revoke'
      WHEN p_action LIKE 'api_client.%' THEN 'api_clients:write'
      ELSE NULL END
$$;
REVOKE ALL ON FUNCTION management_permission_for_action(text) FROM PUBLIC;

CREATE FUNCTION append_management_audit(
    p_id uuid,p_tenant uuid,p_merchant uuid,p_actor uuid,p_session text,p_approval_actor uuid,
    p_action text,p_resource_type text,p_resource_id uuid,p_reason text,p_details jsonb,p_occurred_at timestamptz
) RETURNS bytea LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE
    v_sequence bigint;
    v_previous bytea;
    v_hash bytea;
    v_permission text;
BEGIN
    v_permission := management_permission_for_action(p_action);
    IF v_permission IS NULL OR NOT validate_management_actor(p_actor,p_tenant,p_merchant,v_permission) THEN RAISE EXCEPTION 'management actor is not authorized'; END IF;
    IF p_approval_actor IS NOT NULL AND (p_approval_actor=p_actor OR NOT validate_management_actor(p_approval_actor,p_tenant,p_merchant,v_permission)) THEN RAISE EXCEPTION 'management approver is not authorized or distinct'; END IF;
    PERFORM pg_advisory_xact_lock(hashtextextended(p_tenant::text||':'||p_merchant::text,0));
    SELECT sequence,entry_hash INTO v_sequence,v_previous FROM management_audit_log
      WHERE tenant_id=p_tenant AND merchant_id=p_merchant ORDER BY sequence DESC LIMIT 1 FOR UPDATE;
    v_sequence := COALESCE(v_sequence,0)+1;
    v_previous := COALESCE(v_previous,''::bytea);
    v_hash := digest(v_previous || convert_to(concat_ws('|',p_id::text,p_tenant::text,p_merchant::text,v_sequence::text,p_actor::text,COALESCE(p_session,''),COALESCE(p_approval_actor::text,''),p_action,p_resource_type,p_resource_id::text,COALESCE(p_reason,''),p_details::text,p_occurred_at::text),'UTF8'),'sha256');
    INSERT INTO management_audit_log(id,tenant_id,merchant_id,sequence,actor_id,session_id,approval_actor_id,action,resource_type,resource_id,reason,details,previous_hash,entry_hash,occurred_at)
    VALUES(p_id,p_tenant,p_merchant,v_sequence,p_actor,NULLIF(p_session,''),p_approval_actor,p_action,p_resource_type,p_resource_id,NULLIF(p_reason,''),p_details,v_previous,v_hash,p_occurred_at);
    RETURN v_hash;
END $$;
REVOKE ALL ON FUNCTION append_management_audit(uuid,uuid,uuid,uuid,text,uuid,text,text,uuid,text,jsonb,timestamptz) FROM PUBLIC;
REVOKE INSERT,UPDATE,DELETE ON management_audit_log FROM PUBLIC;
COMMENT ON FUNCTION append_management_audit(uuid,uuid,uuid,uuid,text,uuid,text,text,uuid,text,jsonb,timestamptz) IS 'Migration-owned BYPASSRLS boundary. Grant EXECUTE, never direct audit mutation, to the management runtime role.';

CREATE TABLE management_assertion_nonces (
    jti uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL
);
CREATE INDEX management_assertion_nonce_expiry_idx ON management_assertion_nonces(expires_at);

CREATE FUNCTION consume_management_assertion(p_jti uuid,p_tenant uuid,p_expires timestamptz)
RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
BEGIN
    INSERT INTO public.management_assertion_nonces(jti,tenant_id,expires_at,created_at)
    VALUES(p_jti,p_tenant,p_expires,clock_timestamp()) ON CONFLICT DO NOTHING;
    RETURN FOUND;
END $$;
REVOKE ALL ON FUNCTION consume_management_assertion(uuid,uuid,timestamptz) FROM PUBLIC;
COMMENT ON FUNCTION consume_management_assertion(uuid,uuid,timestamptz) IS 'Migration-owned BYPASSRLS pre-auth replay boundary; grant EXECUTE only to the management runtime role.';

-- Every management mutation also uses the existing transactional outbox. New
-- tables are tenant isolated even for table owners.
ALTER TABLE payment_links ENABLE ROW LEVEL SECURITY;
ALTER TABLE payment_link_redemptions ENABLE ROW LEVEL SECURITY;
ALTER TABLE management_webhook_signing_keys ENABLE ROW LEVEL SECURITY;
ALTER TABLE management_webhook_verifications ENABLE ROW LEVEL SECURITY;
ALTER TABLE management_api_clients ENABLE ROW LEVEL SECURITY;
ALTER TABLE management_api_client_versions ENABLE ROW LEVEL SECURITY;
ALTER TABLE management_idempotency_records ENABLE ROW LEVEL SECURITY;
ALTER TABLE management_action_requests ENABLE ROW LEVEL SECURITY;
ALTER TABLE management_audit_log ENABLE ROW LEVEL SECURITY;
ALTER TABLE management_assertion_nonces ENABLE ROW LEVEL SECURITY;

ALTER TABLE payment_links FORCE ROW LEVEL SECURITY;
ALTER TABLE payment_link_redemptions FORCE ROW LEVEL SECURITY;
ALTER TABLE management_webhook_signing_keys FORCE ROW LEVEL SECURITY;
ALTER TABLE management_webhook_verifications FORCE ROW LEVEL SECURITY;
ALTER TABLE management_api_clients FORCE ROW LEVEL SECURITY;
ALTER TABLE management_api_client_versions FORCE ROW LEVEL SECURITY;
ALTER TABLE management_idempotency_records FORCE ROW LEVEL SECURITY;
ALTER TABLE management_action_requests FORCE ROW LEVEL SECURITY;
ALTER TABLE management_audit_log FORCE ROW LEVEL SECURITY;
ALTER TABLE management_assertion_nonces FORCE ROW LEVEL SECURITY;

CREATE POLICY payment_links_tenant ON payment_links USING (tenant_id=current_setting('app.tenant_id',true)::uuid) WITH CHECK (tenant_id=current_setting('app.tenant_id',true)::uuid);
CREATE POLICY payment_link_redemptions_tenant ON payment_link_redemptions USING (tenant_id=current_setting('app.tenant_id',true)::uuid) WITH CHECK (tenant_id=current_setting('app.tenant_id',true)::uuid);
CREATE POLICY management_webhook_keys_tenant ON management_webhook_signing_keys USING (tenant_id=current_setting('app.tenant_id',true)::uuid) WITH CHECK (tenant_id=current_setting('app.tenant_id',true)::uuid);
CREATE POLICY management_webhook_verifications_tenant ON management_webhook_verifications USING (tenant_id=current_setting('app.tenant_id',true)::uuid) WITH CHECK (tenant_id=current_setting('app.tenant_id',true)::uuid);
CREATE POLICY management_api_clients_tenant ON management_api_clients USING (tenant_id=current_setting('app.tenant_id',true)::uuid) WITH CHECK (tenant_id=current_setting('app.tenant_id',true)::uuid);
CREATE POLICY management_api_client_versions_tenant ON management_api_client_versions USING (tenant_id=current_setting('app.tenant_id',true)::uuid) WITH CHECK (tenant_id=current_setting('app.tenant_id',true)::uuid);
CREATE POLICY management_idempotency_tenant ON management_idempotency_records USING (tenant_id=current_setting('app.tenant_id',true)::uuid) WITH CHECK (tenant_id=current_setting('app.tenant_id',true)::uuid);
CREATE POLICY management_action_requests_tenant ON management_action_requests USING (tenant_id=current_setting('app.tenant_id',true)::uuid) WITH CHECK (tenant_id=current_setting('app.tenant_id',true)::uuid);
CREATE POLICY management_audit_tenant ON management_audit_log USING (tenant_id=current_setting('app.tenant_id',true)::uuid) WITH CHECK (tenant_id=current_setting('app.tenant_id',true)::uuid);
CREATE POLICY management_assertion_nonce_tenant ON management_assertion_nonces USING (tenant_id=current_setting('app.tenant_id',true)::uuid) WITH CHECK (tenant_id=current_setting('app.tenant_id',true)::uuid);

CREATE FUNCTION validate_management_actor_columns() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE actor uuid; merchant uuid; permission text;
BEGIN
    merchant := NEW.merchant_id;
    IF TG_TABLE_NAME='payment_links' THEN
        actor := CASE WHEN TG_OP='INSERT' THEN NEW.created_by ELSE COALESCE(NEW.disabled_by,NEW.created_by) END;
        permission := 'payment_links:write';
    ELSIF TG_TABLE_NAME='management_api_clients' THEN
        actor := CASE WHEN TG_OP='INSERT' THEN NEW.created_by ELSE COALESCE(NEW.revoked_by,NEW.created_by) END;
        permission := CASE WHEN NEW.revoked_by IS NULL THEN 'api_clients:write' ELSE 'api_clients:revoke' END;
    ELSIF TG_TABLE_NAME='management_action_requests' THEN
        actor := NEW.requested_by;
        permission := CASE NEW.operation WHEN 'webhook.disable' THEN 'webhook_settings:disable' WHEN 'api_client.revoke' THEN 'api_clients:revoke' ELSE NULL END;
        IF permission IS NULL OR NEW.approved_by IS NOT NULL AND
          (NEW.approved_by=NEW.requested_by OR NOT validate_management_actor(NEW.approved_by,NEW.tenant_id,merchant,permission))
        THEN RAISE EXCEPTION 'management approver is not authorized or distinct'; END IF;
    ELSE
        RETURN NEW;
    END IF;
    IF actor IS NULL OR NOT validate_management_actor(actor,NEW.tenant_id,merchant,permission) THEN RAISE EXCEPTION 'management actor is not authorized'; END IF;
    RETURN NEW;
END $$;
CREATE CONSTRAINT TRIGGER payment_links_actor_guard AFTER INSERT OR UPDATE OF disabled_by ON payment_links DEFERRABLE INITIALLY IMMEDIATE FOR EACH ROW EXECUTE FUNCTION validate_management_actor_columns();
CREATE CONSTRAINT TRIGGER management_api_clients_actor_guard AFTER INSERT OR UPDATE OF revoked_by ON management_api_clients DEFERRABLE INITIALLY IMMEDIATE FOR EACH ROW EXECUTE FUNCTION validate_management_actor_columns();
CREATE CONSTRAINT TRIGGER management_action_requests_actor_guard AFTER INSERT OR UPDATE OF approved_by ON management_action_requests DEFERRABLE INITIALLY IMMEDIATE FOR EACH ROW EXECUTE FUNCTION validate_management_actor_columns();

-- Cross-object composite invariant used by public checkout and redemption.
ALTER TABLE payment_link_redemptions ADD CONSTRAINT payment_link_redemption_checkout_fk
    FOREIGN KEY (checkout_token_hash,intent_id,payment_link_id,tenant_id,merchant_id)
    REFERENCES checkout_sessions(token_hash,intent_id,payment_link_id,tenant_id,merchant_id);

COMMIT;
