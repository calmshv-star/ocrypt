BEGIN;

UPDATE admin_sessions SET revoked_at=coalesce(revoked_at,clock_timestamp()),
  revocation_reason=coalesce(revocation_reason,'invitation_enrollment_rollback')
  WHERE purpose='invitation';
UPDATE admin_users SET status='disabled',updated_at=clock_timestamp(),version=version+1 WHERE status='invited';

DROP FUNCTION IF EXISTS cleanup_expired_admin_invitation_enrollments(integer);
DROP FUNCTION IF EXISTS activate_admin_invitation_identity(uuid,uuid,uuid,text,text,text,timestamptz);
DROP FUNCTION IF EXISTS ensure_admin_invitation_identity(uuid,uuid,uuid,text,text,text,text,uuid,timestamptz);
DROP FUNCTION IF EXISTS lookup_admin_invitation_session(bytea,timestamptz);
DROP FUNCTION IF EXISTS lookup_merchant_invitation_for_session(bytea,uuid,uuid,text,text,text);
DROP FUNCTION IF EXISTS lookup_merchant_invitation(bytea);
CREATE FUNCTION lookup_merchant_invitation(requested_hash bytea)
RETURNS TABLE(tenant_id uuid,merchant_id uuid)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
  SELECT i.tenant_id,i.merchant_id FROM public.merchant_member_invitations i
    JOIN public.tenants t ON t.id=i.tenant_id AND t.status='active'
    JOIN public.merchants m ON m.id=i.merchant_id AND m.tenant_id=i.tenant_id AND m.status='active'
   WHERE i.token_hash=requested_hash AND i.status='active' AND i.expires_at>clock_timestamp() LIMIT 1
$$;
REVOKE ALL ON FUNCTION lookup_merchant_invitation(bytea) FROM PUBLIC;
DO $$ BEGIN
  IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='merchant_admin_runtime') THEN
    GRANT EXECUTE ON FUNCTION lookup_merchant_invitation(bytea) TO merchant_admin_runtime;
  END IF;
END $$;

DROP TABLE IF EXISTS admin_invitation_enrollments;
DROP INDEX IF EXISTS admin_sessions_invitation_idx;
ALTER TABLE admin_sessions DROP CONSTRAINT admin_session_invitation_shape_check,
  DROP CONSTRAINT admin_session_purpose_check,DROP COLUMN invitation_id,DROP COLUMN purpose;
ALTER TABLE admin_login_attempts DROP CONSTRAINT admin_login_attempt_shape_check,
  DROP CONSTRAINT admin_login_attempt_purpose_check,DROP CONSTRAINT admin_login_attempt_invitation_fk,
  DROP COLUMN expected_email,DROP COLUMN invitation_merchant_id,DROP COLUMN invitation_tenant_id,DROP COLUMN invitation_id;
ALTER TABLE admin_login_attempts ADD CONSTRAINT admin_login_attempts_purpose_check CHECK(purpose IN ('login','step_up')),
  ADD CONSTRAINT admin_login_attempts_check1 CHECK(
    (purpose='login' AND expected_user_id IS NULL AND existing_session_hash IS NULL) OR
    (purpose='step_up' AND expected_user_id IS NOT NULL AND octet_length(existing_session_hash)=32));
ALTER TABLE admin_users DROP CONSTRAINT admin_users_status_check;
ALTER TABLE admin_users ADD CONSTRAINT admin_users_status_check CHECK(status IN ('active','disabled','locked'));

COMMIT;
