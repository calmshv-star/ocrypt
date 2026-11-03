BEGIN;

-- REG-3: sanctions screening of self-service signups. The company name, the
-- registrant's name, the website and email domains and the client region are
-- screened inside the registration transaction; any match, or lists that are
-- missing or stale, creates the merchant in status 'review' so no cabinet
-- right is projected until a platform operator admits it.

INSERT INTO admin_permissions(permission_key,description) VALUES
  ('signup_reviews:read','Read self-service signups held by sanctions screening'),
  ('signup_reviews:decide','Admit or reject a self-service signup held by sanctions screening');
INSERT INTO admin_role_permissions(role_key,permission_key) VALUES
  ('security_admin','signup_reviews:read'),('security_admin','signup_reviews:decide'),
  ('auditor','signup_reviews:read');

-- Lists are loaded by the database owner from official files
-- (scripts/signup_screening_import.py). A source with stale_after is an
-- official list that must be refreshed; NULL marks an operator-curated list.
CREATE TABLE signup_screening_sources (
  source text PRIMARY KEY CHECK (source ~ '^[a-z][a-z0-9_]{1,31}$'),
  description text NOT NULL CHECK (length(description) BETWEEN 1 AND 200),
  published_at timestamptz,
  loaded_at timestamptz NOT NULL,
  stale_after interval CHECK (stale_after IS NULL OR stale_after>interval '0'),
  entry_count integer NOT NULL CHECK (entry_count>=0)
);
CREATE TABLE signup_screening_entries (
  source text NOT NULL REFERENCES signup_screening_sources(source) ON DELETE CASCADE,
  kind text NOT NULL CHECK (kind IN ('name','domain')),
  match_key text NOT NULL CHECK (length(match_key) BETWEEN 3 AND 255),
  reference text NOT NULL CHECK (length(reference) BETWEEN 1 AND 128),
  match_words text[] GENERATED ALWAYS AS (string_to_array(match_key,' ')) STORED,
  PRIMARY KEY (source,kind,match_key,reference)
);
CREATE INDEX signup_screening_entries_match_idx ON signup_screening_entries(kind,match_key);
CREATE INDEX signup_screening_entries_words_idx ON signup_screening_entries USING gin(match_words) WHERE kind='name';
-- Pseudo-regions an edge proxy reports besides country codes.
CREATE TABLE signup_screened_ip_regions (
  region text PRIMARY KEY CHECK (region ~ '^[A-Z][A-Z0-9]$'),
  reason text NOT NULL CHECK (length(reason) BETWEEN 1 AND 200)
);
INSERT INTO signup_screened_ip_regions(region,reason) VALUES ('T1','Tor exit node');
REVOKE ALL ON signup_screening_sources,signup_screening_entries,signup_screened_ip_regions FROM PUBLIC;

