BEGIN;

-- Self-service registration. A signup request is screened before the OIDC
-- redirect and consumed exactly once in the callback, where one transaction
-- creates the tenant, a test-mode merchant, the active owner identity, its
-- merchant membership and merchant-scoped cabinet rights. Operator-managed
-- tenants and invitations are unchanged.

-- Merchant-scoped wallet rights, so a merchant owner can manage its own
-- watch-only receiving addresses without platform infrastructure access.
INSERT INTO admin_permissions(permission_key,description) VALUES
  ('wallets:read','Read the merchant''s own watch-only receiving wallets'),
  ('wallets:write','Replace or import the merchant''s own watch-only receiving addresses');

ALTER TABLE admin_roles DROP CONSTRAINT admin_roles_role_key_check;
ALTER TABLE admin_roles ADD CONSTRAINT admin_roles_role_key_check CHECK (role_key IN (
  'support_read_only','payment_operator','senior_approver','treasury_operator','security_admin','auditor','merchant_owner'));
INSERT INTO admin_roles(role_key,description) VALUES ('merchant_owner','Self-service merchant owner, scoped to one merchant');
-- merchant_owner is projected from an active owner membership of a
-- self-service merchant (list_current_admin_self_service_owner_grants) and is
-- never stored as a binding, so removing the owner or disabling the merchant
-- or tenant revokes it at the next session load.
ALTER TABLE admin_role_bindings ADD CONSTRAINT admin_role_binding_not_merchant_owner CHECK (role_key<>'merchant_owner');
INSERT INTO admin_role_permissions(role_key,permission_key)
SELECT 'merchant_owner',permission_key FROM unnest(ARRAY[
  'dashboard:read','payments:read','unmatched:read','unmatched:claim','resolution:request',
  'webhooks:read','webhooks:replay','reconciliation:read','checkout:write',
  'payment_links:read','payment_links:write','matching_policy:read',
  'webhook_settings:read','webhook_settings:write','webhook_settings:rotate','webhook_settings:disable',
  'api_clients:read','api_clients:write','api_clients:rotate','api_clients:revoke',
  'audit:read','wallets:read','wallets:write']) AS permission_key;

-- Countries where registration is closed. Comprehensive sanctions plus Russia
-- until a legal opinion on Federal Law 259-FZ (product specification).
CREATE TABLE signup_blocked_countries (
  country char(2) PRIMARY KEY CHECK (country ~ '^[A-Z]{2}$'),
  reason text NOT NULL CHECK (length(btrim(reason)) BETWEEN 3 AND 200)
);
INSERT INTO signup_blocked_countries(country,reason) VALUES
  ('CU','comprehensive sanctions'),('IR','comprehensive sanctions'),
  ('KP','comprehensive sanctions'),('SY','comprehensive sanctions'),
  ('RU','closed pending a legal opinion on Federal Law 259-FZ');

-- REG-5: disposable mailbox providers. A domain also blocks its subdomains.
-- Operators extend this table; the seed lists widely known providers.
CREATE TABLE signup_blocked_email_domains (
  domain text PRIMARY KEY CHECK (domain=lower(domain) AND domain ~ '^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$' AND length(domain)<=253)
);
INSERT INTO signup_blocked_email_domains(domain) VALUES
  ('10minutemail.com'),('20minutemail.com'),('33mail.com'),('dispostable.com'),('dropmail.me'),
  ('emailondeck.com'),('fakeinbox.com'),('getairmail.com'),('getnada.com'),('guerrillamail.biz'),
  ('guerrillamail.com'),('guerrillamail.de'),('guerrillamail.info'),('guerrillamail.net'),('guerrillamail.org'),
  ('guerrillamailblock.com'),('harakirimail.com'),('mail-temp.com'),('mailcatch.com'),('maildrop.cc'),
  ('mailinator.com'),('mailinator.net'),('mailnesia.com'),('mintemail.com'),('moakt.com'),
  ('mohmal.com'),('mytemp.email'),('sharklasers.com'),('spambox.us'),('spamgourmet.com'),
  ('temp-mail.io'),('temp-mail.org'),('tempail.com'),('tempmail.dev'),('tempmail.net'),
  ('tempmailo.com'),('tempr.email'),('throwawaymail.com'),('tmpmail.net'),('tmpmail.org'),
  ('trashmail.com'),('trashmail.de'),('trashmail.net'),('yopmail.com'),('yopmail.fr'),('yopmail.net');

