BEGIN;

-- Merchant cabinet identities are backed by an authenticated admin/OIDC user.
-- The cabinet roles are deliberately closed: callers can select a role, but
-- can never submit arbitrary permissions or define a privilege-bearing role.
CREATE TABLE merchant_cabinet_permissions (
    permission_key text PRIMARY KEY CHECK (permission_key ~ '^[a-z][a-z_]*:[a-z][a-z_]*$'),
    description text NOT NULL CHECK (length(description) BETWEEN 1 AND 500)
);

CREATE TABLE merchant_cabinet_roles (
    role_key text PRIMARY KEY CHECK (role_key IN ('owner','security_admin','admin','developer','support','viewer')),
    description text NOT NULL CHECK (length(description) BETWEEN 1 AND 500),
    high_risk boolean NOT NULL,
    system_managed boolean NOT NULL DEFAULT true CHECK (system_managed)
);

CREATE TABLE merchant_cabinet_role_permissions (
    role_key text NOT NULL REFERENCES merchant_cabinet_roles(role_key) ON DELETE RESTRICT,
    permission_key text NOT NULL REFERENCES merchant_cabinet_permissions(permission_key) ON DELETE RESTRICT,
    PRIMARY KEY (role_key,permission_key)
);

INSERT INTO merchant_cabinet_permissions(permission_key,description) VALUES
('team:read','Read merchant members, roles, invitations, and security requests'),
('team:invite','Invite a member into the merchant cabinet'),
('team:manage','Change ordinary roles and disable or remove ordinary members'),
('team:security_request','Request changes affecting owner or security roles'),
('team:security_approve','Approve a distinct actor security request after MFA'),
('settings:read','Read non-financial merchant project settings'),
('settings:write','Update non-financial merchant project settings'),
('settings_audit:read','Read the immutable merchant settings audit chain');

INSERT INTO merchant_cabinet_roles(role_key,description,high_risk) VALUES
('owner','Full merchant cabinet ownership',true),
('security_admin','Team security request and approval authority',true),
('admin','Ordinary team and project settings administration',false),
('developer','Integration settings and read access',false),
('support','Support and read access',false),
('viewer','Read-only merchant cabinet access',false);

INSERT INTO merchant_cabinet_role_permissions(role_key,permission_key) VALUES
('owner','team:read'),('owner','team:invite'),('owner','team:manage'),
('owner','team:security_request'),('owner','team:security_approve'),
('owner','settings:read'),('owner','settings:write'),('owner','settings_audit:read'),
('security_admin','team:read'),('security_admin','team:invite'),('security_admin','team:manage'),
('security_admin','team:security_request'),('security_admin','team:security_approve'),
('security_admin','settings:read'),('security_admin','settings_audit:read'),
('admin','team:read'),('admin','team:invite'),('admin','team:manage'),
('admin','settings:read'),('admin','settings:write'),('admin','settings_audit:read'),
('developer','team:read'),('developer','settings:read'),('developer','settings:write'),
('support','team:read'),('support','settings:read'),
('viewer','team:read'),('viewer','settings:read');

CREATE TABLE merchant_members (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    merchant_id uuid NOT NULL,
    admin_user_id uuid NOT NULL REFERENCES admin_users(id) ON DELETE RESTRICT,
    oidc_issuer text NOT NULL CHECK (oidc_issuer LIKE 'https://%' AND right(oidc_issuer,1)<>'/'),
    oidc_subject text NOT NULL CHECK (length(oidc_subject) BETWEEN 1 AND 255),
    email text NOT NULL CHECK (email=lower(email) AND length(email) BETWEEN 3 AND 320 AND email LIKE '%_@_%._%'),
    display_name text NOT NULL CHECK (length(btrim(display_name)) BETWEEN 1 AND 255),
    status text NOT NULL CHECK (status IN ('active','disabled','removed')),
    joined_at timestamptz NOT NULL,
    disabled_at timestamptz,
    removed_at timestamptz,
    updated_at timestamptz NOT NULL,
    version bigint NOT NULL DEFAULT 1 CHECK (version>0),
    FOREIGN KEY (merchant_id,tenant_id) REFERENCES merchants(id,tenant_id) ON DELETE RESTRICT,
    UNIQUE (id,tenant_id,merchant_id),
    UNIQUE (merchant_id,admin_user_id),
    UNIQUE (merchant_id,oidc_issuer,oidc_subject),
    CHECK ((status='active' AND disabled_at IS NULL AND removed_at IS NULL) OR
           (status='disabled' AND disabled_at IS NOT NULL AND removed_at IS NULL) OR
           (status='removed' AND removed_at IS NOT NULL))
);
CREATE INDEX merchant_members_list_idx ON merchant_members(tenant_id,merchant_id,status,id DESC);

