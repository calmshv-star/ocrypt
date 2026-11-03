-- REG-5: email change, data export and account deletion for cabinet users.
--
-- Passwords, second factors and the email itself live at the identity
-- provider. A login always carries a verified email; when it differs from
-- the stored one, the cabinet takes it over (user and memberships in one
-- transaction), audits it and tells the old address.
--
-- A user can download what the platform holds about them and delete their
-- account. Deletion ends every session and access, removes notification
-- data, and pseudonymises the user and their memberships; the audit log and
-- the merchants' financial records stay, as the law requires. A merchant the
-- user alone owns is closed (status disabled) only when they confirm it.
-- Platform operators (global bindings) are offboarded by another operator.
BEGIN;

ALTER TABLE notification_jobs ADD COLUMN recipient_email text
  CHECK (recipient_email IS NULL OR (length(recipient_email) BETWEEN 3 AND 320 AND recipient_email LIKE '%_@_%._%'));
ALTER TABLE notification_jobs DROP CONSTRAINT notification_jobs_event_check;
ALTER TABLE notification_jobs ADD CONSTRAINT notification_jobs_event_check
  CHECK (event IN ('paid','underpaid','review','webhook_failed','sign_in','sanctioned','email_changed'));
ALTER TABLE notification_jobs DROP CONSTRAINT notification_jobs_check;
ALTER TABLE notification_jobs ADD CONSTRAINT notification_jobs_check
  CHECK ((event IN ('sign_in','email_changed')) = (merchant_id IS NULL));
-- Only the email-change notice goes to an address other than the user's.
ALTER TABLE notification_jobs ADD CONSTRAINT notification_jobs_recipient_check
  CHECK (recipient_email IS NULL OR (event='email_changed' AND channel='email'));

-- As in 000063/000064, plus a fixed recipient for the email-change notice
-- (the old address, which the user no longer has on file).
CREATE OR REPLACE FUNCTION claim_notification_job(p_worker uuid,p_lease_seconds integer)
RETURNS TABLE(id uuid,lease_token uuid,channel text,event text,payload jsonb,locale text,email text,chat_id bigint,attempt_count integer)
LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE job record; now_at timestamptz; recipient_email text; recipient_chat bigint; token uuid;
BEGIN
  IF p_worker IS NULL OR p_lease_seconds<10 OR p_lease_seconds>300 THEN RAISE EXCEPTION 'invalid notification lease' USING ERRCODE='OCP02'; END IF;
  DELETE FROM public.notification_jobs j WHERE j.id IN (
    SELECT x.id FROM public.notification_jobs x WHERE x.status IN ('sent','dead') AND x.updated_at<clock_timestamp()-interval '30 days' LIMIT 100);
  LOOP
    now_at := clock_timestamp();
    SELECT j.* INTO job FROM public.notification_jobs j
     WHERE (j.status IN ('pending','retry') AND j.available_at<=now_at) OR (j.status='leased' AND j.lease_until<now_at)
     ORDER BY j.available_at,j.id FOR UPDATE SKIP LOCKED LIMIT 1;
    IF NOT FOUND THEN RETURN; END IF;
    IF job.attempt_count>=20 THEN
      UPDATE public.notification_jobs SET status='dead',last_error_code='attempts_exhausted',lease_owner=NULL,lease_token=NULL,lease_until=NULL,updated_at=now_at WHERE notification_jobs.id=job.id;
      CONTINUE;
    END IF;
    recipient_email := NULL; recipient_chat := NULL;
    IF job.recipient_email IS NOT NULL THEN
      recipient_email := job.recipient_email;
    ELSIF job.merchant_id IS NULL OR public.notification_recipient_allowed(job.user_id,job.tenant_id,job.merchant_id,
         CASE WHEN job.event='webhook_failed' THEN 'webhooks:read' ELSE 'payments:read' END) THEN
      IF job.channel='email' THEN
        SELECT u.email INTO recipient_email FROM public.admin_users u WHERE u.id=job.user_id AND u.status='active' AND u.email LIKE '%_@_%';
      ELSE
        SELECT l.chat_id INTO recipient_chat FROM public.telegram_chat_links l JOIN public.admin_users u ON u.id=l.user_id AND u.status='active' WHERE l.user_id=job.user_id;
      END IF;
    END IF;
    IF recipient_email IS NULL AND recipient_chat IS NULL THEN
      UPDATE public.notification_jobs SET status='dead',last_error_code='no_recipient',lease_owner=NULL,lease_token=NULL,lease_until=NULL,updated_at=now_at WHERE notification_jobs.id=job.id;
      CONTINUE;
    END IF;
    token := gen_random_uuid();
    UPDATE public.notification_jobs SET status='leased',lease_owner=p_worker,lease_token=token,lease_until=now_at+p_lease_seconds*interval '1 second',
      attempt_count=notification_jobs.attempt_count+1,updated_at=now_at WHERE notification_jobs.id=job.id;
    RETURN QUERY SELECT job.id,token,job.channel,job.event,job.payload,job.locale,recipient_email,recipient_chat,job.attempt_count+1;
    RETURN;
  END LOOP;