-- One key per name, computed only here so lists and signups cannot disagree:
-- casefold, NFKD without combining marks, dots and apostrophes dropped (S.A.,
-- O'Neil), other punctuation to spaces, legal forms and joining words dropped, distinct words sorted. The builtin collation
-- keeps it independent of the database locale.
CREATE FUNCTION signup_screening_name_key(p_name text) RETURNS text
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE
SET search_path=pg_catalog AS $$
  SELECT CASE WHEN length(k)>=3 THEN k END FROM (
    SELECT string_agg(w,' ' ORDER BY w COLLATE "C") AS k FROM (
      SELECT DISTINCT w FROM regexp_split_to_table(
        regexp_replace(
          regexp_replace(normalize(casefold(p_name COLLATE pg_unicode_fast),NFKD) COLLATE pg_unicode_fast,'[̀-ͯ.''’]','','g'),
          '[^[:alnum:]]+',' ','g'),
        ' ') AS w
       WHERE w<>'' AND w NOT IN (
         'llc','ltd','limited','inc','incorporated','corp','corporation','co','company','plc','llp','lp',
         'gmbh','ag','kg','sa','sas','sarl','srl','spa','bv','nv','oy','oyj','ab','as','asa','aps',
         'jsc','pjsc','ojsc','cjsc','oao','zao','pao','ooo','fze','fzco','fzc','fzllc','pte','pty',
         'the','of','and','und','et',
         'ооо','оао','зао','пао','ао','ип','тов','чп','и')
    ) words
  ) joined
$$;

-- A bare lowercase host: scheme, credentials, port, path and a leading www.
-- removed. NULL unless it is a syntactically valid multi-label DNS name.
CREATE FUNCTION signup_screening_domain_key(p_value text) RETURNS text
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE
SET search_path=pg_catalog AS $$
  SELECT CASE WHEN length(h)<=253 AND h ~ '^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]([a-z0-9-]{0,61}[a-z0-9])?$' THEN h END
    FROM (SELECT regexp_replace(rtrim(regexp_replace(regexp_replace(regexp_replace(
           lower(btrim(p_value) COLLATE "C"),'^[a-z][a-z0-9+.-]*://',''),'^[^/?#]*@',''),'[/?#:].*$',''),'.'),'^www\.','') AS h) host
$$;

-- Replaces one source atomically. An official list may not be emptied or
-- shrink below half of what is loaded, so a truncated download cannot
-- silently switch screening off; delete the source row to start over.
CREATE FUNCTION replace_signup_screening_source(
  p_source text,p_description text,p_published_at timestamptz,p_stale_after interval,p_entries jsonb
) RETURNS integer
LANGUAGE plpgsql
SET search_path=pg_catalog,public AS $$
DECLARE previous integer; loaded integer;
BEGIN
  IF p_source IS NULL OR p_source !~ '^[a-z][a-z0-9_]{1,31}$' THEN
    RAISE EXCEPTION 'screening source name is invalid' USING ERRCODE='22023';
  END IF;
  IF p_entries IS NULL OR jsonb_typeof(p_entries)<>'array' OR EXISTS (
       SELECT 1 FROM jsonb_array_elements(p_entries) e
        WHERE jsonb_typeof(e)<>'object' OR coalesce(e->>'kind','') NOT IN ('name','domain')
           OR coalesce(e->>'value','')='' OR length(coalesce(e->>'reference','')) NOT BETWEEN 1 AND 128) THEN
    RAISE EXCEPTION 'screening entries must be objects with kind name or domain, value and reference' USING ERRCODE='22023';
  END IF;
  SELECT s.entry_count INTO previous FROM public.signup_screening_sources s WHERE s.source=p_source FOR UPDATE;
  INSERT INTO public.signup_screening_sources(source,description,published_at,loaded_at,stale_after,entry_count)
    VALUES(p_source,p_description,p_published_at,clock_timestamp(),p_stale_after,0)
    ON CONFLICT (source) DO UPDATE SET description=excluded.description,published_at=excluded.published_at,
      loaded_at=excluded.loaded_at,stale_after=excluded.stale_after;
  DELETE FROM public.signup_screening_entries WHERE source=p_source;
  INSERT INTO public.signup_screening_entries(source,kind,match_key,reference)
    SELECT DISTINCT p_source,x.kind,x.match_key,x.reference FROM (
      SELECT e->>'kind' AS kind,e->>'reference' AS reference,
             CASE e->>'kind' WHEN 'name' THEN public.signup_screening_name_key(e->>'value')
                             ELSE public.signup_screening_domain_key(e->>'value') END AS match_key
        FROM jsonb_array_elements(p_entries) e) x
     WHERE x.match_key IS NOT NULL AND length(x.match_key)<=255;
  GET DIAGNOSTICS loaded = ROW_COUNT;
  IF p_stale_after IS NOT NULL AND loaded=0 THEN
    RAISE EXCEPTION 'an official screening list cannot be empty' USING ERRCODE='22023';
  END IF;
  IF previous IS NOT NULL AND loaded*2<previous THEN
    RAISE EXCEPTION 'screening list % would shrink from % to % entries', p_source, previous, loaded USING ERRCODE='22023';
  END IF;
  UPDATE public.signup_screening_sources SET entry_count=loaded WHERE source=p_source;
  RETURN loaded;
END $$;
REVOKE ALL ON FUNCTION replace_signup_screening_source(text,text,timestamptz,interval,jsonb) FROM PUBLIC;

-- The client region reported by the trusted edge proxy when the form was
-- posted. ip_screened=false only when the installation opted out.
ALTER TABLE admin_signup_requests
  ADD COLUMN ip_region text CHECK (ip_region IS NULL OR ip_region ~ '^[A-Z][A-Z0-9]$'),
  ADD COLUMN ip_screened boolean NOT NULL DEFAULT false;

CREATE TABLE merchant_signup_screenings (
  merchant_id uuid PRIMARY KEY REFERENCES merchant_signups(merchant_id) ON DELETE RESTRICT,
  tenant_id uuid NOT NULL,
  status text NOT NULL CHECK (status IN ('clear','review','approved','rejected')),
  reasons text[] NOT NULL CHECK (reasons <@ ARRAY['domain_match','ip_region','ip_region_unknown','name_match','screening_list_stale']::text[]),
  matches jsonb NOT NULL CHECK (jsonb_typeof(matches)='array' AND jsonb_array_length(matches)<=20),
  ip_region text CHECK (ip_region IS NULL OR ip_region ~ '^[A-Z][A-Z0-9]$'),
  screened_at timestamptz NOT NULL,
  decided_by uuid REFERENCES admin_users(id) ON DELETE RESTRICT,
  decided_at timestamptz,
  decision_reason text CHECK (decision_reason IS NULL OR length(decision_reason) BETWEEN 3 AND 1000),
  CHECK ((status='clear')=(cardinality(reasons)=0)),
  CHECK ((status IN ('approved','rejected'))=(decided_by IS NOT NULL AND decided_at IS NOT NULL AND decision_reason IS NOT NULL)),
  CHECK (decided_by IS NOT NULL OR (decided_at IS NULL AND decision_reason IS NULL))
);
CREATE INDEX merchant_signup_screenings_review_idx ON merchant_signup_screenings(screened_at) WHERE status='review';
REVOKE ALL ON merchant_signup_screenings FROM PUBLIC;

DROP FUNCTION create_admin_signup_request(uuid,text,text,text,text,text,timestamptz,timestamptz);
-- Screens and stores a signup request before the OIDC redirect. Returns false
-- instead of raising, so the caller cannot learn which rule rejected it.
CREATE FUNCTION create_admin_signup_request(
  p_id uuid,p_company text,p_country text,p_business text,p_website text,p_terms text,
  p_created timestamptz,p_expires timestamptz,p_ip_region text,p_ip_screened boolean
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
     OR company ~ '[[:cntrl:]­؜᠎​-‏‪-‮⁠-⁯﻿]'
     OR EXISTS (SELECT 1 FROM public.signup_blocked_countries c WHERE c.country=p_country)
     OR NOT EXISTS (SELECT 1 FROM public.signup_business_types b WHERE b.business_type=p_business AND b.allowed)
     OR (p_website IS NOT NULL AND (p_website !~ '^https://[^[:space:]/?#]+(/[^[:space:]]*)?$' OR length(p_website)>255))
     OR length(coalesce(p_terms,'')) NOT BETWEEN 1 AND 64
     OR p_expires<=p_created OR p_expires>p_created+interval '1 hour'
     OR p_ip_screened IS NULL OR (p_ip_region IS NOT NULL AND (NOT p_ip_screened OR p_ip_region !~ '^[A-Z][A-Z0-9]$')) THEN
    RETURN false;
  END IF;
  INSERT INTO public.admin_signup_requests(id,company_name,country,business_type,website,terms_version,created_at,expires_at,ip_region,ip_screened)
    VALUES(p_id,company,p_country,p_business,p_website,p_terms,p_created,p_expires,p_ip_region,p_ip_screened);
  RETURN true;
END $$;
REVOKE ALL ON FUNCTION create_admin_signup_request(uuid,text,text,text,text,text,timestamptz,timestamptz,text,boolean) FROM PUBLIC;

-- Reasons and matched list references for one signup, sorted and bounded.
CREATE FUNCTION screen_admin_signup(req public.admin_signup_requests,p_email text,p_display_name text,p_at timestamptz)
RETURNS TABLE(reasons text[],matches jsonb)
LANGUAGE sql STABLE
SET search_path=pg_catalog,public AS $$
  WITH names AS (
    SELECT DISTINCT k,string_to_array(k,' ') AS words FROM (VALUES (public.signup_screening_name_key(req.company_name)),(public.signup_screening_name_key(p_display_name))) v(k)
     WHERE k IS NOT NULL
  ), hosts AS (
    SELECT DISTINCT h FROM (VALUES (public.signup_screening_domain_key(req.website)),(public.signup_screening_domain_key(split_part(p_email,'@',2)))) v(h)
     WHERE h IS NOT NULL
  ), suffixes AS (
    -- every parent domain of each host, so a listed domain also catches its subdomains
    SELECT DISTINCT array_to_string(labels[i:],'.') AS d
      FROM (SELECT string_to_array(h,'.') AS labels FROM hosts) x, generate_subscripts(labels,1) i
     WHERE i<cardinality(labels)
  ), hits AS (
    -- Same words, or at least two words of one name all found in the other
    -- (a listed person without the patronymic, a listed company plus words).
    SELECT 'name_match' AS reason,e.source,e.kind,e.reference FROM public.signup_screening_entries e JOIN names n ON e.kind='name'
       AND (e.match_key=n.k OR (cardinality(n.words)>=2 AND e.match_words@>n.words) OR (cardinality(e.match_words)>=2 AND e.match_words<@n.words))
    UNION
    SELECT 'domain_match',e.source,e.kind,e.reference FROM public.signup_screening_entries e JOIN suffixes s ON e.kind='domain' AND e.match_key=s.d
  ), flags AS (
    SELECT reason FROM hits
    UNION SELECT 'ip_region' WHERE req.ip_screened AND req.ip_region IS NOT NULL AND req.ip_region<>'XX' AND (
      EXISTS (SELECT 1 FROM public.signup_blocked_countries c WHERE c.country=req.ip_region)
      OR EXISTS (SELECT 1 FROM public.signup_screened_ip_regions r WHERE r.region=req.ip_region))
    UNION SELECT 'ip_region_unknown' WHERE req.ip_screened AND (req.ip_region IS NULL OR req.ip_region='XX')
    UNION SELECT 'screening_list_stale' WHERE NOT EXISTS (SELECT 1 FROM public.signup_screening_sources s WHERE s.stale_after IS NOT NULL)
       OR EXISTS (SELECT 1 FROM public.signup_screening_sources s WHERE s.stale_after IS NOT NULL AND s.loaded_at+s.stale_after<p_at)
  )
  SELECT coalesce((SELECT array_agg(reason ORDER BY reason) FROM flags),ARRAY[]::text[]),
         coalesce((SELECT jsonb_agg(jsonb_build_object('source',source,'kind',kind,'reference',reference) ORDER BY source,kind,reference)
                     FROM (SELECT DISTINCT source,kind,reference FROM hits ORDER BY source,kind,reference LIMIT 20) h),'[]'::jsonb)
$$;
REVOKE ALL ON FUNCTION screen_admin_signup(public.admin_signup_requests,text,text,timestamptz) FROM PUBLIC;

DROP FUNCTION register_self_service_merchant(uuid,text,text,text,text,uuid,uuid,uuid,uuid,timestamptz);
-- Consumes one screened request after OIDC (verified email and MFA are
-- checked by the caller). A returning identity is not given a second tenant:
-- it gets its existing merchant back with created=false. A new account whose
-- screening has any reason is created held (merchant 'review', held=true).
-- Returns no row when the request is unknown, consumed, expired or the
-- identity is not usable.
CREATE FUNCTION register_self_service_merchant(
  p_request uuid,p_issuer text,p_subject text,p_email text,p_display_name text,
  p_user uuid,p_tenant uuid,p_merchant uuid,p_member uuid,p_at timestamptz
) RETURNS TABLE(user_id uuid,tenant_id uuid,merchant_id uuid,created boolean,held boolean)
LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE req public.admin_signup_requests%ROWTYPE; existing public.admin_users%ROWTYPE;
  normalized_email text := lower(btrim(coalesce(p_email,'')));
  display text := left(btrim(coalesce(p_display_name,'')),255);
  found_reasons text[]; found_matches jsonb;
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
    user_id := existing.id; created := false; held := false;
    IF tenant_id IS NOT NULL THEN
      PERFORM public.append_admin_audit(gen_random_uuid(),tenant_id,merchant_id,existing.id,NULL,
        'admin.self_service.returning_identity','merchant',merchant_id::text,req.id::text,'signup by an existing identity',NULL,NULL,
        '{}'::jsonb,NULL,NULL,p_at);
    END IF;
    RETURN NEXT;
    RETURN;
  END IF;
  SELECT s.reasons,s.matches INTO found_reasons,found_matches FROM public.screen_admin_signup(req,normalized_email,p_display_name,p_at) s;
  held := cardinality(found_reasons)>0;
  INSERT INTO public.tenants(id,public_id,name,status,created_at,updated_at)
    VALUES(p_tenant,'tn_'||replace(p_tenant::text,'-',''),req.company_name,'active',p_at,p_at);
  INSERT INTO public.merchants(id,tenant_id,code,display_name,environment,settlement_currency,status,created_at,updated_at)
    VALUES(p_merchant,p_tenant,'main',req.company_name,'test','USD',CASE WHEN held THEN 'review' ELSE 'active' END,p_at,p_at);
  INSERT INTO public.admin_users(id,oidc_issuer,oidc_subject,display_name,email,status,created_at,updated_at,version)
    VALUES(p_user,p_issuer,p_subject,display,normalized_email,'active',p_at,p_at,1);
  INSERT INTO public.merchant_members(id,tenant_id,merchant_id,admin_user_id,oidc_issuer,oidc_subject,email,display_name,status,joined_at,updated_at)
    VALUES(p_member,p_tenant,p_merchant,p_user,p_issuer,p_subject,normalized_email,display,'active',p_at,p_at);
  INSERT INTO public.merchant_member_role_bindings(id,tenant_id,merchant_id,member_id,role_key,granted_by,granted_at,reason)
    VALUES(gen_random_uuid(),p_tenant,p_merchant,p_member,'owner',p_user,p_at,'self-service registration owner');
  INSERT INTO public.merchant_signups(merchant_id,tenant_id,admin_user_id,request_id,country,business_type,website,terms_version,terms_accepted_at,created_at)
    VALUES(p_merchant,p_tenant,p_user,req.id,req.country,req.business_type,req.website,req.terms_version,req.created_at,p_at);
  INSERT INTO public.merchant_signup_screenings(merchant_id,tenant_id,status,reasons,matches,ip_region,screened_at)
    VALUES(p_merchant,p_tenant,CASE WHEN held THEN 'review' ELSE 'clear' END,found_reasons,found_matches,req.ip_region,p_at);
  PERFORM public.append_admin_audit(gen_random_uuid(),p_tenant,p_merchant,p_user,NULL,
    'admin.self_service.registered','merchant',p_merchant::text,req.id::text,'self-service registration',NULL,NULL,
    jsonb_build_object('country',req.country,'business_type',req.business_type,'terms_version',req.terms_version),NULL,NULL,p_at);
  IF held THEN
    PERFORM public.append_admin_audit(gen_random_uuid(),p_tenant,p_merchant,p_user,NULL,
      'admin.self_service.screening_hold','merchant',p_merchant::text,req.id::text,'held for sanctions screening review',NULL,NULL,
      jsonb_build_object('reasons',to_jsonb(found_reasons),'matches',found_matches),NULL,NULL,p_at);
  END IF;
  user_id := p_user; tenant_id := p_tenant; merchant_id := p_merchant; created := true;
  RETURN NEXT;
END $$;
REVOKE ALL ON FUNCTION register_self_service_merchant(uuid,text,text,text,text,uuid,uuid,uuid,uuid,timestamptz) FROM PUBLIC;

-- True when the current admin user holds the permission through an active
-- global binding (no tenant, no merchant). Tenant-scoped operators never see
-- or decide signups of other tenants.
CREATE FUNCTION current_admin_has_global_permission(p_permission text) RETURNS boolean
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path=pg_catalog,public SET row_security=off AS $$
  SELECT EXISTS (
    SELECT 1 FROM public.admin_users u
      JOIN public.admin_role_bindings b ON b.user_id=u.id AND b.tenant_id IS NULL AND b.merchant_id IS NULL
       AND b.revoked_at IS NULL AND (b.expires_at IS NULL OR b.expires_at>clock_timestamp())
      JOIN public.admin_role_permissions rp ON rp.role_key=b.role_key AND rp.permission_key=p_permission
     WHERE u.id=nullif(current_setting('app.admin_user_id',true),'')::uuid AND u.status='active')
$$;
REVOKE ALL ON FUNCTION current_admin_has_global_permission(text) FROM PUBLIC;

-- Held signups, oldest first, for a global reviewer.
CREATE FUNCTION list_signup_screening_reviews(p_limit integer)
RETURNS TABLE(merchant_id uuid,tenant_id uuid,company_name text,country text,business_type text,website text,
  email text,display_name text,ip_region text,reasons text[],matches jsonb,screened_at timestamptz)
LANGUAGE plpgsql STABLE SECURITY DEFINER
SET search_path=pg_catalog,public SET row_security=off AS $$
BEGIN
  IF NOT public.current_admin_has_global_permission('signup_reviews:read') THEN
    RAISE EXCEPTION 'signup review requires a global signup_reviews:read binding' USING ERRCODE='42501';
  END IF;
  RETURN QUERY
    SELECT s.merchant_id,s.tenant_id,m.display_name,ms.country::text,ms.business_type,ms.website,
           coalesce(u.email,''),u.display_name,s.ip_region,s.reasons,s.matches,s.screened_at
      FROM public.merchant_signup_screenings s
      JOIN public.merchant_signups ms ON ms.merchant_id=s.merchant_id
      JOIN public.merchants m ON m.id=s.merchant_id AND m.tenant_id=s.tenant_id
      JOIN public.admin_users u ON u.id=ms.admin_user_id
     WHERE s.status='review'
     ORDER BY s.screened_at,s.merchant_id
     LIMIT least(greatest(coalesce(p_limit,100),1),200);
END $$;
REVOKE ALL ON FUNCTION list_signup_screening_reviews(integer) FROM PUBLIC;

-- Admits (merchant review -> active) or rejects (-> disabled) one held
-- signup, once, with a reason. The registrant can never decide its own.
CREATE FUNCTION decide_signup_screening(p_merchant uuid,p_decision text,p_reason text,p_at timestamptz)
RETURNS text
LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE actor uuid := nullif(current_setting('app.admin_user_id',true),'')::uuid;
  screening public.merchant_signup_screenings%ROWTYPE; signup public.merchant_signups%ROWTYPE;
  reason text := btrim(coalesce(p_reason,'')); outcome text;
BEGIN
  IF NOT public.current_admin_has_global_permission('signup_reviews:decide') THEN
    RAISE EXCEPTION 'signup decision requires a global signup_reviews:decide binding' USING ERRCODE='42501';
  END IF;
  IF p_decision NOT IN ('approve','reject') OR length(reason) NOT BETWEEN 3 AND 1000 THEN
    RAISE EXCEPTION 'decision must be approve or reject with a reason of 3 to 1000 characters' USING ERRCODE='22023';
  END IF;
  SELECT * INTO screening FROM public.merchant_signup_screenings s WHERE s.merchant_id=p_merchant FOR UPDATE;
  IF NOT FOUND OR screening.status<>'review' THEN
    RAISE EXCEPTION 'signup is not awaiting review' USING ERRCODE='55000';
  END IF;
  SELECT * INTO signup FROM public.merchant_signups s WHERE s.merchant_id=p_merchant;
  IF signup.admin_user_id=actor OR EXISTS (SELECT 1 FROM public.merchant_members m WHERE m.merchant_id=p_merchant AND m.admin_user_id=actor) THEN
    RAISE EXCEPTION 'a registrant cannot review its own signup' USING ERRCODE='42501';
  END IF;
  outcome := CASE p_decision WHEN 'approve' THEN 'approved' ELSE 'rejected' END;
  UPDATE public.merchants SET status=CASE outcome WHEN 'approved' THEN 'active' ELSE 'disabled' END,updated_at=p_at,version=version+1
   WHERE id=p_merchant AND tenant_id=screening.tenant_id AND status='review';
  IF NOT FOUND THEN
    RAISE EXCEPTION 'merchant is no longer held for review' USING ERRCODE='55000';
  END IF;
  UPDATE public.merchant_signup_screenings SET status=outcome,decided_by=actor,decided_at=p_at,decision_reason=reason
   WHERE merchant_id=p_merchant;
  PERFORM public.append_admin_audit(gen_random_uuid(),screening.tenant_id,p_merchant,actor,NULL,
    'admin.signup_review.'||outcome,'merchant',p_merchant::text,p_merchant::text,reason,NULL,NULL,
    jsonb_build_object('reasons',to_jsonb(screening.reasons)),NULL,NULL,p_at);
  RETURN outcome;
END $$;
REVOKE ALL ON FUNCTION decide_signup_screening(uuid,text,text,timestamptz) FROM PUBLIC;

-- What the current registrant may know about its own application: 'pending'
-- while held, 'rejected' after a rejection, NULL otherwise. Never reasons.
CREATE FUNCTION current_admin_signup_review_status() RETURNS text
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path=pg_catalog,public SET row_security=off AS $$
  SELECT CASE s.status WHEN 'review' THEN 'pending' WHEN 'rejected' THEN 'rejected' END
    FROM public.merchant_signups ms
    JOIN public.merchant_signup_screenings s ON s.merchant_id=ms.merchant_id
   WHERE ms.admin_user_id=nullif(current_setting('app.admin_user_id',true),'')::uuid
     AND s.status IN ('review','rejected')
   ORDER BY ms.created_at DESC LIMIT 1
$$;
REVOKE ALL ON FUNCTION current_admin_signup_review_status() FROM PUBLIC;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='merchant_admin_runtime') THEN
    GRANT EXECUTE ON FUNCTION create_admin_signup_request(uuid,text,text,text,text,text,timestamptz,timestamptz,text,boolean) TO merchant_admin_runtime;
    GRANT EXECUTE ON FUNCTION register_self_service_merchant(uuid,text,text,text,text,uuid,uuid,uuid,uuid,timestamptz) TO merchant_admin_runtime;
    GRANT EXECUTE ON FUNCTION list_signup_screening_reviews(integer) TO merchant_admin_runtime;
    GRANT EXECUTE ON FUNCTION decide_signup_screening(uuid,text,text,timestamptz) TO merchant_admin_runtime;
    GRANT EXECUTE ON FUNCTION current_admin_signup_review_status() TO merchant_admin_runtime;
  END IF;
END $$;

COMMIT;