CREATE TABLE merchant_member_role_bindings (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    merchant_id uuid NOT NULL,
    member_id uuid NOT NULL,
    role_key text NOT NULL REFERENCES merchant_cabinet_roles(role_key) ON DELETE RESTRICT,
    granted_by uuid NOT NULL REFERENCES admin_users(id) ON DELETE RESTRICT,
    approved_by uuid REFERENCES admin_users(id) ON DELETE RESTRICT,
    grant_request_id uuid,
    granted_at timestamptz NOT NULL,
    revoked_by uuid REFERENCES admin_users(id) ON DELETE RESTRICT,
    revoke_request_id uuid,
    revoked_at timestamptz,
    reason text NOT NULL CHECK (length(btrim(reason)) BETWEEN 8 AND 1000),
    version bigint NOT NULL DEFAULT 1 CHECK (version>0),
    FOREIGN KEY (member_id,tenant_id,merchant_id) REFERENCES merchant_members(id,tenant_id,merchant_id) ON DELETE RESTRICT,
    FOREIGN KEY (merchant_id,tenant_id) REFERENCES merchants(id,tenant_id) ON DELETE RESTRICT,
    UNIQUE (id,tenant_id,merchant_id),
    CHECK (approved_by IS NULL OR approved_by<>granted_by),
    CHECK (revoked_at IS NOT NULL OR revoked_by IS NULL)
);
CREATE UNIQUE INDEX merchant_member_active_role_idx ON merchant_member_role_bindings(member_id,role_key) WHERE revoked_at IS NULL;
CREATE INDEX merchant_member_roles_scope_idx ON merchant_member_role_bindings(tenant_id,merchant_id,member_id) WHERE revoked_at IS NULL;

-- The raw invitation token never enters PostgreSQL. token_hash is SHA-256 of
-- 32 cryptographically random bytes and is the only persisted credential.
CREATE TABLE merchant_member_invitations (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    merchant_id uuid NOT NULL,
    email text NOT NULL CHECK (email=lower(email) AND length(email) BETWEEN 3 AND 320 AND email LIKE '%_@_%._%'),
    token_hash bytea NOT NULL UNIQUE CHECK (octet_length(token_hash)=32),
    token_key_id text NOT NULL CHECK (token_key_id ~ '^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$'),
    role_keys text[] NOT NULL CHECK (cardinality(role_keys) BETWEEN 1 AND 6 AND role_keys <@ ARRAY['admin','developer','support','viewer']::text[]),
    delivery_mode text NOT NULL CHECK (delivery_mode IN ('copy_once','email')),
    status text NOT NULL CHECK (status IN ('pending_delivery','active','accepted','revoked','expired')),
    invited_by uuid NOT NULL REFERENCES admin_users(id) ON DELETE RESTRICT,
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    activated_at timestamptz,
    accepted_by_member_id uuid,
    accepted_oidc_issuer text,
    accepted_oidc_subject text,
    accepted_at timestamptz,
    revoked_by uuid REFERENCES admin_users(id) ON DELETE RESTRICT,
    revoked_at timestamptz,
    revoke_reason text,
    version bigint NOT NULL DEFAULT 1 CHECK (version>0),
    FOREIGN KEY (merchant_id,tenant_id) REFERENCES merchants(id,tenant_id) ON DELETE RESTRICT,
    FOREIGN KEY (accepted_by_member_id,tenant_id,merchant_id) REFERENCES merchant_members(id,tenant_id,merchant_id) ON DELETE RESTRICT,
    UNIQUE (id,tenant_id,merchant_id),
    CHECK (expires_at>created_at AND expires_at<=created_at+interval '30 days'),
    CHECK (accepted_at IS NULL OR status='accepted'),
    CHECK (revoked_at IS NULL OR status='revoked'),
    CHECK (activated_at IS NULL OR status IN ('active','accepted','expired','revoked'))
);
CREATE UNIQUE INDEX merchant_invitation_open_email_idx ON merchant_member_invitations(merchant_id,email) WHERE status IN ('pending_delivery','active');
CREATE INDEX merchant_invitation_expiry_idx ON merchant_member_invitations(expires_at) WHERE status IN ('pending_delivery','active');

CREATE TABLE merchant_invitation_delivery_jobs (
    invitation_id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    merchant_id uuid NOT NULL,
    token_key_id text NOT NULL CHECK (token_key_id ~ '^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$'),
    status text NOT NULL CHECK (status IN ('ready','leased','retry','acknowledged','dead_letter','expired')),
    attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count BETWEEN 0 AND 100),
    next_attempt_at timestamptz NOT NULL,
    lease_owner uuid,
    lease_token uuid,
    lease_until timestamptz,
    provider_delivery_id text CHECK (provider_delivery_id IS NULL OR length(provider_delivery_id) BETWEEN 1 AND 255),
    last_error_code text CHECK (last_error_code IS NULL OR last_error_code ~ '^[a-z0-9_]{1,80}$'),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    completed_at timestamptz,
    FOREIGN KEY (invitation_id,tenant_id,merchant_id) REFERENCES merchant_member_invitations(id,tenant_id,merchant_id) ON DELETE RESTRICT,
    FOREIGN KEY (merchant_id,tenant_id) REFERENCES merchants(id,tenant_id) ON DELETE RESTRICT,
    CHECK ((status='leased' AND lease_owner IS NOT NULL AND lease_token IS NOT NULL AND lease_until IS NOT NULL) OR status<>'leased')
);
CREATE INDEX merchant_invitation_delivery_queue_idx ON merchant_invitation_delivery_jobs(status,next_attempt_at,invitation_id) WHERE status IN ('ready','retry','leased');

