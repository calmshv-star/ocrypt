BEGIN;

-- An invited identity is deliberately inert. It has no platform role binding
-- and ordinary admin session lookup continues to require status='active'.
ALTER TABLE admin_users DROP CONSTRAINT admin_users_status_check;
ALTER TABLE admin_users ADD CONSTRAINT admin_users_status_check
  CHECK (status IN ('invited','active','disabled','locked'));

ALTER TABLE admin_login_attempts
  ADD COLUMN invitation_id uuid,
  ADD COLUMN invitation_tenant_id uuid,
  ADD COLUMN invitation_merchant_id uuid,
  ADD COLUMN expected_email text;
ALTER TABLE admin_login_attempts
  ADD CONSTRAINT admin_login_attempt_invitation_fk
  FOREIGN KEY(invitation_id,invitation_tenant_id,invitation_merchant_id)
  REFERENCES merchant_member_invitations(id,tenant_id,merchant_id) ON DELETE CASCADE;
DO $$ DECLARE item record; BEGIN
  FOR item IN SELECT conname FROM pg_constraint
    WHERE conrelid='admin_login_attempts'::regclass AND contype='c'
      AND pg_get_constraintdef(oid) LIKE '%purpose%'
  LOOP EXECUTE format('ALTER TABLE admin_login_attempts DROP CONSTRAINT %I',item.conname); END LOOP;
END $$;
ALTER TABLE admin_login_attempts
  ADD CONSTRAINT admin_login_attempt_purpose_check CHECK (purpose IN ('login','step_up','invitation')),
  ADD CONSTRAINT admin_login_attempt_shape_check CHECK (
    (purpose='login' AND expected_user_id IS NULL AND existing_session_hash IS NULL
      AND invitation_id IS NULL AND invitation_tenant_id IS NULL AND invitation_merchant_id IS NULL AND expected_email IS NULL) OR
    (purpose='step_up' AND expected_user_id IS NOT NULL AND octet_length(existing_session_hash)=32
      AND invitation_id IS NULL AND invitation_tenant_id IS NULL AND invitation_merchant_id IS NULL AND expected_email IS NULL) OR
    (purpose='invitation' AND expected_user_id IS NULL AND existing_session_hash IS NULL
      AND invitation_id IS NOT NULL AND invitation_tenant_id IS NOT NULL AND invitation_merchant_id IS NOT NULL
      AND expected_email=lower(expected_email) AND length(expected_email) BETWEEN 3 AND 320)
  );

ALTER TABLE admin_sessions
  ADD COLUMN purpose text NOT NULL DEFAULT 'admin',
  ADD COLUMN invitation_id uuid REFERENCES merchant_member_invitations(id) ON DELETE RESTRICT,
  ADD CONSTRAINT admin_session_purpose_check CHECK (purpose IN ('admin','invitation')),
  ADD CONSTRAINT admin_session_invitation_shape_check CHECK (
    (purpose='admin' AND invitation_id IS NULL) OR
    (purpose='invitation' AND invitation_id IS NOT NULL)
  );
CREATE INDEX admin_sessions_invitation_idx ON admin_sessions(invitation_id,absolute_expires_at)
  WHERE purpose='invitation' AND revoked_at IS NULL;

CREATE TABLE admin_invitation_enrollments (
  invitation_id uuid NOT NULL,
  tenant_id uuid NOT NULL,
  merchant_id uuid NOT NULL,
  user_id uuid NOT NULL REFERENCES admin_users(id) ON DELETE RESTRICT,
  session_id uuid NOT NULL REFERENCES admin_sessions(id) ON DELETE CASCADE,
  oidc_issuer text NOT NULL,
  oidc_subject text NOT NULL,
  email text NOT NULL CHECK(email=lower(email) AND length(email) BETWEEN 3 AND 320),
  status text NOT NULL CHECK(status IN ('pending','accepted','expired')),
  created_at timestamptz NOT NULL,
  expires_at timestamptz NOT NULL,
  accepted_at timestamptz,
  PRIMARY KEY(invitation_id,user_id),
  FOREIGN KEY(invitation_id,tenant_id,merchant_id)
    REFERENCES merchant_member_invitations(id,tenant_id,merchant_id) ON DELETE RESTRICT,
  CHECK(expires_at>created_at),
  CHECK((status='accepted')=(accepted_at IS NOT NULL))
);
CREATE INDEX admin_invitation_enrollment_expiry_idx
  ON admin_invitation_enrollments(expires_at) WHERE status='pending';