END $$;

-- Takes over the verified email the provider sent at login. Returns true
-- when it changed. The old address is told (with the new one masked).
CREATE FUNCTION sync_current_admin_email(p_email text,p_at timestamptz) RETURNS boolean
LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE actor uuid:=nullif(current_setting('app.admin_user_id',true),'')::uuid;
  normalized text:=lower(btrim(p_email)); previous text; issuer text; subject text; chosen text; masked text;
BEGIN
  IF actor IS NULL OR normalized IS NULL OR length(normalized) NOT BETWEEN 3 AND 320 OR normalized NOT LIKE '%_@_%._%' THEN
    RAISE EXCEPTION 'invalid email' USING ERRCODE='OCP02';
  END IF;
  SELECT u.email,u.oidc_issuer,u.oidc_subject INTO previous,issuer,subject FROM public.admin_users u
   WHERE u.id=actor AND u.status='active' FOR UPDATE;
  IF NOT FOUND THEN RAISE EXCEPTION 'not an active admin' USING ERRCODE='OCP01'; END IF;
  IF previous IS NOT DISTINCT FROM normalized THEN RETURN false; END IF;
  UPDATE public.admin_users SET email=normalized,updated_at=p_at,version=version+1 WHERE id=actor;
  UPDATE public.merchant_members SET email=normalized,updated_at=p_at,version=version+1
   WHERE admin_user_id=actor AND oidc_issuer=issuer AND oidc_subject=subject AND status<>'removed';
  PERFORM public.append_admin_audit(gen_random_uuid(),NULL,NULL,actor,NULL,'admin.user.email_changed','admin_user',actor::text,
    'email-sync','verified email changed at the sign-in provider',NULL,NULL,
    jsonb_build_object('previous_domain',split_part(coalesce(previous,''),'@',2),'new_domain',split_part(normalized,'@',2)),NULL,NULL,p_at);
  IF previous IS NOT NULL AND previous LIKE '%_@_%._%' THEN
    SELECT a.locale INTO chosen FROM public.notification_account_settings a WHERE a.user_id=actor;
    masked := left(normalized,1)||'***@'||split_part(normalized,'@',2);
    INSERT INTO public.notification_jobs(id,user_id,channel,event,dedupe_key,payload,locale,status,available_at,created_at,updated_at,recipient_email)
    VALUES(gen_random_uuid(),actor,'email','email_changed','email_changed:'||md5(previous||chr(31)||normalized),
           jsonb_build_object('new_email',masked,'changed_at',p_at),coalesce(chosen,'en'),'pending',p_at,p_at,p_at,previous)
    ON CONFLICT (user_id,channel,dedupe_key) DO NOTHING;
  END IF;
  RETURN true;
END $$;

-- Self-service and team merchants the current user is the only active
-- owner of (and that are not closed yet).
CREATE FUNCTION current_admin_sole_owned_merchants() RETURNS TABLE(tenant_id uuid,merchant_id uuid,display_name text)
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path=pg_catalog,public SET row_security=off AS $$
  SELECT DISTINCT m.tenant_id,m.merchant_id,merchant.display_name
    FROM public.merchant_members m
    JOIN public.merchant_member_role_bindings b ON b.member_id=m.id AND b.role_key='owner' AND b.revoked_at IS NULL
    JOIN public.merchants merchant ON merchant.id=m.merchant_id AND merchant.tenant_id=m.tenant_id AND merchant.status<>'disabled'
   WHERE m.admin_user_id=nullif(current_setting('app.admin_user_id',true),'')::uuid AND m.status='active'
     AND NOT EXISTS (SELECT 1 FROM public.merchant_member_role_bindings ob JOIN public.merchant_members om ON om.id=ob.member_id
                      WHERE ob.merchant_id=m.merchant_id AND ob.role_key='owner' AND ob.revoked_at IS NULL
                        AND om.status='active' AND om.admin_user_id<>m.admin_user_id)
   ORDER BY m.merchant_id
$$;