CREATE TABLE merchant_invitation_delivery_workers (
    worker_id uuid PRIMARY KEY,
    last_seen_at timestamptz NOT NULL,
    started_at timestamptz NOT NULL
);

-- The BFF resolves an opaque invite token to its scope before minting the
-- request-bound internal assertion. No caller-supplied tenant/merchant is trusted.
CREATE FUNCTION lookup_merchant_invitation(requested_hash bytea)
RETURNS TABLE(tenant_id uuid,merchant_id uuid)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
  SELECT i.tenant_id,i.merchant_id FROM public.merchant_member_invitations i
    JOIN public.tenants t ON t.id=i.tenant_id AND t.status='active'
    JOIN public.merchants m ON m.id=i.merchant_id AND m.tenant_id=i.tenant_id AND m.status='active'
   WHERE i.token_hash=requested_hash AND i.status='active' AND i.expires_at>clock_timestamp() LIMIT 1
$$;
REVOKE ALL ON FUNCTION lookup_merchant_invitation(bytea) FROM PUBLIC;

CREATE FUNCTION merchant_security_payload_hash(p_operation text,p_target uuid,p_version bigint,p_roles text[])
RETURNS bytea LANGUAGE sql IMMUTABLE SET search_path=pg_catalog,public AS $$
  SELECT digest(convert_to(concat_ws(chr(31),p_operation,p_target::text,p_version::text,array_to_string(p_roles,',')),'UTF8'),'sha256')
$$;
REVOKE ALL ON FUNCTION merchant_security_payload_hash(text,uuid,bigint,text[]) FROM PUBLIC;

-- A frozen request is the only route for owner/security_admin grants/removals
-- and for disabling/removing a member who currently holds either role.
CREATE TABLE merchant_security_action_requests (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    merchant_id uuid NOT NULL,
    operation text NOT NULL CHECK (operation IN ('member.roles.replace','member.disable','member.remove')),
    target_member_id uuid NOT NULL,
    target_version bigint NOT NULL CHECK (target_version>0),
    desired_role_keys text[] NOT NULL DEFAULT '{}' CHECK (desired_role_keys <@ ARRAY['owner','security_admin','admin','developer','support','viewer']::text[]),
    payload_hash bytea NOT NULL CHECK (octet_length(payload_hash)=32),
    requested_by uuid NOT NULL REFERENCES admin_users(id) ON DELETE RESTRICT,
    requested_session_id uuid NOT NULL REFERENCES admin_sessions(id) ON DELETE RESTRICT,
    request_reason text NOT NULL CHECK (length(btrim(request_reason)) BETWEEN 8 AND 1000),
    requested_mfa_at timestamptz NOT NULL,
    approved_by uuid REFERENCES admin_users(id) ON DELETE RESTRICT,
    approved_session_id uuid REFERENCES admin_sessions(id) ON DELETE RESTRICT,
    approval_reason text,
    approved_mfa_at timestamptz,
    status text NOT NULL CHECK (status IN ('pending_approval','completed','rejected','expired','failed')),
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    decided_at timestamptz,
    updated_at timestamptz NOT NULL,
    version bigint NOT NULL DEFAULT 1 CHECK (version>0),
    FOREIGN KEY (target_member_id,tenant_id,merchant_id) REFERENCES merchant_members(id,tenant_id,merchant_id) ON DELETE RESTRICT,
    FOREIGN KEY (merchant_id,tenant_id) REFERENCES merchants(id,tenant_id) ON DELETE RESTRICT,
    UNIQUE (id,tenant_id,merchant_id),
    CHECK (expires_at>created_at AND expires_at<=created_at+interval '24 hours'),
    CHECK (payload_hash=merchant_security_payload_hash(operation,target_member_id,target_version,desired_role_keys)),
    CHECK (approved_by IS NULL OR approved_by<>requested_by),
    CHECK (approved_session_id IS NULL OR approved_session_id<>requested_session_id),
    CHECK (requested_mfa_at>=created_at-interval '10 minutes' AND requested_mfa_at<=created_at+interval '15 seconds'),
    CHECK ((status='pending_approval' AND approved_by IS NULL AND decided_at IS NULL) OR
           (status='completed' AND approved_by IS NOT NULL AND approved_session_id IS NOT NULL AND approved_mfa_at IS NOT NULL AND decided_at IS NOT NULL AND approved_mfa_at>=decided_at-interval '10 minutes' AND approved_mfa_at<=decided_at+interval '15 seconds') OR
           (status IN ('rejected','expired','failed') AND decided_at IS NOT NULL))
);
CREATE INDEX merchant_security_actions_queue_idx ON merchant_security_action_requests(tenant_id,merchant_id,status,expires_at,id DESC);

ALTER TABLE merchant_member_role_bindings
  ADD CONSTRAINT merchant_role_grant_request_fk FOREIGN KEY(grant_request_id,tenant_id,merchant_id) REFERENCES merchant_security_action_requests(id,tenant_id,merchant_id) ON DELETE RESTRICT,
  ADD CONSTRAINT merchant_role_revoke_request_fk FOREIGN KEY(revoke_request_id,tenant_id,merchant_id) REFERENCES merchant_security_action_requests(id,tenant_id,merchant_id) ON DELETE RESTRICT;