CREATE INDEX admin_invitation_enrollment_session_idx ON admin_invitation_enrollments(session_id,status);
ALTER TABLE admin_invitation_enrollments ENABLE ROW LEVEL SECURITY;
ALTER TABLE admin_invitation_enrollments FORCE ROW LEVEL SECURITY;
CREATE POLICY admin_invitation_enrollment_scope_policy ON admin_invitation_enrollments
  USING(tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid
    AND merchant_id=nullif(current_setting('app.merchant_id',true),'')::uuid)
  WITH CHECK(tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid
    AND merchant_id=nullif(current_setting('app.merchant_id',true),'')::uuid);

-- Session authentication starts with only a bearer hash, before tenant GUCs
-- can safely be known. This narrow definer lookup derives scope internally and
-- returns only the single matching, live invitation capability.
CREATE FUNCTION lookup_admin_invitation_session(requested_hash bytea,requested_at timestamptz)
RETURNS TABLE(
  session_id uuid,session_hash bytea,csrf_hash bytea,user_id uuid,session_issuer text,session_subject text,
  purpose text,invitation_id uuid,acr text,amr text[],created_at timestamptz,last_seen_at timestamptz,
  idle_expires_at timestamptz,absolute_expires_at timestamptz,step_up_until timestamptz,rotated_at timestamptz,
  revoked_at timestamptz,identity_issuer text,identity_subject text,display_name text,email text,identity_status text
)
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path=pg_catalog,public SET row_security=off AS $$
  SELECT s.id,s.session_hash,s.csrf_hash,s.user_id,s.oidc_issuer,s.oidc_subject,s.purpose,s.invitation_id,
    s.acr,s.amr,s.created_at,s.last_seen_at,s.idle_expires_at,s.absolute_expires_at,s.step_up_until,s.rotated_at,s.revoked_at,
    u.oidc_issuer,u.oidc_subject,u.display_name,coalesce(u.email,''),u.status
  FROM public.admin_sessions s
  JOIN public.admin_users u ON u.id=s.user_id AND u.oidc_issuer=s.oidc_issuer AND u.oidc_subject=s.oidc_subject
  JOIN public.admin_invitation_enrollments e ON e.invitation_id=s.invitation_id AND e.user_id=s.user_id
    AND e.session_id=s.id AND e.status='pending' AND e.expires_at>requested_at
  JOIN public.merchant_member_invitations i ON i.id=e.invitation_id AND i.tenant_id=e.tenant_id
    AND i.merchant_id=e.merchant_id AND i.status='active' AND i.expires_at>requested_at AND i.email=e.email
  WHERE octet_length(requested_hash)=32 AND s.session_hash=requested_hash AND s.purpose='invitation'
    AND s.revoked_at IS NULL AND s.idle_expires_at>requested_at AND s.absolute_expires_at>requested_at
    AND u.status IN ('invited','active') AND lower(u.email)=e.email
  LIMIT 1
$$;
REVOKE ALL ON FUNCTION lookup_admin_invitation_session(bytea,timestamptz) FROM PUBLIC;

-- Existing callers still use the same SHA-256 lookup boundary. The additional
-- columns bind the OIDC attempt and later restricted session without storing
-- the raw credential.
DROP FUNCTION lookup_merchant_invitation(bytea);
CREATE FUNCTION lookup_merchant_invitation(requested_hash bytea)
RETURNS TABLE(invitation_id uuid,tenant_id uuid,merchant_id uuid,email text,expires_at timestamptz)
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path=pg_catalog,public SET row_security=off AS $$
  SELECT i.id,i.tenant_id,i.merchant_id,i.email,i.expires_at
    FROM public.merchant_member_invitations i
    JOIN public.tenants t ON t.id=i.tenant_id AND t.status='active'
    JOIN public.merchants m ON m.id=i.merchant_id AND m.tenant_id=i.tenant_id AND m.status='active'
   WHERE octet_length(requested_hash)=32 AND i.token_hash=requested_hash
     AND i.status='active' AND i.expires_at>clock_timestamp()
   LIMIT 1