CREATE TABLE signup_business_types (
  business_type text PRIMARY KEY CHECK (business_type ~ '^[a-z][a-z_]{1,39}$'),
  allowed boolean NOT NULL,
  description text NOT NULL
);
INSERT INTO signup_business_types(business_type,allowed,description) VALUES
  ('ecommerce',true,'Online store selling physical goods'),
  ('digital_goods',true,'Digital goods, licences and downloads'),
  ('saas',true,'Software and subscriptions'),
  ('hosting',true,'Hosting, VPN and infrastructure services'),
  ('services',true,'Professional and online services'),
  ('education',true,'Courses and education'),
  ('media',true,'Media, content and creators'),
  ('marketplace',true,'Marketplace or platform for other sellers'),
  ('games',true,'Games and in-game items without gambling'),
  ('nonprofit',true,'Donations and non-profit organisations'),
  ('gambling',false,'Gambling, betting and casinos'),
  ('adult',false,'Adult content and services'),
  ('weapons',false,'Weapons, ammunition and explosives'),
  ('drugs',false,'Drugs and controlled substances'),
  ('financial_services',false,'Money transmission, exchange or custody of others'' funds'),
  ('mlm',false,'Pyramid, MLM and investment schemes');

CREATE TABLE admin_signup_requests (
  id uuid PRIMARY KEY,
  company_name text NOT NULL CHECK (company_name=btrim(company_name) AND length(company_name) BETWEEN 2 AND 120),
  country char(2) NOT NULL CHECK (country ~ '^[A-Z]{2}$'),
  business_type text NOT NULL REFERENCES signup_business_types(business_type) ON DELETE RESTRICT,
  website text CHECK (website IS NULL OR (website ~ '^https://[^[:space:]/?#]+(/[^[:space:]]*)?$' AND length(website)<=255)),
  terms_version text NOT NULL CHECK (length(terms_version) BETWEEN 1 AND 64),
  created_at timestamptz NOT NULL,
  expires_at timestamptz NOT NULL,
  consumed_at timestamptz,
  CHECK (expires_at>created_at AND expires_at<=created_at+interval '1 hour')
);
CREATE INDEX admin_signup_requests_expiry_idx ON admin_signup_requests(expires_at) WHERE consumed_at IS NULL;

ALTER TABLE admin_login_attempts ADD COLUMN signup_request_id uuid REFERENCES admin_signup_requests(id) ON DELETE CASCADE;
ALTER TABLE admin_login_attempts DROP CONSTRAINT admin_login_attempt_purpose_check;
ALTER TABLE admin_login_attempts DROP CONSTRAINT admin_login_attempt_shape_check;
-- A new registrant creates the IdP account, verifies the email and enrolls
-- MFA inside one signup attempt, so it may live up to one hour.
ALTER TABLE admin_login_attempts DROP CONSTRAINT admin_login_attempts_check;
ALTER TABLE admin_login_attempts
  ADD CONSTRAINT admin_login_attempts_check CHECK (expires_at>created_at
    AND expires_at<=created_at+CASE WHEN purpose='signup' THEN interval '1 hour' ELSE interval '15 minutes' END),
  ADD CONSTRAINT admin_login_attempt_purpose_check CHECK (purpose IN ('login','step_up','invitation','signup')),
  ADD CONSTRAINT admin_login_attempt_shape_check CHECK (
    (purpose='login' AND expected_user_id IS NULL AND existing_session_hash IS NULL
      AND invitation_id IS NULL AND invitation_tenant_id IS NULL AND invitation_merchant_id IS NULL AND expected_email IS NULL
      AND signup_request_id IS NULL) OR
    (purpose='step_up' AND expected_user_id IS NOT NULL AND octet_length(existing_session_hash)=32
      AND invitation_id IS NULL AND invitation_tenant_id IS NULL AND invitation_merchant_id IS NULL AND expected_email IS NULL
      AND signup_request_id IS NULL) OR
    (purpose='invitation' AND expected_user_id IS NULL AND existing_session_hash IS NULL
      AND invitation_id IS NOT NULL AND invitation_tenant_id IS NOT NULL AND invitation_merchant_id IS NOT NULL
      AND expected_email=lower(expected_email) AND length(expected_email) BETWEEN 3 AND 320
      AND signup_request_id IS NULL) OR
    (purpose='signup' AND expected_user_id IS NULL AND existing_session_hash IS NULL
      AND invitation_id IS NULL AND invitation_tenant_id IS NULL AND invitation_merchant_id IS NULL AND expected_email IS NULL
      AND signup_request_id IS NOT NULL)
  );