CREATE TABLE merchant_session_revocation_signals (
    sequence bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    id uuid NOT NULL UNIQUE,
    tenant_id uuid NOT NULL,
    merchant_id uuid NOT NULL,
    admin_user_id uuid NOT NULL REFERENCES admin_users(id) ON DELETE RESTRICT,
    member_id uuid NOT NULL,
    reason text NOT NULL CHECK (length(reason) BETWEEN 1 AND 255),
    requested_by uuid NOT NULL REFERENCES admin_users(id) ON DELETE RESTRICT,
    created_at timestamptz NOT NULL,
    consumed_at timestamptz,
    FOREIGN KEY (member_id,tenant_id,merchant_id) REFERENCES merchant_members(id,tenant_id,merchant_id) ON DELETE RESTRICT,
    FOREIGN KEY (merchant_id,tenant_id) REFERENCES merchants(id,tenant_id) ON DELETE RESTRICT
);
CREATE INDEX merchant_session_revocations_pending_idx ON merchant_session_revocation_signals(sequence) WHERE consumed_at IS NULL;

CREATE TABLE merchant_project_settings (
    tenant_id uuid NOT NULL,
    merchant_id uuid NOT NULL,
    display_name text NOT NULL CHECK (length(btrim(display_name)) BETWEEN 1 AND 120),
    locale text NOT NULL CHECK (locale IN ('en','zh-CN','es','fr','de','ru')),
    timezone text NOT NULL CHECK (length(timezone) BETWEEN 1 AND 100),
    support_email text CHECK (support_email IS NULL OR (support_email=lower(support_email) AND length(support_email) BETWEEN 3 AND 320 AND support_email LIKE '%_@_%._%')),
    notify_payment_succeeded boolean NOT NULL,
    notify_payment_failed boolean NOT NULL,
    notify_weekly_summary boolean NOT NULL,
    allowed_embed_origins text[] NOT NULL DEFAULT '{}' CHECK (cardinality(allowed_embed_origins)<=100),
    updated_by uuid NOT NULL REFERENCES admin_users(id) ON DELETE RESTRICT,
    updated_at timestamptz NOT NULL,
    version bigint NOT NULL DEFAULT 1 CHECK (version>0),
    PRIMARY KEY (tenant_id,merchant_id),
    FOREIGN KEY (merchant_id,tenant_id) REFERENCES merchants(id,tenant_id) ON DELETE RESTRICT
);

CREATE TABLE merchant_project_settings_versions (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    merchant_id uuid NOT NULL,
    settings_version bigint NOT NULL CHECK (settings_version>0),
    snapshot jsonb NOT NULL CHECK (jsonb_typeof(snapshot)='object' AND pg_column_size(snapshot)<=32768),
    snapshot_hash bytea NOT NULL CHECK (octet_length(snapshot_hash)=32),
    changed_by uuid NOT NULL REFERENCES admin_users(id) ON DELETE RESTRICT,
    reason text NOT NULL CHECK (length(btrim(reason)) BETWEEN 8 AND 1000),
    created_at timestamptz NOT NULL,
    FOREIGN KEY (merchant_id,tenant_id) REFERENCES merchants(id,tenant_id) ON DELETE RESTRICT,
    UNIQUE (tenant_id,merchant_id,settings_version)
);

CREATE FUNCTION reject_merchant_settings_history_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'merchant settings history is append-only'; END $$;
CREATE TRIGGER merchant_settings_versions_immutable BEFORE UPDATE OR DELETE ON merchant_project_settings_versions FOR EACH ROW EXECUTE FUNCTION reject_merchant_settings_history_mutation();

CREATE TABLE merchant_settings_idempotency (
    tenant_id uuid NOT NULL,
    merchant_id uuid NOT NULL,
    actor_user_id uuid NOT NULL REFERENCES admin_users(id) ON DELETE RESTRICT,
    operation text NOT NULL CHECK (length(operation) BETWEEN 1 AND 100),
    idempotency_key text NOT NULL CHECK (length(idempotency_key) BETWEEN 8 AND 255),
    request_hash bytea NOT NULL CHECK (octet_length(request_hash)=32),
    resource_id uuid,
    response_status integer NOT NULL CHECK (response_status BETWEEN 200 AND 299),
    response_body jsonb NOT NULL CHECK (pg_column_size(response_body)<=65536),
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    FOREIGN KEY (merchant_id,tenant_id) REFERENCES merchants(id,tenant_id) ON DELETE RESTRICT,
    PRIMARY KEY (merchant_id,actor_user_id,operation,idempotency_key),
    CHECK (expires_at>created_at)
);
CREATE INDEX merchant_settings_idempotency_expiry_idx ON merchant_settings_idempotency(expires_at);

CREATE TABLE merchant_settings_assertion_jtis (
    tenant_id uuid NOT NULL,
    merchant_id uuid NOT NULL,
    jti uuid NOT NULL,
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz NOT NULL,
    FOREIGN KEY (merchant_id,tenant_id) REFERENCES merchants(id,tenant_id) ON DELETE RESTRICT,
    PRIMARY KEY (tenant_id,merchant_id,jti)
);
CREATE INDEX merchant_settings_assertion_expiry_idx ON merchant_settings_assertion_jtis(expires_at);