$$;
REVOKE ALL ON FUNCTION lookup_merchant_invitation(bytea) FROM PUBLIC;

CREATE FUNCTION lookup_merchant_invitation_for_session(
  requested_hash bytea,requested_user uuid,requested_session uuid,
  requested_issuer text,requested_subject text,requested_email text
)
RETURNS TABLE(invitation_id uuid,tenant_id uuid,merchant_id uuid,email text,status text,expires_at timestamptz)
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path=pg_catalog,public SET row_security=off AS $$
  SELECT i.id,i.tenant_id,i.merchant_id,i.email,i.status,i.expires_at
  FROM public.merchant_member_invitations i
  JOIN public.tenants t ON t.id=i.tenant_id AND t.status='active'
  JOIN public.merchants m ON m.id=i.merchant_id AND m.tenant_id=i.tenant_id AND m.status='active'
  WHERE octet_length(requested_hash)=32 AND i.token_hash=requested_hash
    AND i.email=lower(btrim(requested_email))
    AND (
      (i.status='active' AND i.expires_at>clock_timestamp()) OR
      (i.status='accepted' AND EXISTS(
        SELECT 1 FROM public.admin_invitation_enrollments e
        WHERE e.invitation_id=i.id AND e.tenant_id=i.tenant_id AND e.merchant_id=i.merchant_id
          AND e.user_id=requested_user AND e.session_id=requested_session AND e.status='accepted'
          AND e.oidc_issuer=requested_issuer AND e.oidc_subject=requested_subject AND e.email=i.email
      ))
    )
  LIMIT 1
$$;
REVOKE ALL ON FUNCTION lookup_merchant_invitation_for_session(bytea,uuid,uuid,text,text,text) FROM PUBLIC;

CREATE FUNCTION ensure_admin_invitation_identity(
  requested_invitation uuid,requested_tenant uuid,requested_merchant uuid,requested_email text,
  requested_issuer text,requested_subject text,requested_display_name text,
  proposed_user uuid,requested_at timestamptz
) RETURNS TABLE(user_id uuid,oidc_issuer text,oidc_subject text,display_name text,email text,status text,created boolean)
LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE found public.admin_users%ROWTYPE; normalized_email text := lower(btrim(requested_email));
BEGIN
  IF requested_issuer NOT LIKE 'https://%' OR right(requested_issuer,1)='/' OR
     length(requested_subject) NOT BETWEEN 1 AND 255 OR
     length(requested_display_name) NOT BETWEEN 1 AND 255 THEN
    RETURN;
  END IF;
  PERFORM 1 FROM public.merchant_member_invitations i
   WHERE i.id=requested_invitation AND i.tenant_id=requested_tenant AND i.merchant_id=requested_merchant
     AND i.email=normalized_email AND i.status='active' AND i.expires_at>clock_timestamp()
   FOR UPDATE;
  IF NOT FOUND THEN RETURN; END IF;
  PERFORM pg_advisory_xact_lock(hashtextextended(requested_issuer||chr(31)||requested_subject,0));
  SELECT * INTO found FROM public.admin_users u
   WHERE u.oidc_issuer=requested_issuer AND u.oidc_subject=requested_subject FOR UPDATE;
  IF NOT FOUND THEN
    INSERT INTO public.admin_users(id,oidc_issuer,oidc_subject,display_name,email,status,created_at,updated_at,version)
      VALUES(proposed_user,requested_issuer,requested_subject,requested_display_name,normalized_email,'invited',requested_at,requested_at,1)
      RETURNING * INTO found;
    created := true;
  ELSE
    IF found.status NOT IN ('active','invited') OR lower(coalesce(found.email,''))<>normalized_email THEN RETURN; END IF;
    created := false;
  END IF;
  user_id:=found.id; oidc_issuer:=found.oidc_issuer; oidc_subject:=found.oidc_subject;
  display_name:=found.display_name; email:=lower(found.email); status:=found.status;
  RETURN NEXT;