-- Everything the platform holds about the current user, as one document.
CREATE FUNCTION current_admin_account_export() RETURNS jsonb
LANGUAGE plpgsql STABLE SECURITY DEFINER
SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE actor uuid:=nullif(current_setting('app.admin_user_id',true),'')::uuid; result jsonb;
BEGIN
  IF actor IS NULL OR NOT EXISTS (SELECT 1 FROM public.admin_users u WHERE u.id=actor AND u.status='active') THEN
    RAISE EXCEPTION 'not an active admin' USING ERRCODE='OCP01';
  END IF;
  SELECT jsonb_build_object(
    'format','ocrypt-account-export-v1',
    'user',(SELECT jsonb_build_object('id',u.id,'display_name',u.display_name,'email',u.email,'sign_in_provider',u.oidc_issuer,'provider_subject',u.oidc_subject,'created_at',u.created_at)
              FROM public.admin_users u WHERE u.id=actor),
    'memberships',coalesce((SELECT jsonb_agg(jsonb_build_object('tenant_id',m.tenant_id,'merchant_id',m.merchant_id,'merchant',merchant.display_name,
                     'status',m.status,'joined_at',m.joined_at,
                     'roles',(SELECT coalesce(jsonb_agg(b.role_key ORDER BY b.role_key),'[]'::jsonb) FROM public.merchant_member_role_bindings b WHERE b.member_id=m.id AND b.revoked_at IS NULL))
                     ORDER BY m.joined_at)
              FROM public.merchant_members m JOIN public.merchants merchant ON merchant.id=m.merchant_id AND merchant.tenant_id=m.tenant_id
             WHERE m.admin_user_id=actor),'[]'::jsonb),
    'role_bindings',coalesce((SELECT jsonb_agg(jsonb_build_object('role',b.role_key,'tenant_id',b.tenant_id,'merchant_id',b.merchant_id,'granted_at',b.granted_at,'expires_at',b.expires_at,'revoked_at',b.revoked_at) ORDER BY b.granted_at)
              FROM public.admin_role_bindings b WHERE b.user_id=actor),'[]'::jsonb),
    'sessions',coalesce((SELECT jsonb_agg(jsonb_build_object('created_at',s.created_at,'last_seen_at',s.last_seen_at,'device',s.device_label,'revoked_at',s.revoked_at) ORDER BY s.created_at DESC)
              FROM (SELECT * FROM public.admin_sessions x WHERE x.user_id=actor ORDER BY x.created_at DESC LIMIT 500) s),'[]'::jsonb),
    'notifications',jsonb_build_object(
       'account',(SELECT jsonb_build_object('sign_in_email',a.sign_in_email,'sign_in_telegram',a.sign_in_telegram,'locale',a.locale) FROM public.notification_account_settings a WHERE a.user_id=actor),
       'merchants',coalesce((SELECT jsonb_agg(jsonb_build_object('merchant_id',p.merchant_id,'email_events',p.email_events,'telegram_events',p.telegram_events)) FROM public.notification_preferences p WHERE p.user_id=actor),'[]'::jsonb),
       'telegram_chat_id',(SELECT l.chat_id FROM public.telegram_chat_links l WHERE l.user_id=actor)),
    'audit_events',coalesce((SELECT jsonb_agg(jsonb_build_object('action',e.action,'resource_type',e.resource_type,'resource_id',e.resource_id,'occurred_at',e.occurred_at) ORDER BY e.occurred_at DESC)
              FROM (SELECT * FROM public.admin_audit_log x WHERE x.actor_user_id=actor ORDER BY x.occurred_at DESC LIMIT 1000) e),'[]'::jsonb)
  ) INTO result;
  RETURN jsonb_strip_nulls(result);
END $$;