CREATE TABLE merchant_settings_audit_log (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    merchant_id uuid NOT NULL,
    sequence bigint NOT NULL CHECK (sequence>0),
    actor_user_id uuid NOT NULL REFERENCES admin_users(id) ON DELETE RESTRICT,
    session_id uuid REFERENCES admin_sessions(id) ON DELETE RESTRICT,
    approval_actor_id uuid REFERENCES admin_users(id) ON DELETE RESTRICT,
    action text NOT NULL CHECK (length(action) BETWEEN 1 AND 120),
    resource_type text NOT NULL CHECK (length(resource_type) BETWEEN 1 AND 80),
    resource_id text NOT NULL CHECK (length(resource_id) BETWEEN 1 AND 255),
    reason text NOT NULL CHECK (length(btrim(reason)) BETWEEN 1 AND 1000),
    details jsonb NOT NULL CHECK (jsonb_typeof(details)='object' AND pg_column_size(details)<=16384),
    previous_hash bytea NOT NULL CHECK (octet_length(previous_hash) IN (0,32)),
    entry_hash bytea NOT NULL CHECK (octet_length(entry_hash)=32),
    occurred_at timestamptz NOT NULL,
    FOREIGN KEY (merchant_id,tenant_id) REFERENCES merchants(id,tenant_id) ON DELETE RESTRICT,
    UNIQUE (tenant_id,merchant_id,sequence),
    UNIQUE (tenant_id,merchant_id,entry_hash)
);
CREATE INDEX merchant_settings_audit_scope_idx ON merchant_settings_audit_log(tenant_id,merchant_id,id DESC);

CREATE FUNCTION reject_merchant_settings_audit_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'merchant settings audit is append-only'; END $$;
CREATE TRIGGER merchant_settings_audit_immutable BEFORE UPDATE OR DELETE ON merchant_settings_audit_log FOR EACH ROW EXECUTE FUNCTION reject_merchant_settings_audit_mutation();

CREATE FUNCTION append_merchant_settings_audit(
  p_id uuid,p_tenant uuid,p_merchant uuid,p_actor uuid,p_session uuid,p_approval_actor uuid,
  p_action text,p_resource_type text,p_resource_id text,p_reason text,p_details jsonb,p_occurred_at timestamptz
) RETURNS bytea LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE prior bytea; next_sequence bigint; calculated bytea;
BEGIN
  PERFORM pg_advisory_xact_lock(hashtextextended(p_tenant::text||chr(31)||p_merchant::text||chr(31)||'merchant-settings-audit',0));
  SELECT sequence,entry_hash INTO next_sequence,prior FROM public.merchant_settings_audit_log
    WHERE tenant_id=p_tenant AND merchant_id=p_merchant ORDER BY sequence DESC LIMIT 1;
  next_sequence:=coalesce(next_sequence,0)+1;
  calculated:=digest(convert_to(concat_ws(chr(31),encode(coalesce(prior,''::bytea),'hex'),p_id::text,
    p_tenant::text,p_merchant::text,p_actor::text,coalesce(p_session::text,''),coalesce(p_approval_actor::text,''),
    p_action,p_resource_type,p_resource_id,p_reason,p_details::text,p_occurred_at::text),'UTF8'),'sha256');
  INSERT INTO public.merchant_settings_audit_log(id,tenant_id,merchant_id,sequence,actor_user_id,session_id,
    approval_actor_id,action,resource_type,resource_id,reason,details,previous_hash,entry_hash,occurred_at)
  VALUES(p_id,p_tenant,p_merchant,next_sequence,p_actor,p_session,p_approval_actor,p_action,p_resource_type,
    p_resource_id,p_reason,p_details,coalesce(prior,''::bytea),calculated,p_occurred_at);
  RETURN calculated;
END $$;
REVOKE ALL ON FUNCTION append_merchant_settings_audit(uuid,uuid,uuid,uuid,uuid,uuid,text,text,text,text,jsonb,timestamptz) FROM PUBLIC;

CREATE FUNCTION merchant_invitation_delivery_heartbeat(p_worker uuid)
RETURNS void LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
BEGIN
  INSERT INTO public.merchant_invitation_delivery_workers(worker_id,last_seen_at,started_at)
    VALUES(p_worker,clock_timestamp(),clock_timestamp())
    ON CONFLICT(worker_id) DO UPDATE SET last_seen_at=excluded.last_seen_at;
  DELETE FROM public.merchant_settings_assertion_jtis WHERE expires_at<clock_timestamp()-interval '24 hours';
  DELETE FROM public.merchant_invitation_delivery_workers WHERE worker_id<>p_worker AND last_seen_at<clock_timestamp()-interval '7 days';
END
$$;
REVOKE ALL ON FUNCTION merchant_invitation_delivery_heartbeat(uuid) FROM PUBLIC;

