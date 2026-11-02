BEGIN;

DROP FUNCTION IF EXISTS register_self_service_merchant(uuid,text,text,text,text,uuid,uuid,uuid,uuid,timestamptz);
DROP FUNCTION IF EXISTS create_admin_signup_request(uuid,text,text,text,text,text,timestamptz,timestamptz);
DROP TABLE IF EXISTS merchant_signups;
DELETE FROM admin_login_attempts WHERE purpose='signup';
ALTER TABLE admin_login_attempts DROP CONSTRAINT admin_login_attempt_shape_check;
ALTER TABLE admin_login_attempts DROP CONSTRAINT admin_login_attempt_purpose_check;
ALTER TABLE admin_login_attempts DROP COLUMN signup_request_id;
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
DROP TABLE IF EXISTS admin_signup_requests;
DROP TABLE IF EXISTS signup_business_types;
DROP TABLE IF EXISTS signup_blocked_countries;
DELETE FROM admin_role_bindings WHERE role_key='merchant_owner';
DELETE FROM admin_role_permissions WHERE role_key='merchant_owner';
DELETE FROM admin_roles WHERE role_key='merchant_owner';
ALTER TABLE admin_roles DROP CONSTRAINT admin_roles_role_key_check;
ALTER TABLE admin_roles ADD CONSTRAINT admin_roles_role_key_check CHECK (role_key IN (
  'support_read_only','payment_operator','senior_approver','treasury_operator','security_admin','auditor'));
DELETE FROM admin_role_permissions WHERE permission_key IN ('wallets:read','wallets:write');
DELETE FROM admin_permissions WHERE permission_key IN ('wallets:read','wallets:write');

COMMIT;