END $$;
REVOKE ALL ON FUNCTION ensure_admin_invitation_identity(uuid,uuid,uuid,text,text,text,text,uuid,timestamptz) FROM PUBLIC;

-- Called inside the merchant-settings SERIALIZABLE acceptance transaction.
-- Membership, invitation consumption, identity activation and session promotion
-- therefore commit or roll back together in the single shared PostgreSQL DB.
CREATE FUNCTION activate_admin_invitation_identity(
  requested_invitation uuid,requested_user uuid,requested_session uuid,
  requested_issuer text,requested_subject text,requested_email text,requested_at timestamptz
) RETURNS boolean
LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE enrollment public.admin_invitation_enrollments%ROWTYPE; identity public.admin_users%ROWTYPE;
  invite_tenant uuid; invite_merchant uuid; session_expiry timestamptz;
BEGIN
  SELECT * INTO identity FROM public.admin_users u WHERE u.id=requested_user FOR UPDATE;
  IF NOT FOUND OR identity.status NOT IN ('invited','active') OR
     identity.oidc_issuer<>requested_issuer OR identity.oidc_subject<>requested_subject OR
     lower(coalesce(identity.email,''))<>lower(btrim(requested_email)) THEN RETURN false; END IF;
  SELECT i.tenant_id,i.merchant_id INTO invite_tenant,invite_merchant
    FROM public.merchant_member_invitations i
    JOIN public.merchant_members m ON m.id=i.accepted_by_member_id
      AND m.tenant_id=i.tenant_id AND m.merchant_id=i.merchant_id
    WHERE i.id=requested_invitation AND i.status='accepted' AND i.accepted_oidc_issuer=requested_issuer
      AND i.accepted_oidc_subject=requested_subject AND m.admin_user_id=requested_user AND m.status='active'
  ;
  IF NOT FOUND THEN RETURN false; END IF;
  SELECT * INTO enrollment FROM public.admin_invitation_enrollments e
   WHERE e.invitation_id=requested_invitation AND e.user_id=requested_user
     AND e.session_id=requested_session AND e.status='pending' AND e.expires_at>requested_at
     AND e.oidc_issuer=requested_issuer AND e.oidc_subject=requested_subject
     AND e.email=lower(btrim(requested_email)) FOR UPDATE;
  IF FOUND THEN
    UPDATE public.admin_users SET status='active',updated_at=requested_at,version=version+1
     WHERE id=requested_user AND status='invited';
    UPDATE public.admin_sessions SET purpose='admin',invitation_id=NULL
     WHERE id=requested_session AND user_id=requested_user AND purpose='invitation'
       AND invitation_id=requested_invitation AND revoked_at IS NULL AND absolute_expires_at>requested_at;
    IF NOT FOUND THEN RETURN false; END IF;
    UPDATE public.admin_invitation_enrollments SET status='accepted',accepted_at=requested_at
     WHERE invitation_id=requested_invitation AND user_id=requested_user AND status='pending';
  ELSE
    IF identity.status<>'active' THEN RETURN false; END IF;
    SELECT s.absolute_expires_at INTO session_expiry FROM public.admin_sessions s
      WHERE s.id=requested_session AND s.user_id=requested_user AND s.purpose='admin'
        AND s.invitation_id IS NULL AND s.oidc_issuer=requested_issuer AND s.oidc_subject=requested_subject
        AND s.revoked_at IS NULL AND s.absolute_expires_at>requested_at FOR UPDATE;
    IF NOT FOUND THEN RETURN false; END IF;
    UPDATE public.admin_sessions SET revoked_at=requested_at,revocation_reason='invitation_session_replaced'
      WHERE id IN(SELECT e.session_id FROM public.admin_invitation_enrollments e
        WHERE e.invitation_id=requested_invitation AND e.user_id=requested_user AND e.status='pending')
        AND id<>requested_session AND purpose='invitation' AND revoked_at IS NULL;
    INSERT INTO public.admin_invitation_enrollments(invitation_id,tenant_id,merchant_id,user_id,session_id,
      oidc_issuer,oidc_subject,email,status,created_at,expires_at,accepted_at)
      VALUES(requested_invitation,invite_tenant,invite_merchant,requested_user,requested_session,
        requested_issuer,requested_subject,lower(btrim(requested_email)),'accepted',requested_at,session_expiry,requested_at)
      ON CONFLICT(invitation_id,user_id) DO UPDATE SET session_id=EXCLUDED.session_id,status='accepted',
        created_at=EXCLUDED.created_at,expires_at=EXCLUDED.expires_at,accepted_at=EXCLUDED.accepted_at
      WHERE admin_invitation_enrollments.status IN ('pending','expired');
  END IF;
  PERFORM public.append_admin_audit(gen_random_uuid(),invite_tenant,invite_merchant,
    requested_user,requested_session,'admin.invitation.identity.activated','merchant_invitation',
    requested_invitation::text,requested_session::text,'merchant invitation accepted',NULL,NULL,
    '{"purpose":"invitation"}'::jsonb,NULL,NULL,requested_at);
  RETURN true;