CREATE FUNCTION merchant_invitation_delivery_keys_admitted(p_key_ids text[])
RETURNS boolean LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
  SELECT cardinality(p_key_ids)>0 AND NOT EXISTS(
    SELECT 1 FROM public.merchant_invitation_delivery_jobs j
      JOIN public.merchant_member_invitations i ON i.id=j.invitation_id
     WHERE j.status IN ('ready','retry','leased') AND i.status='pending_delivery' AND i.expires_at>clock_timestamp()
       AND NOT (j.token_key_id=ANY(p_key_ids)))
$$;
REVOKE ALL ON FUNCTION merchant_invitation_delivery_keys_admitted(text[]) FROM PUBLIC;

CREATE FUNCTION claim_merchant_invitation_delivery(p_worker uuid,p_lease_seconds integer)
RETURNS TABLE(invitation_id uuid,tenant_id uuid,merchant_id uuid,email text,expires_at timestamptz,token_hash bytea,token_key_id text,lease_token uuid,attempt_count integer)
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE item record; now_at timestamptz:=clock_timestamp();
BEGIN
  IF p_lease_seconds<10 OR p_lease_seconds>300 THEN RAISE EXCEPTION 'invalid invitation delivery lease'; END IF;
  PERFORM public.merchant_invitation_delivery_heartbeat(p_worker);
  FOR item IN SELECT i.id,i.tenant_id,i.merchant_id,i.invited_by FROM public.merchant_member_invitations i
      JOIN public.merchant_invitation_delivery_jobs j ON j.invitation_id=i.id
      WHERE i.status='pending_delivery' AND i.expires_at<=now_at AND j.status IN ('ready','retry','leased') FOR UPDATE OF i,j
  LOOP
    UPDATE public.merchant_invitation_delivery_jobs SET status='expired',updated_at=now_at,completed_at=now_at,lease_owner=NULL,lease_token=NULL,lease_until=NULL,last_error_code='invitation_expired' WHERE invitation_id=item.id;
    UPDATE public.merchant_member_invitations SET status='expired',version=version+1 WHERE id=item.id;
    PERFORM public.append_merchant_settings_audit(gen_random_uuid(),item.tenant_id,item.merchant_id,item.invited_by,NULL,NULL,'invitation.delivery_expired','invitation',item.id::text,'invitation expired before notification delivery',jsonb_build_object('error_code','invitation_expired'),now_at);
  END LOOP;
  RETURN QUERY WITH candidate AS (
    SELECT j.invitation_id FROM public.merchant_invitation_delivery_jobs j
      WHERE (j.status IN ('ready','retry') AND j.next_attempt_at<=clock_timestamp()) OR (j.status='leased' AND j.lease_until<clock_timestamp())
      ORDER BY j.next_attempt_at,j.invitation_id FOR UPDATE SKIP LOCKED LIMIT 1
  ), claimed AS (
    UPDATE public.merchant_invitation_delivery_jobs j SET status='leased',lease_owner=p_worker,lease_token=gen_random_uuid(),
      lease_until=clock_timestamp()+p_lease_seconds*interval '1 second',attempt_count=attempt_count+1,updated_at=clock_timestamp()
      FROM candidate c WHERE j.invitation_id=c.invitation_id
      RETURNING j.invitation_id,j.tenant_id,j.merchant_id,j.token_key_id,j.lease_token,j.attempt_count
  ) SELECT c.invitation_id,c.tenant_id,c.merchant_id,i.email,i.expires_at,i.token_hash,c.token_key_id,c.lease_token,c.attempt_count
      FROM claimed c JOIN public.merchant_member_invitations i ON i.id=c.invitation_id AND i.status='pending_delivery';
END $$;
REVOKE ALL ON FUNCTION claim_merchant_invitation_delivery(uuid,integer) FROM PUBLIC;

CREATE FUNCTION complete_merchant_invitation_delivery(p_invitation uuid,p_lease uuid,p_provider_id text)
RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE item record; audit_id uuid:=gen_random_uuid(); now_at timestamptz:=clock_timestamp();
BEGIN
  IF p_provider_id IS NULL OR length(p_provider_id) NOT BETWEEN 1 AND 255 THEN RETURN false; END IF;
  SELECT i.tenant_id,i.merchant_id,i.invited_by,i.version INTO item FROM public.merchant_member_invitations i
    JOIN public.merchant_invitation_delivery_jobs j ON j.invitation_id=i.id
    WHERE i.id=p_invitation AND i.status='pending_delivery' AND i.expires_at>now_at AND j.status='leased' AND j.lease_token=p_lease AND j.lease_until>=now_at FOR UPDATE OF i,j;
  IF NOT FOUND THEN RETURN false; END IF;
  UPDATE public.merchant_invitation_delivery_jobs SET status='acknowledged',provider_delivery_id=p_provider_id,updated_at=now_at,completed_at=now_at,lease_owner=NULL,lease_token=NULL,lease_until=NULL,last_error_code=NULL WHERE invitation_id=p_invitation;
  UPDATE public.merchant_member_invitations SET status='active',activated_at=now_at,version=version+1 WHERE id=p_invitation AND version=item.version;
  PERFORM public.append_merchant_settings_audit(audit_id,item.tenant_id,item.merchant_id,item.invited_by,NULL,NULL,'invitation.delivery_acknowledged','invitation',p_invitation::text,'notification provider durably accepted invitation',jsonb_build_object('provider_delivery_id',p_provider_id),now_at);
  RETURN true;