CREATE TABLE merchant_signups (
  merchant_id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  admin_user_id uuid NOT NULL REFERENCES admin_users(id) ON DELETE RESTRICT,
  request_id uuid NOT NULL UNIQUE REFERENCES admin_signup_requests(id) ON DELETE RESTRICT,
  country char(2) NOT NULL,
  business_type text NOT NULL REFERENCES signup_business_types(business_type) ON DELETE RESTRICT,
  website text,
  terms_version text NOT NULL,
  terms_accepted_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL,
  FOREIGN KEY (merchant_id,tenant_id) REFERENCES merchants(id,tenant_id) ON DELETE RESTRICT
);

-- Screens and stores a signup request before the OIDC redirect. Returns false
-- instead of raising, so the caller cannot learn which rule rejected it.
CREATE FUNCTION create_admin_signup_request(
  p_id uuid,p_company text,p_country text,p_business text,p_website text,p_terms text,
  p_created timestamptz,p_expires timestamptz
) RETURNS boolean
LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,public AS $$
DECLARE company text := btrim(coalesce(p_company,''));
BEGIN
  -- Anonymous callers write here, so expired unconsumed requests (and their
  -- login attempts, by cascade) are purged in small batches on every call.
  DELETE FROM public.admin_signup_requests r WHERE r.id IN (
    SELECT x.id FROM public.admin_signup_requests x
     WHERE x.consumed_at IS NULL AND x.expires_at<p_created
     ORDER BY x.expires_at LIMIT 100 FOR UPDATE SKIP LOCKED);
  IF length(company) NOT BETWEEN 2 AND 120 OR coalesce(p_country,'') !~ '^[A-Z]{2}$'
     OR company ~ '[[:cntrl:]\u00AD\u061C\u180E\u200B-\u200F\u202A-\u202E\u2060-\u206F\uFEFF]'
     OR EXISTS (SELECT 1 FROM public.signup_blocked_countries c WHERE c.country=p_country)
     OR NOT EXISTS (SELECT 1 FROM public.signup_business_types b WHERE b.business_type=p_business AND b.allowed)
     OR (p_website IS NOT NULL AND (p_website !~ '^https://[^[:space:]/?#]+(/[^[:space:]]*)?$' OR length(p_website)>255))
     OR length(coalesce(p_terms,'')) NOT BETWEEN 1 AND 64
     OR p_expires<=p_created OR p_expires>p_created+interval '1 hour' THEN
    RETURN false;
  END IF;
  INSERT INTO public.admin_signup_requests(id,company_name,country,business_type,website,terms_version,created_at,expires_at)
    VALUES(p_id,company,p_country,p_business,p_website,p_terms,p_created,p_expires);
  RETURN true;
END $$;
REVOKE ALL ON FUNCTION create_admin_signup_request(uuid,text,text,text,text,text,timestamptz,timestamptz) FROM PUBLIC;