END $$;
REVOKE ALL ON FUNCTION activate_admin_invitation_identity(uuid,uuid,uuid,text,text,text,timestamptz) FROM PUBLIC;

-- Expiry is fail-closed. The audited identity row is retained, but every stale
-- invitation capability is revoked. Row locks serialize this with activation,
-- so an identity activated by a concurrent accept is never expired or removed.
CREATE FUNCTION cleanup_expired_admin_invitation_enrollments(batch_size integer DEFAULT 100)
RETURNS integer
LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE item record; cleaned integer:=0;
BEGIN
  IF batch_size NOT BETWEEN 1 AND 1000 THEN RAISE EXCEPTION 'invalid cleanup batch'; END IF;
  FOR item IN SELECT e.invitation_id,e.user_id,e.session_id
    FROM public.admin_invitation_enrollments e
    WHERE e.status='pending' AND e.expires_at<=clock_timestamp()
    ORDER BY e.expires_at FOR UPDATE SKIP LOCKED LIMIT batch_size
  LOOP
    UPDATE public.admin_invitation_enrollments SET status='expired'
      WHERE invitation_id=item.invitation_id AND user_id=item.user_id AND status='pending';
    IF FOUND THEN
      UPDATE public.admin_sessions SET revoked_at=clock_timestamp(),revocation_reason='invitation_enrollment_expired'
       WHERE id=item.session_id AND purpose='invitation' AND revoked_at IS NULL;
      cleaned:=cleaned+1;
    END IF;
  END LOOP;
  RETURN cleaned;
END $$;
REVOKE ALL ON FUNCTION cleanup_expired_admin_invitation_enrollments(integer) FROM PUBLIC;

DO $$ BEGIN
  IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='merchant_admin_runtime') THEN
    GRANT SELECT,INSERT,UPDATE ON admin_invitation_enrollments TO merchant_admin_runtime;
    GRANT EXECUTE ON FUNCTION lookup_merchant_invitation(bytea),
      lookup_merchant_invitation_for_session(bytea,uuid,uuid,text,text,text),
      ensure_admin_invitation_identity(uuid,uuid,uuid,text,text,text,text,uuid,timestamptz),
      lookup_admin_invitation_session(bytea,timestamptz),
      cleanup_expired_admin_invitation_enrollments(integer) TO merchant_admin_runtime;
  END IF;
  IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='merchant_settings_api_runtime') THEN
    GRANT EXECUTE ON FUNCTION activate_admin_invitation_identity(uuid,uuid,uuid,text,text,text,timestamptz)
      TO merchant_settings_api_runtime;
  END IF;
END $$;

COMMIT;