END $$;
REVOKE ALL ON FUNCTION complete_merchant_invitation_delivery(uuid,uuid,text) FROM PUBLIC;

CREATE FUNCTION fail_merchant_invitation_delivery(p_invitation uuid,p_lease uuid,p_error_code text,p_max_attempts integer,p_retry_seconds integer)
RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE attempts integer; now_at timestamptz:=clock_timestamp(); terminal boolean; item record;
BEGIN
  IF p_error_code !~ '^[a-z0-9_]{1,80}$' OR p_max_attempts<1 OR p_max_attempts>20 OR p_retry_seconds<1 OR p_retry_seconds>86400 THEN RETURN false; END IF;
  SELECT attempt_count INTO attempts FROM public.merchant_invitation_delivery_jobs WHERE invitation_id=p_invitation AND status='leased' AND lease_token=p_lease AND lease_until>=now_at FOR UPDATE;
  IF NOT FOUND THEN RETURN false; END IF;
  terminal:=attempts>=p_max_attempts;
  UPDATE public.merchant_invitation_delivery_jobs SET status=CASE WHEN terminal THEN 'dead_letter' ELSE 'retry' END,
    next_attempt_at=now_at+p_retry_seconds*interval '1 second',last_error_code=p_error_code,updated_at=now_at,
    completed_at=CASE WHEN terminal THEN now_at ELSE NULL END,lease_owner=NULL,lease_token=NULL,lease_until=NULL WHERE invitation_id=p_invitation;
  IF terminal THEN
    SELECT tenant_id,merchant_id,invited_by INTO item FROM public.merchant_member_invitations WHERE id=p_invitation AND status='pending_delivery' FOR UPDATE;
    UPDATE public.merchant_member_invitations SET status='revoked',revoked_at=now_at,revoke_reason='notification delivery exhausted',version=version+1 WHERE id=p_invitation AND status='pending_delivery';
    IF FOUND THEN PERFORM public.append_merchant_settings_audit(gen_random_uuid(),item.tenant_id,item.merchant_id,item.invited_by,NULL,NULL,'invitation.delivery_dead_letter','invitation',p_invitation::text,'notification delivery attempts exhausted',jsonb_build_object('error_code',p_error_code,'attempt_count',attempts),now_at); END IF;
  END IF;
  RETURN true;
END $$;
REVOKE ALL ON FUNCTION fail_merchant_invitation_delivery(uuid,uuid,text,integer,integer) FROM PUBLIC;

CREATE FUNCTION enforce_merchant_invitation_transition() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.tenant_id<>OLD.tenant_id OR NEW.merchant_id<>OLD.merchant_id OR NEW.email<>OLD.email OR NEW.token_hash<>OLD.token_hash OR
     NEW.token_key_id<>OLD.token_key_id OR NEW.role_keys<>OLD.role_keys OR NEW.delivery_mode<>OLD.delivery_mode OR NEW.invited_by<>OLD.invited_by OR
     NEW.created_at<>OLD.created_at OR NEW.expires_at<>OLD.expires_at THEN RAISE EXCEPTION 'merchant invitation identity and grant are immutable'; END IF;
  IF NOT ((OLD.status='pending_delivery' AND NEW.status IN ('pending_delivery','active','revoked','expired')) OR
          (OLD.status='active' AND NEW.status IN ('active','accepted','revoked','expired')) OR OLD.status=NEW.status) THEN
    RAISE EXCEPTION 'invalid merchant invitation transition'; END IF;
  IF NEW.version<>OLD.version+1 THEN RAISE EXCEPTION 'merchant invitation version must advance exactly once'; END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER merchant_invitation_transition BEFORE UPDATE ON merchant_member_invitations FOR EACH ROW EXECUTE FUNCTION enforce_merchant_invitation_transition();

CREATE FUNCTION enforce_merchant_security_action_transition() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.tenant_id<>OLD.tenant_id OR NEW.merchant_id<>OLD.merchant_id OR NEW.operation<>OLD.operation OR NEW.target_member_id<>OLD.target_member_id OR
     NEW.target_version<>OLD.target_version OR NEW.desired_role_keys<>OLD.desired_role_keys OR NEW.payload_hash<>OLD.payload_hash OR
     NEW.requested_by<>OLD.requested_by OR NEW.requested_session_id<>OLD.requested_session_id OR NEW.request_reason<>OLD.request_reason OR
     NEW.requested_mfa_at<>OLD.requested_mfa_at OR NEW.created_at<>OLD.created_at OR NEW.expires_at<>OLD.expires_at THEN
    RAISE EXCEPTION 'merchant security request payload is immutable'; END IF;
  IF OLD.status<>'pending_approval' OR NEW.status NOT IN ('completed','rejected','expired','failed') THEN RAISE EXCEPTION 'invalid merchant security request transition'; END IF;
  IF NEW.version<>OLD.version+1 THEN RAISE EXCEPTION 'merchant security request version must advance exactly once'; END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER merchant_security_action_transition BEFORE UPDATE ON merchant_security_action_requests FOR EACH ROW EXECUTE FUNCTION enforce_merchant_security_action_transition();