-- Consumes one screened request after OIDC (verified email and MFA are
-- checked by the caller). A returning identity is not given a second tenant:
-- it gets its existing merchant back with created=false. Returns no row when
-- the request is unknown, consumed, expired or the identity is not usable.
CREATE FUNCTION register_self_service_merchant(
  p_request uuid,p_issuer text,p_subject text,p_email text,p_display_name text,
  p_user uuid,p_tenant uuid,p_merchant uuid,p_member uuid,p_at timestamptz
) RETURNS TABLE(user_id uuid,tenant_id uuid,merchant_id uuid,created boolean)
LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE req public.admin_signup_requests%ROWTYPE; existing public.admin_users%ROWTYPE;
  normalized_email text := lower(btrim(coalesce(p_email,'')));
  display text := left(btrim(coalesce(p_display_name,'')),255);
BEGIN
  IF p_issuer NOT LIKE 'https://%' OR right(p_issuer,1)='/' OR length(coalesce(p_subject,'')) NOT BETWEEN 1 AND 255
     OR length(normalized_email) NOT BETWEEN 3 AND 320 OR normalized_email NOT LIKE '%_@_%._%' THEN
    RETURN;
  END IF;
  IF display='' THEN display := normalized_email; END IF;
  -- A disposable mailbox neither registers nor consumes the request.
  IF EXISTS (SELECT 1 FROM public.signup_blocked_email_domains d
              WHERE split_part(normalized_email,'@',2)=d.domain
                 OR split_part(normalized_email,'@',2) LIKE '%.'||d.domain) THEN
    RETURN;
  END IF;
  SELECT * INTO req FROM public.admin_signup_requests r
   WHERE r.id=p_request AND r.consumed_at IS NULL AND r.expires_at>p_at FOR UPDATE;
  IF NOT FOUND THEN RETURN; END IF;
  IF EXISTS (SELECT 1 FROM public.signup_blocked_countries c WHERE c.country=req.country)
     OR NOT EXISTS (SELECT 1 FROM public.signup_business_types b WHERE b.business_type=req.business_type AND b.allowed) THEN
    RETURN;
  END IF;
  UPDATE public.admin_signup_requests SET consumed_at=p_at WHERE id=req.id;
  PERFORM pg_advisory_xact_lock(hashtextextended(p_issuer||chr(31)||p_subject,0));
  SELECT * INTO existing FROM public.admin_users u WHERE u.oidc_issuer=p_issuer AND u.oidc_subject=p_subject FOR UPDATE;
  IF FOUND THEN
    IF existing.status<>'active' OR lower(coalesce(existing.email,''))<>normalized_email THEN RETURN; END IF;
    SELECT m.tenant_id,m.merchant_id INTO tenant_id,merchant_id FROM public.merchant_members m
     WHERE m.admin_user_id=existing.id AND m.status='active' ORDER BY m.joined_at LIMIT 1;
    user_id := existing.id; created := false;
    IF tenant_id IS NOT NULL THEN
      PERFORM public.append_admin_audit(gen_random_uuid(),tenant_id,merchant_id,existing.id,NULL,
        'admin.self_service.returning_identity','merchant',merchant_id::text,req.id::text,'signup by an existing identity',NULL,NULL,
        '{}'::jsonb,NULL,NULL,p_at);
    END IF;
    RETURN NEXT;
    RETURN;
  END IF;
  INSERT INTO public.tenants(id,public_id,name,status,created_at,updated_at)
    VALUES(p_tenant,'tn_'||replace(p_tenant::text,'-',''),req.company_name,'active',p_at,p_at);
  INSERT INTO public.merchants(id,tenant_id,code,display_name,environment,settlement_currency,status,created_at,updated_at)
    VALUES(p_merchant,p_tenant,'main',req.company_name,'test','USD','active',p_at,p_at);
  INSERT INTO public.admin_users(id,oidc_issuer,oidc_subject,display_name,email,status,created_at,updated_at,version)
    VALUES(p_user,p_issuer,p_subject,display,normalized_email,'active',p_at,p_at,1);
  INSERT INTO public.merchant_members(id,tenant_id,merchant_id,admin_user_id,oidc_issuer,oidc_subject,email,display_name,status,joined_at,updated_at)
    VALUES(p_member,p_tenant,p_merchant,p_user,p_issuer,p_subject,normalized_email,display,'active',p_at,p_at);
  INSERT INTO public.merchant_member_role_bindings(id,tenant_id,merchant_id,member_id,role_key,granted_by,granted_at,reason)
    VALUES(gen_random_uuid(),p_tenant,p_merchant,p_member,'owner',p_user,p_at,'self-service registration owner');
  INSERT INTO public.merchant_signups(merchant_id,tenant_id,admin_user_id,request_id,country,business_type,website,terms_version,terms_accepted_at,created_at)
    VALUES(p_merchant,p_tenant,p_user,req.id,req.country,req.business_type,req.website,req.terms_version,req.created_at,p_at);
  PERFORM public.append_admin_audit(gen_random_uuid(),p_tenant,p_merchant,p_user,NULL,
    'admin.self_service.registered','merchant',p_merchant::text,req.id::text,'self-service registration',NULL,NULL,
    jsonb_build_object('country',req.country,'business_type',req.business_type,'terms_version',req.terms_version),NULL,NULL,p_at);
  user_id := p_user; tenant_id := p_tenant; merchant_id := p_merchant; created := true;
  RETURN NEXT;