-- Deletes the current user's account (see the header). Errors: OCP06 for a
-- platform operator, OCP05 when merchants would be left without an owner and
-- p_close_merchants is false. Returns the number of merchants closed.
CREATE FUNCTION delete_current_admin_account(p_close_merchants boolean,p_at timestamptz) RETURNS integer
LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE actor uuid:=nullif(current_setting('app.admin_user_id',true),'')::uuid; item record; closed integer:=0;
BEGIN
  IF actor IS NULL OR NOT EXISTS (SELECT 1 FROM public.admin_users u WHERE u.id=actor AND u.status='active') THEN
    RAISE EXCEPTION 'not an active admin' USING ERRCODE='OCP01';
  END IF;
  IF EXISTS (SELECT 1 FROM public.admin_role_bindings b WHERE b.user_id=actor AND b.tenant_id IS NULL AND b.revoked_at IS NULL AND (b.expires_at IS NULL OR b.expires_at>p_at)) THEN
    RAISE EXCEPTION 'platform operators are offboarded by another operator' USING ERRCODE='OCP06';
  END IF;
  -- The owner guard's lock for every merchant the user belongs to, in a
  -- fixed order, so no concurrent owner change can slip in.
  FOR item IN SELECT DISTINCT m.merchant_id FROM public.merchant_members m WHERE m.admin_user_id=actor ORDER BY m.merchant_id LOOP
    PERFORM pg_advisory_xact_lock(hashtextextended(item.merchant_id::text||chr(31)||'merchant-owner-guard',0));
  END LOOP;
  IF EXISTS (SELECT 1 FROM public.current_admin_sole_owned_merchants()) AND NOT coalesce(p_close_merchants,false) THEN
    RAISE EXCEPTION 'merchants would be left without an owner' USING ERRCODE='OCP05';
  END IF;
  -- Close the merchants only this user owns; their owner record stays (the
  -- last-owner guard) but is pseudonymised below with every other record.
  FOR item IN SELECT * FROM public.current_admin_sole_owned_merchants() LOOP
    UPDATE public.merchants SET status='disabled',updated_at=p_at,version=version+1 WHERE id=item.merchant_id AND tenant_id=item.tenant_id;
    PERFORM public.append_admin_audit(gen_random_uuid(),item.tenant_id,item.merchant_id,actor,NULL,'merchant.closed_with_owner_account','merchant',item.merchant_id::text,
      'account-deletion','closed because its only owner deleted their account',NULL,NULL,'{}'::jsonb,NULL,NULL,p_at);
    closed := closed+1;
  END LOOP;
  -- Leave every other merchant: roles first, then the membership.
  UPDATE public.merchant_member_role_bindings b SET revoked_at=p_at,revoked_by=actor,version=b.version+1
    FROM public.merchant_members m JOIN public.merchants merchant ON merchant.id=m.merchant_id AND merchant.tenant_id=m.tenant_id
   WHERE b.member_id=m.id AND m.admin_user_id=actor AND b.revoked_at IS NULL AND merchant.status<>'disabled';
  UPDATE public.merchant_members m SET status='removed',removed_at=p_at,disabled_at=NULL,updated_at=p_at,version=m.version+1
    FROM public.merchants merchant
   WHERE merchant.id=m.merchant_id AND merchant.tenant_id=m.tenant_id AND m.admin_user_id=actor AND m.status<>'removed' AND merchant.status<>'disabled';
  UPDATE public.merchant_members SET email='deleted-'||left(replace(id::text,'-',''),12)||'@account.invalid',display_name='Deleted user',
         oidc_subject='deleted:'||id::text,updated_at=p_at,version=version+1
   WHERE admin_user_id=actor;
  UPDATE public.admin_role_bindings SET revoked_at=p_at,version=version+1 WHERE user_id=actor AND revoked_at IS NULL;
  UPDATE public.admin_sessions SET revoked_at=coalesce(revoked_at,p_at),revocation_reason=coalesce(revocation_reason,'account_deleted'),
         oidc_subject='deleted:'||actor::text,source_address=NULL,device_label=NULL
   WHERE user_id=actor;
  DELETE FROM public.notification_jobs WHERE user_id=actor AND status IN ('pending','retry','leased');
  DELETE FROM public.notification_preferences WHERE user_id=actor;
  DELETE FROM public.notification_account_settings WHERE user_id=actor;
  DELETE FROM public.telegram_link_codes WHERE user_id=actor;
  DELETE FROM public.telegram_chat_links WHERE user_id=actor;
  -- The identity is freed: the same provider account can sign up anew.
  UPDATE public.admin_users SET display_name='Deleted user',email=NULL,oidc_subject='deleted:'||id::text,status='disabled',updated_at=p_at,version=version+1
   WHERE id=actor;
  PERFORM public.append_admin_audit(gen_random_uuid(),NULL,NULL,actor,NULL,'admin.account.deleted','admin_user',actor::text,
    'account-deletion','account deleted by its user',NULL,NULL,jsonb_build_object('closed_merchants',closed),NULL,NULL,p_at);
  RETURN closed;
END $$;

REVOKE ALL ON FUNCTION sync_current_admin_email(text,timestamptz) FROM PUBLIC;
REVOKE ALL ON FUNCTION current_admin_sole_owned_merchants() FROM PUBLIC;
REVOKE ALL ON FUNCTION current_admin_account_export() FROM PUBLIC;
REVOKE ALL ON FUNCTION delete_current_admin_account(boolean,timestamptz) FROM PUBLIC;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='merchant_admin_runtime') THEN
    GRANT EXECUTE ON FUNCTION sync_current_admin_email(text,timestamptz),current_admin_sole_owned_merchants(),
      current_admin_account_export(),delete_current_admin_account(boolean,timestamptz) TO merchant_admin_runtime;
  END IF;
END $$;

COMMIT;