-- Losing the last active owner is rejected at the database boundary. Role
-- replacement code inserts the replacement owner before revoking the old one.
CREATE FUNCTION guard_last_merchant_owner() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  PERFORM pg_advisory_xact_lock(hashtextextended(OLD.merchant_id::text||chr(31)||'merchant-owner-guard',0));
  IF OLD.role_key='owner' AND OLD.revoked_at IS NULL AND NEW.revoked_at IS NOT NULL AND
     NOT EXISTS (SELECT 1 FROM merchant_member_role_bindings b JOIN merchant_members m ON m.id=b.member_id
       WHERE b.merchant_id=OLD.merchant_id AND b.role_key='owner' AND b.revoked_at IS NULL AND b.id<>OLD.id AND m.status='active')
  THEN RAISE EXCEPTION 'cannot revoke the last active merchant owner'; END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER merchant_last_owner_guard BEFORE UPDATE OF revoked_at ON merchant_member_role_bindings FOR EACH ROW EXECUTE FUNCTION guard_last_merchant_owner();

CREATE FUNCTION guard_last_merchant_owner_member_status() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  PERFORM pg_advisory_xact_lock(hashtextextended(OLD.merchant_id::text||chr(31)||'merchant-owner-guard',0));
  IF OLD.status='active' AND NEW.status IN ('disabled','removed') AND
     EXISTS (SELECT 1 FROM merchant_member_role_bindings b WHERE b.member_id=OLD.id AND b.role_key='owner' AND b.revoked_at IS NULL) AND
     NOT EXISTS (SELECT 1 FROM merchant_member_role_bindings b JOIN merchant_members m ON m.id=b.member_id
       WHERE b.merchant_id=OLD.merchant_id AND b.role_key='owner' AND b.revoked_at IS NULL AND b.member_id<>OLD.id AND m.status='active')
  THEN RAISE EXCEPTION 'cannot disable or remove the last active merchant owner'; END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER merchant_last_owner_member_guard BEFORE UPDATE OF status ON merchant_members FOR EACH ROW EXECUTE FUNCTION guard_last_merchant_owner_member_status();

-- The dedicated worker calls this function. It atomically revokes every
-- active admin session for signalled identities and acknowledges the signals;
-- a crashed worker simply retries the still-unconsumed rows.
CREATE FUNCTION consume_merchant_session_revocations(p_batch integer)
RETURNS integer LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE affected integer; now_at timestamptz:=clock_timestamp();
BEGIN
  IF p_batch<1 OR p_batch>1000 THEN RAISE EXCEPTION 'invalid revocation batch'; END IF;
  WITH claimed AS (
    SELECT sequence,admin_user_id,reason FROM public.merchant_session_revocation_signals
      WHERE consumed_at IS NULL ORDER BY sequence FOR UPDATE SKIP LOCKED LIMIT p_batch
  ), revoked AS (
    UPDATE public.admin_sessions s SET revoked_at=now_at,revocation_reason=left('merchant membership: '||c.reason,255)
      FROM claimed c WHERE s.user_id=c.admin_user_id AND s.revoked_at IS NULL RETURNING s.id
  )
  UPDATE public.merchant_session_revocation_signals q SET consumed_at=now_at
    FROM claimed c WHERE q.sequence=c.sequence;
  GET DIAGNOSTICS affected=ROW_COUNT;
  RETURN affected;
END $$;
REVOKE ALL ON FUNCTION consume_merchant_session_revocations(integer) FROM PUBLIC;

-- All merchant-owned cabinet state is scoped by both tenant and merchant.
DO $$ DECLARE table_name text; BEGIN
  FOREACH table_name IN ARRAY ARRAY[
    'merchant_members','merchant_member_role_bindings','merchant_member_invitations','merchant_invitation_delivery_jobs',
    'merchant_security_action_requests','merchant_session_revocation_signals',
    'merchant_project_settings','merchant_project_settings_versions',
    'merchant_settings_idempotency','merchant_settings_assertion_jtis','merchant_settings_audit_log'
  ] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY',table_name);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY',table_name);
    EXECUTE format('CREATE POLICY %I_scope ON %I USING (tenant_id=nullif(current_setting(''app.tenant_id'',true),'''')::uuid AND merchant_id=nullif(current_setting(''app.merchant_id'',true),'''')::uuid) WITH CHECK (tenant_id=nullif(current_setting(''app.tenant_id'',true),'''')::uuid AND merchant_id=nullif(current_setting(''app.merchant_id'',true),'''')::uuid)',table_name,table_name);
  END LOOP;
END $$;

REVOKE ALL ON merchant_cabinet_permissions,merchant_cabinet_roles,merchant_cabinet_role_permissions,
  merchant_members,merchant_member_role_bindings,merchant_member_invitations,merchant_invitation_delivery_jobs,merchant_invitation_delivery_workers,merchant_security_action_requests,
  merchant_session_revocation_signals,merchant_project_settings,merchant_project_settings_versions,
  merchant_settings_idempotency,merchant_settings_assertion_jtis,merchant_settings_audit_log FROM PUBLIC;
REVOKE ALL ON SEQUENCE merchant_session_revocation_signals_sequence_seq FROM PUBLIC;

COMMIT;
