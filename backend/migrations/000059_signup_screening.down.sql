BEGIN;

DROP FUNCTION IF EXISTS current_admin_signup_review_status();
DROP FUNCTION IF EXISTS decide_signup_screening(uuid,text,text,timestamptz);
DROP FUNCTION IF EXISTS list_signup_screening_reviews(integer);
DROP FUNCTION IF EXISTS current_admin_has_global_permission(text);
DROP FUNCTION IF EXISTS register_self_service_merchant(uuid,text,text,text,text,uuid,uuid,uuid,uuid,timestamptz);
DROP FUNCTION IF EXISTS screen_admin_signup(admin_signup_requests,text,text,timestamptz);
DROP FUNCTION IF EXISTS create_admin_signup_request(uuid,text,text,text,text,text,timestamptz,timestamptz,text,boolean);
DROP TABLE IF EXISTS merchant_signup_screenings;
ALTER TABLE admin_signup_requests DROP COLUMN IF EXISTS ip_screened, DROP COLUMN IF EXISTS ip_region;
DROP FUNCTION IF EXISTS replace_signup_screening_source(text,text,timestamptz,interval,jsonb);
DROP FUNCTION IF EXISTS signup_screening_domain_key(text);
DROP FUNCTION IF EXISTS signup_screening_name_key(text);
DROP TABLE IF EXISTS signup_screened_ip_regions;
DROP TABLE IF EXISTS signup_screening_entries;
DROP TABLE IF EXISTS signup_screening_sources;
DELETE FROM admin_role_permissions WHERE permission_key IN ('signup_reviews:read','signup_reviews:decide');
DELETE FROM admin_permissions WHERE permission_key IN ('signup_reviews:read','signup_reviews:decide');

-- The 000058 functions, unchanged.
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

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='merchant_admin_runtime') THEN
    GRANT EXECUTE ON FUNCTION create_admin_signup_request(uuid,text,text,text,text,text,timestamptz,timestamptz) TO merchant_admin_runtime;
    GRANT EXECUTE ON FUNCTION register_self_service_merchant(uuid,text,text,text,text,uuid,uuid,uuid,uuid,timestamptz) TO merchant_admin_runtime;
  END IF;
END $$;

COMMIT;