END $$;
REVOKE ALL ON FUNCTION register_self_service_merchant(uuid,text,text,text,text,uuid,uuid,uuid,uuid,timestamptz) FROM PUBLIC;

-- Projects merchant_owner permissions for the current admin user from each
-- active owner membership of a self-service merchant whose merchant and
-- tenant are active. Same identity checks as
-- list_current_admin_merchant_memberships (000011).
CREATE FUNCTION list_current_admin_self_service_owner_grants()
RETURNS TABLE(binding_id uuid,tenant_id uuid,merchant_id uuid,permission_key text)
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path=pg_catalog,public
SET row_security=off AS $$
  WITH actor AS (
    SELECT u.id,u.oidc_issuer,u.oidc_subject,lower(u.email) AS email
      FROM public.admin_users u
     WHERE u.id=nullif(current_setting('app.admin_user_id',true),'')::uuid
       AND u.status='active'
  )
  SELECT b.id,m.tenant_id,m.merchant_id,rp.permission_key
    FROM actor u
    JOIN public.merchant_members m ON m.admin_user_id=u.id
      AND m.status='active'
      AND m.oidc_issuer=u.oidc_issuer
      AND m.oidc_subject=u.oidc_subject
      AND lower(m.email)=u.email
    JOIN public.tenants t ON t.id=m.tenant_id AND t.status='active'
    JOIN public.merchants merchant ON merchant.id=m.merchant_id AND merchant.tenant_id=m.tenant_id AND merchant.status='active'
    JOIN public.merchant_signups s ON s.merchant_id=m.merchant_id AND s.tenant_id=m.tenant_id
    JOIN public.merchant_member_role_bindings b ON b.member_id=m.id
      AND b.tenant_id=m.tenant_id AND b.merchant_id=m.merchant_id AND b.role_key='owner' AND b.revoked_at IS NULL
    JOIN public.admin_role_permissions rp ON rp.role_key='merchant_owner'
   ORDER BY m.tenant_id,m.merchant_id,b.id,rp.permission_key
$$;
REVOKE ALL ON FUNCTION list_current_admin_self_service_owner_grants() FROM PUBLIC;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='merchant_admin_runtime') THEN
    GRANT EXECUTE ON FUNCTION list_current_admin_self_service_owner_grants() TO merchant_admin_runtime;
    GRANT EXECUTE ON FUNCTION create_admin_signup_request(uuid,text,text,text,text,text,timestamptz,timestamptz) TO merchant_admin_runtime;
    GRANT EXECUTE ON FUNCTION register_self_service_merchant(uuid,text,text,text,text,uuid,uuid,uuid,uuid,timestamptz) TO merchant_admin_runtime;
    GRANT SELECT ON signup_business_types,signup_blocked_countries TO merchant_admin_runtime;
  END IF;
END $$;

COMMIT;
