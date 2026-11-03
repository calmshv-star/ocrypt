-- Email and Telegram notifications for cabinet users.
--
-- Merchant events (paid, underpaid, review, webhook_failed) fan out inside
-- the transaction that records them: an AFTER trigger on callback_events and
-- on callback_deliveries turning dead_letter queues one job per subscribed
-- user and channel. A failing fan-out only logs a warning; it never aborts a
-- settlement or a delivery update. Recipients must hold payments:read
-- (webhooks:read for webhook failures) for the merchant when the job is
-- queued and again when it is claimed.
--
-- A user without saved preferences for a merchant gets the defaults only if
-- they are the self-service owner: every merchant event by email. Anyone
-- else subscribes explicitly. Sign-in alerts are per account, email and
-- Telegram on by default.
--
-- The tables have no runtime grants: the cabinet BFF and the notification
-- worker reach them only through the definer functions below.
BEGIN;

CREATE TABLE notification_account_settings (
    user_id uuid PRIMARY KEY REFERENCES admin_users(id) ON DELETE CASCADE,
    sign_in_email boolean NOT NULL,
    sign_in_telegram boolean NOT NULL,
    locale text NOT NULL CHECK (locale IN ('en','ru','zh','es','fr','de')),
    updated_at timestamptz NOT NULL
);

CREATE TABLE notification_preferences (
    user_id uuid NOT NULL REFERENCES admin_users(id) ON DELETE CASCADE,
    tenant_id uuid NOT NULL,
    merchant_id uuid NOT NULL,
    email_events text[] NOT NULL,
    telegram_events text[] NOT NULL,
    locale text NOT NULL CHECK (locale IN ('en','ru','zh','es','fr','de')),
    updated_at timestamptz NOT NULL,
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    PRIMARY KEY (user_id, merchant_id),
    FOREIGN KEY (merchant_id, tenant_id) REFERENCES merchants(id, tenant_id) ON DELETE CASCADE,
    CHECK (email_events <@ ARRAY['paid','underpaid','review','webhook_failed']::text[] AND cardinality(email_events) <= 4),
    CHECK (telegram_events <@ ARRAY['paid','underpaid','review','webhook_failed']::text[] AND cardinality(telegram_events) <= 4)
);
CREATE INDEX notification_preferences_merchant_idx ON notification_preferences (merchant_id, tenant_id);

-- One private Telegram chat per user, linked by a one-time code the user
-- sends to the operator's bot. Private chat ids are positive.
CREATE TABLE telegram_chat_links (
    user_id uuid PRIMARY KEY REFERENCES admin_users(id) ON DELETE CASCADE,
    chat_id bigint NOT NULL CHECK (chat_id > 0),
    linked_at timestamptz NOT NULL
);

CREATE TABLE telegram_link_codes (
    code_hash bytea PRIMARY KEY CHECK (octet_length(code_hash) = 32),
    user_id uuid NOT NULL REFERENCES admin_users(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL CHECK (expires_at > created_at AND expires_at <= created_at + interval '30 minutes'),
    used_at timestamptz
);
CREATE INDEX telegram_link_codes_user_idx ON telegram_link_codes (user_id) WHERE used_at IS NULL;

-- The getUpdates offset of the single bot poller.
CREATE TABLE telegram_update_state (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    next_offset bigint NOT NULL CHECK (next_offset >= 0),
    updated_at timestamptz NOT NULL
);

CREATE TABLE notification_jobs (
    id uuid PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES admin_users(id) ON DELETE CASCADE,
    tenant_id uuid,
    merchant_id uuid,
    channel text NOT NULL CHECK (channel IN ('email','telegram')),
    event text NOT NULL CHECK (event IN ('paid','underpaid','review','webhook_failed','sign_in')),
    dedupe_key text NOT NULL CHECK (length(dedupe_key) BETWEEN 1 AND 200),
    payload jsonb NOT NULL CHECK (jsonb_typeof(payload) = 'object' AND pg_column_size(payload) <= 4096),
    locale text NOT NULL CHECK (locale IN ('en','ru','zh','es','fr','de')),
    status text NOT NULL CHECK (status IN ('pending','leased','retry','sent','dead')),
    attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count BETWEEN 0 AND 100),
    available_at timestamptz NOT NULL,
    lease_owner uuid,
    lease_token uuid,
    lease_until timestamptz,
    provider_ref text CHECK (provider_ref IS NULL OR length(provider_ref) BETWEEN 1 AND 255),
    last_error_code text CHECK (last_error_code IS NULL OR last_error_code ~ '^[a-z0-9_]{1,80}$'),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    FOREIGN KEY (merchant_id, tenant_id) REFERENCES merchants(id, tenant_id) ON DELETE CASCADE,
    CHECK ((event = 'sign_in') = (merchant_id IS NULL)),
    CHECK ((status = 'leased') = (lease_owner IS NOT NULL AND lease_token IS NOT NULL AND lease_until IS NOT NULL)),
    UNIQUE (user_id, channel, dedupe_key)
);
CREATE INDEX notification_jobs_claim_idx ON notification_jobs (available_at, id) WHERE status IN ('pending','retry','leased');
CREATE INDEX notification_jobs_purge_idx ON notification_jobs (updated_at) WHERE status IN ('sent','dead');

ALTER TABLE notification_account_settings ENABLE ROW LEVEL SECURITY;
ALTER TABLE notification_preferences ENABLE ROW LEVEL SECURITY;
ALTER TABLE telegram_chat_links ENABLE ROW LEVEL SECURITY;
ALTER TABLE telegram_link_codes ENABLE ROW LEVEL SECURITY;
ALTER TABLE telegram_update_state ENABLE ROW LEVEL SECURITY;
ALTER TABLE notification_jobs ENABLE ROW LEVEL SECURITY;

-- Active self-service owners of the merchant, the same facts
-- list_current_admin_self_service_owner_grants projects.
CREATE FUNCTION notification_default_owners(p_tenant uuid,p_merchant uuid) RETURNS TABLE(user_id uuid)
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path=pg_catalog,public SET row_security=off AS $$
  SELECT DISTINCT u.id
    FROM public.merchant_members mm
    JOIN public.admin_users u ON u.id=mm.admin_user_id AND u.status='active'
     AND mm.oidc_issuer=u.oidc_issuer AND mm.oidc_subject=u.oidc_subject AND lower(mm.email)=lower(u.email)
    JOIN public.merchant_member_role_bindings b ON b.member_id=mm.id AND b.tenant_id=mm.tenant_id
     AND b.merchant_id=mm.merchant_id AND b.role_key='owner' AND b.revoked_at IS NULL
    JOIN public.merchant_signups s ON s.merchant_id=mm.merchant_id AND s.tenant_id=mm.tenant_id
   WHERE mm.tenant_id=p_tenant AND mm.merchant_id=p_merchant AND mm.status='active'
$$;

-- True when the user may read this merchant's payments (or webhooks) now:
-- an unexpired tenant binding whose role carries the permission, or the
-- active self-service owner membership (the merchant_owner projection).
CREATE FUNCTION notification_recipient_allowed(p_user uuid,p_tenant uuid,p_merchant uuid,p_permission text) RETURNS boolean
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path=pg_catalog,public SET row_security=off AS $$
  SELECT EXISTS (
    SELECT 1 FROM public.admin_users u
      JOIN public.tenants t ON t.id=p_tenant AND t.status='active'
      JOIN public.merchants m ON m.id=p_merchant AND m.tenant_id=p_tenant AND m.status='active'
     WHERE u.id=p_user AND u.status='active' AND p_permission IN ('payments:read','webhooks:read') AND (
       EXISTS (SELECT 1 FROM public.admin_role_bindings b
                 JOIN public.admin_role_permissions rp ON rp.role_key=b.role_key AND rp.permission_key=p_permission
                WHERE b.user_id=u.id AND b.revoked_at IS NULL AND (b.expires_at IS NULL OR b.expires_at>now())
                  AND b.tenant_id=p_tenant AND (b.merchant_id IS NULL OR b.merchant_id=p_merchant))
       OR EXISTS (SELECT 1 FROM public.notification_default_owners(p_tenant,p_merchant) o
                    JOIN public.admin_role_permissions rp ON rp.role_key='merchant_owner' AND rp.permission_key=p_permission
                   WHERE o.user_id=u.id)))
$$;

-- Queues one job per subscribed, still authorised user and channel. The
-- dedupe key makes a replayed event or a repeated failure a no-op.
CREATE FUNCTION enqueue_merchant_notification(p_tenant uuid,p_merchant uuid,p_event text,p_permission text,p_dedupe text,p_payload jsonb) RETURNS integer
LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE inserted integer; now_at timestamptz:=clock_timestamp();
BEGIN
  WITH explicit AS (
    SELECT p.user_id,p.email_events,p.telegram_events,p.locale FROM public.notification_preferences p
     WHERE p.tenant_id=p_tenant AND p.merchant_id=p_merchant
  ), defaults AS (
    SELECT o.user_id,ARRAY['paid','underpaid','review','webhook_failed']::text[] AS email_events,'{}'::text[] AS telegram_events,coalesce(a.locale,'en') AS locale
      FROM public.notification_default_owners(p_tenant,p_merchant) o
      LEFT JOIN public.notification_account_settings a ON a.user_id=o.user_id
     WHERE NOT EXISTS (SELECT 1 FROM explicit e WHERE e.user_id=o.user_id)
  ), recipients AS (
    SELECT * FROM explicit UNION ALL SELECT * FROM defaults
  ), wanted AS (
    SELECT r.user_id,'email'::text AS channel,r.locale FROM recipients r WHERE p_event=ANY(r.email_events)
    UNION ALL
    SELECT r.user_id,'telegram'::text,r.locale FROM recipients r
      JOIN public.telegram_chat_links l ON l.user_id=r.user_id WHERE p_event=ANY(r.telegram_events)
  )
  INSERT INTO public.notification_jobs(id,user_id,tenant_id,merchant_id,channel,event,dedupe_key,payload,locale,status,available_at,created_at,updated_at)
  SELECT gen_random_uuid(),w.user_id,p_tenant,p_merchant,w.channel,p_event,p_dedupe,p_payload,w.locale,'pending',now_at,now_at,now_at
    FROM wanted w WHERE public.notification_recipient_allowed(w.user_id,p_tenant,p_merchant,p_permission)
  ON CONFLICT (user_id,channel,dedupe_key) DO NOTHING;
  GET DIAGNOSTICS inserted=ROW_COUNT;
  RETURN inserted;
END $$;

CREATE FUNCTION notify_on_callback_event() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE kind text; scale smallint; merchant_name text;
BEGIN
  kind := CASE NEW.event_type WHEN 'payment.settled' THEN 'paid' WHEN 'payment.partially_paid' THEN 'underpaid' WHEN 'payment.needs_review' THEN 'review' END;
  IF kind IS NULL OR NEW.intent_id IS NULL THEN RETURN NEW; END IF;
  BEGIN
    SELECT i.currency_scale INTO scale FROM public.payment_intents i WHERE i.id=NEW.intent_id AND i.tenant_id=NEW.tenant_id;
    SELECT m.display_name INTO merchant_name FROM public.merchants m WHERE m.id=NEW.merchant_id AND m.tenant_id=NEW.tenant_id;
    PERFORM public.enqueue_merchant_notification(NEW.tenant_id,NEW.merchant_id,kind,'payments:read',kind||':'||NEW.id::text,
      jsonb_strip_nulls(jsonb_build_object(
        'merchant',left(merchant_name,200),
        'intent_id',NEW.intent_id,
        'order_id',left(NEW.canonical_payload#>>'{payment_intent,merchant_order_id}',128),
        'amount_minor',left(NEW.canonical_payload#>>'{payment_intent,amount_minor}',80),
        'currency',left(NEW.canonical_payload#>>'{payment_intent,currency}',16),
        'currency_scale',scale,
        'network',left(NEW.canonical_payload#>>'{settlement,network}',64),
        'livemode',coalesce((NEW.canonical_payload->>'livemode')::boolean,true),
        'occurred_at',NEW.occurred_at)));
  EXCEPTION WHEN OTHERS THEN
    RAISE WARNING 'notification fan-out skipped for callback event % (%)',NEW.id,SQLSTATE;
  END;
  RETURN NEW;
END $$;
CREATE TRIGGER callback_events_notify AFTER INSERT ON callback_events FOR EACH ROW
  WHEN (NEW.event_type IN ('payment.settled','payment.partially_paid','payment.needs_review'))
  EXECUTE FUNCTION notify_on_callback_event();

-- A delivery that exhausted its retries. One notice per endpoint and UTC hour,
-- so an endpoint that is down does not flood the inbox. Only the host of the
-- endpoint is named (never userinfo, path or query).
CREATE FUNCTION notify_on_callback_dead_letter() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE item record;
BEGIN
  BEGIN
    SELECT ce.merchant_id,ce.event_type,ce.canonical_payload#>>'{payment_intent,merchant_order_id}' AS order_id,w.id AS endpoint_id,
           substring(w.endpoint_url from '^[A-Za-z][A-Za-z0-9+.-]*://(?:[^/?#@]*@)?([^/:?#]+)') AS host,m.display_name
      INTO item
      FROM public.callback_events ce
      JOIN public.webhook_endpoints w ON w.id=NEW.endpoint_id AND w.tenant_id=NEW.tenant_id AND w.merchant_id=ce.merchant_id
      JOIN public.merchants m ON m.id=ce.merchant_id AND m.tenant_id=ce.tenant_id
     WHERE ce.id=NEW.callback_event_id AND ce.tenant_id=NEW.tenant_id;
    IF FOUND THEN
      PERFORM public.enqueue_merchant_notification(NEW.tenant_id,item.merchant_id,'webhook_failed','webhooks:read',
        'webhook_failed:'||item.endpoint_id::text||':'||to_char(clock_timestamp() AT TIME ZONE 'UTC','YYYYMMDDHH24'),
        jsonb_strip_nulls(jsonb_build_object(
          'merchant',left(item.display_name,200),
          'endpoint_host',left(item.host,253),
          'event_type',left(item.event_type,64),
          'order_id',left(item.order_id,128),
          'http_status',NEW.last_http_status,
          'occurred_at',clock_timestamp())));
    END IF;
  EXCEPTION WHEN OTHERS THEN
    RAISE WARNING 'notification fan-out skipped for callback delivery % (%)',NEW.id,SQLSTATE;
  END;
  RETURN NEW;
END $$;
CREATE TRIGGER callback_deliveries_notify_dead_letter AFTER UPDATE OF status ON callback_deliveries FOR EACH ROW
  WHEN (NEW.status='dead_letter' AND OLD.status IS DISTINCT FROM 'dead_letter')
  EXECUTE FUNCTION notify_on_callback_dead_letter();

-- Sign-in alert for the current admin's own, just created session. The
-- locale is the browser's, used until the user saves settings.
CREATE FUNCTION enqueue_current_admin_sign_in_notification(p_session uuid,p_locale text) RETURNS integer
LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE actor uuid:=nullif(current_setting('app.admin_user_id',true),'')::uuid; now_at timestamptz:=clock_timestamp();
  started timestamptz; device text; wants_email boolean; wants_telegram boolean; chosen text; inserted integer;
BEGIN
  SELECT x.created_at,coalesce(x.device_label,'') INTO started,device FROM public.admin_sessions x
   WHERE x.id=p_session AND x.user_id=actor AND x.purpose='admin' AND x.revoked_at IS NULL AND x.created_at>now_at-interval '10 minutes';
  IF NOT FOUND THEN RETURN 0; END IF;
  SELECT a.sign_in_email,a.sign_in_telegram,a.locale INTO wants_email,wants_telegram,chosen
    FROM public.notification_account_settings a WHERE a.user_id=actor;
  chosen := coalesce(chosen,CASE WHEN p_locale IN ('en','ru','zh','es','fr','de') THEN p_locale ELSE 'en' END);
  INSERT INTO public.notification_jobs(id,user_id,channel,event,dedupe_key,payload,locale,status,available_at,created_at,updated_at)
  SELECT gen_random_uuid(),actor,v.channel,'sign_in','sign_in:'||p_session::text,
         jsonb_build_object('device',device,'signed_in_at',started),chosen,'pending',now_at,now_at,now_at
    FROM (VALUES ('email',coalesce(wants_email,true)),
                 ('telegram',coalesce(wants_telegram,true) AND EXISTS (SELECT 1 FROM public.telegram_chat_links l WHERE l.user_id=actor))) v(channel,wanted)
   WHERE v.wanted
  ON CONFLICT (user_id,channel,dedupe_key) DO NOTHING;
  GET DIAGNOSTICS inserted=ROW_COUNT;
  RETURN inserted;
END $$;

-- The current admin's settings; merchant columns are filled for a merchant
-- the user may read payments of. configured=false means the defaults apply.
CREATE FUNCTION current_admin_notification_settings(p_tenant uuid,p_merchant uuid)
RETURNS TABLE(email text,telegram_linked boolean,sign_in_email boolean,sign_in_telegram boolean,locale text,
              merchant_allowed boolean,webhooks_allowed boolean,configured boolean,email_events text[],telegram_events text[])
LANGUAGE plpgsql STABLE SECURITY DEFINER
SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE actor uuid:=nullif(current_setting('app.admin_user_id',true),'')::uuid; allowed boolean:=false; pref record;
BEGIN
  IF actor IS NULL OR NOT EXISTS (SELECT 1 FROM public.admin_users u WHERE u.id=actor AND u.status='active') THEN
    RAISE EXCEPTION 'not an active admin' USING ERRCODE='OCP01';
  END IF;
  IF p_merchant IS NOT NULL THEN
    allowed := public.notification_recipient_allowed(actor,p_tenant,p_merchant,'payments:read');
  END IF;
  SELECT p.email_events,p.telegram_events INTO pref FROM public.notification_preferences p
   WHERE allowed AND p.user_id=actor AND p.tenant_id=p_tenant AND p.merchant_id=p_merchant;
  RETURN QUERY
  SELECT coalesce(u.email,''),
         EXISTS (SELECT 1 FROM public.telegram_chat_links l WHERE l.user_id=actor),
         coalesce(a.sign_in_email,true),coalesce(a.sign_in_telegram,true),coalesce(a.locale,''),
         allowed,
         allowed AND public.notification_recipient_allowed(actor,p_tenant,p_merchant,'webhooks:read'),
         pref.email_events IS NOT NULL,
         CASE WHEN NOT allowed THEN '{}'::text[]
              WHEN pref.email_events IS NOT NULL THEN pref.email_events
              WHEN EXISTS (SELECT 1 FROM public.notification_default_owners(p_tenant,p_merchant) o WHERE o.user_id=actor) THEN ARRAY['paid','underpaid','review','webhook_failed']::text[]
              ELSE '{}'::text[] END,
         CASE WHEN allowed AND pref.telegram_events IS NOT NULL THEN pref.telegram_events ELSE '{}'::text[] END
    FROM public.admin_users u LEFT JOIN public.notification_account_settings a ON a.user_id=u.id
   WHERE u.id=actor;
END $$;

-- Saves the current admin's events for one merchant. A webhook event needs
-- webhooks:read as well. Errors: OCP01 (no access), OCP02 (invalid input).
CREATE FUNCTION set_current_admin_notification_preferences(p_tenant uuid,p_merchant uuid,p_email text[],p_telegram text[],p_locale text,p_at timestamptz) RETURNS void
LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE actor uuid:=nullif(current_setting('app.admin_user_id',true),'')::uuid;
  allowed_events text[]:=ARRAY['paid','underpaid','review','webhook_failed']::text[];
BEGIN
  IF actor IS NULL OR p_merchant IS NULL OR NOT public.notification_recipient_allowed(actor,p_tenant,p_merchant,'payments:read') THEN
    RAISE EXCEPTION 'notification preferences need payments access' USING ERRCODE='OCP01';
  END IF;
  IF p_email IS NULL OR p_telegram IS NULL OR NOT (p_email <@ allowed_events) OR NOT (p_telegram <@ allowed_events)
     OR cardinality(p_email)>4 OR cardinality(p_telegram)>4 OR p_locale IS NULL OR p_locale NOT IN ('en','ru','zh','es','fr','de') THEN
    RAISE EXCEPTION 'invalid notification preferences' USING ERRCODE='OCP02';
  END IF;
  IF ('webhook_failed'=ANY(p_email) OR 'webhook_failed'=ANY(p_telegram))
     AND NOT public.notification_recipient_allowed(actor,p_tenant,p_merchant,'webhooks:read') THEN
    RAISE EXCEPTION 'webhook notifications need webhook access' USING ERRCODE='OCP01';
  END IF;
  INSERT INTO public.notification_preferences(user_id,tenant_id,merchant_id,email_events,telegram_events,locale,updated_at)
  VALUES(actor,p_tenant,p_merchant,
         ARRAY(SELECT DISTINCT e FROM unnest(p_email) e ORDER BY e),ARRAY(SELECT DISTINCT e FROM unnest(p_telegram) e ORDER BY e),p_locale,p_at)
  ON CONFLICT (user_id,merchant_id) DO UPDATE SET email_events=EXCLUDED.email_events,telegram_events=EXCLUDED.telegram_events,
    locale=EXCLUDED.locale,updated_at=EXCLUDED.updated_at,version=notification_preferences.version+1;
  INSERT INTO public.notification_account_settings(user_id,sign_in_email,sign_in_telegram,locale,updated_at)
  VALUES(actor,true,true,p_locale,p_at)
  ON CONFLICT (user_id) DO UPDATE SET locale=EXCLUDED.locale,updated_at=EXCLUDED.updated_at;
  UPDATE public.notification_preferences SET locale=p_locale,updated_at=p_at,version=version+1
   WHERE user_id=actor AND locale<>p_locale;
END $$;

CREATE FUNCTION set_current_admin_account_notifications(p_sign_in_email boolean,p_sign_in_telegram boolean,p_locale text,p_at timestamptz) RETURNS void
LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE actor uuid:=nullif(current_setting('app.admin_user_id',true),'')::uuid;
BEGIN
  IF actor IS NULL OR NOT EXISTS (SELECT 1 FROM public.admin_users u WHERE u.id=actor AND u.status='active') THEN
    RAISE EXCEPTION 'not an active admin' USING ERRCODE='OCP01';
  END IF;
  IF p_sign_in_email IS NULL OR p_sign_in_telegram IS NULL OR p_locale IS NULL OR p_locale NOT IN ('en','ru','zh','es','fr','de') THEN
    RAISE EXCEPTION 'invalid account notification settings' USING ERRCODE='OCP02';
  END IF;
  INSERT INTO public.notification_account_settings(user_id,sign_in_email,sign_in_telegram,locale,updated_at)
  VALUES(actor,p_sign_in_email,p_sign_in_telegram,p_locale,p_at)
  ON CONFLICT (user_id) DO UPDATE SET sign_in_email=EXCLUDED.sign_in_email,sign_in_telegram=EXCLUDED.sign_in_telegram,
    locale=EXCLUDED.locale,updated_at=EXCLUDED.updated_at;
  -- One language for everything the user receives.
  UPDATE public.notification_preferences SET locale=p_locale,updated_at=p_at,version=version+1
   WHERE user_id=actor AND locale<>p_locale;
END $$;

-- A new one-time code replaces the user's unused ones.
CREATE FUNCTION create_current_admin_telegram_link_code(p_code_hash bytea,p_at timestamptz,p_expires_at timestamptz) RETURNS void
LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE actor uuid:=nullif(current_setting('app.admin_user_id',true),'')::uuid;
BEGIN
  IF actor IS NULL OR NOT EXISTS (SELECT 1 FROM public.admin_users u WHERE u.id=actor AND u.status='active') THEN
    RAISE EXCEPTION 'not an active admin' USING ERRCODE='OCP01';
  END IF;
  IF p_code_hash IS NULL OR octet_length(p_code_hash)<>32 OR p_expires_at<=p_at OR p_expires_at>p_at+interval '30 minutes' THEN
    RAISE EXCEPTION 'invalid telegram link code' USING ERRCODE='OCP02';
  END IF;
  DELETE FROM public.telegram_link_codes c WHERE (c.user_id=actor AND c.used_at IS NULL) OR c.expires_at<p_at-interval '1 day';
  INSERT INTO public.telegram_link_codes(code_hash,user_id,created_at,expires_at) VALUES(p_code_hash,actor,p_at,p_expires_at);
END $$;

CREATE FUNCTION unlink_current_admin_telegram() RETURNS boolean
LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE actor uuid:=nullif(current_setting('app.admin_user_id',true),'')::uuid;
BEGIN
  IF actor IS NULL THEN RAISE EXCEPTION 'not an active admin' USING ERRCODE='OCP01'; END IF;
  DELETE FROM public.telegram_link_codes WHERE user_id=actor AND used_at IS NULL;
  DELETE FROM public.telegram_chat_links WHERE user_id=actor;
  RETURN FOUND;
END $$;

-- Worker: consumes a one-time code sent to the bot and links the chat.
-- Returns the user's saved locale ('' when none) for the reply, or NULL for
-- an unknown, expired or someone else's used code. Audited as the user's own
-- action.
CREATE FUNCTION link_telegram_chat(p_code_hash bytea,p_chat_id bigint) RETURNS text
LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE now_at timestamptz:=clock_timestamp(); owner uuid; chosen text;
BEGIN
  IF p_code_hash IS NULL OR octet_length(p_code_hash)<>32 OR p_chat_id IS NULL OR p_chat_id<=0 THEN RETURN NULL; END IF;
  UPDATE public.telegram_link_codes c SET used_at=now_at
    FROM public.admin_users u
   WHERE c.code_hash=p_code_hash AND c.used_at IS NULL AND c.expires_at>now_at AND u.id=c.user_id AND u.status='active'
  RETURNING c.user_id INTO owner;
  IF owner IS NULL THEN
    -- A replayed update from the chat that already used this code is a
    -- success, so a restart never answers "expired" to a linked user.
    SELECT a.locale INTO chosen FROM public.telegram_link_codes c
      JOIN public.telegram_chat_links l ON l.user_id=c.user_id AND l.chat_id=p_chat_id
      LEFT JOIN public.notification_account_settings a ON a.user_id=c.user_id
     WHERE c.code_hash=p_code_hash AND c.used_at>now_at-interval '1 day';
    IF FOUND THEN RETURN coalesce(chosen,''); END IF;
    RETURN NULL;
  END IF;
  INSERT INTO public.telegram_chat_links(user_id,chat_id,linked_at) VALUES(owner,p_chat_id,now_at)
  ON CONFLICT (user_id) DO UPDATE SET chat_id=EXCLUDED.chat_id,linked_at=EXCLUDED.linked_at;
  PERFORM public.append_admin_audit(gen_random_uuid(),NULL,NULL,owner,NULL,'admin.notifications.telegram_linked','telegram_chat_link',owner::text,
    encode(sha256(p_code_hash),'hex'),'Telegram chat linked with a one-time code from the cabinet',NULL,NULL,'{}'::jsonb,NULL,NULL,now_at);
  SELECT a.locale INTO chosen FROM public.notification_account_settings a WHERE a.user_id=owner;
  RETURN coalesce(chosen,'');
END $$;

CREATE FUNCTION telegram_update_offset() RETURNS bigint
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path=pg_catalog,public SET row_security=off AS $$
  SELECT coalesce((SELECT next_offset FROM public.telegram_update_state WHERE singleton),0)
$$;

-- Moves the offset forward only, so a stale poller cannot replay updates.
CREATE FUNCTION advance_telegram_update_offset(p_offset bigint) RETURNS void
LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,public SET row_security=off AS $$
BEGIN
  IF p_offset IS NULL OR p_offset<0 THEN RAISE EXCEPTION 'invalid telegram offset' USING ERRCODE='OCP02'; END IF;
  INSERT INTO public.telegram_update_state(singleton,next_offset,updated_at) VALUES(true,p_offset,clock_timestamp())
  ON CONFLICT (singleton) DO UPDATE SET next_offset=greatest(telegram_update_state.next_offset,EXCLUDED.next_offset),updated_at=EXCLUDED.updated_at;
END $$;

-- Worker: leases one due job with its recipient resolved now. A job whose
-- user lost access, was disabled or unlinked Telegram ends as dead. Finished
-- jobs older than 30 days are purged on the way.
CREATE FUNCTION claim_notification_job(p_worker uuid,p_lease_seconds integer)
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
    -- A job whose leases keep expiring (a send that never returns) ends
    -- here instead of being leased forever.
    IF job.attempt_count>=20 THEN
      UPDATE public.notification_jobs SET status='dead',last_error_code='attempts_exhausted',lease_owner=NULL,lease_token=NULL,lease_until=NULL,updated_at=now_at WHERE notification_jobs.id=job.id;
      CONTINUE;
    END IF;
    recipient_email := NULL; recipient_chat := NULL;
    IF job.merchant_id IS NULL OR public.notification_recipient_allowed(job.user_id,job.tenant_id,job.merchant_id,
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

CREATE FUNCTION complete_notification_job(p_id uuid,p_lease uuid,p_provider_ref text) RETURNS boolean
LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,public SET row_security=off AS $$
BEGIN
  IF p_provider_ref IS NULL OR length(p_provider_ref) NOT BETWEEN 1 AND 255 THEN RETURN false; END IF;
  UPDATE public.notification_jobs SET status='sent',provider_ref=p_provider_ref,last_error_code=NULL,lease_owner=NULL,lease_token=NULL,lease_until=NULL,updated_at=clock_timestamp()
   WHERE id=p_id AND status='leased' AND lease_token=p_lease AND lease_until>=clock_timestamp();
  RETURN FOUND;
END $$;

-- A retryable failure waits p_retry_seconds; the last allowed attempt or a
-- permanent failure (p_max_attempts=attempt_count) ends the job as dead.
CREATE FUNCTION fail_notification_job(p_id uuid,p_lease uuid,p_error_code text,p_max_attempts integer,p_retry_seconds integer) RETURNS boolean
LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,public SET row_security=off AS $$
BEGIN
  IF p_error_code IS NULL OR p_error_code !~ '^[a-z0-9_]{1,80}$' OR p_max_attempts IS NULL OR p_max_attempts<1 OR p_max_attempts>20
     OR p_retry_seconds IS NULL OR p_retry_seconds<1 OR p_retry_seconds>86400 THEN RETURN false; END IF;
  UPDATE public.notification_jobs SET
    status=CASE WHEN attempt_count>=p_max_attempts THEN 'dead' ELSE 'retry' END,
    available_at=clock_timestamp()+p_retry_seconds*interval '1 second',
    last_error_code=p_error_code,lease_owner=NULL,lease_token=NULL,lease_until=NULL,updated_at=clock_timestamp()
   WHERE id=p_id AND status='leased' AND lease_token=p_lease AND lease_until>=clock_timestamp();
  RETURN FOUND;
END $$;

REVOKE ALL ON FUNCTION notification_recipient_allowed(uuid,uuid,uuid,text) FROM PUBLIC;
REVOKE ALL ON FUNCTION notification_default_owners(uuid,uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION enqueue_merchant_notification(uuid,uuid,text,text,text,jsonb) FROM PUBLIC;
REVOKE ALL ON FUNCTION notify_on_callback_event() FROM PUBLIC;
REVOKE ALL ON FUNCTION notify_on_callback_dead_letter() FROM PUBLIC;
REVOKE ALL ON FUNCTION enqueue_current_admin_sign_in_notification(uuid,text) FROM PUBLIC;
REVOKE ALL ON FUNCTION current_admin_notification_settings(uuid,uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION set_current_admin_notification_preferences(uuid,uuid,text[],text[],text,timestamptz) FROM PUBLIC;
REVOKE ALL ON FUNCTION set_current_admin_account_notifications(boolean,boolean,text,timestamptz) FROM PUBLIC;
REVOKE ALL ON FUNCTION create_current_admin_telegram_link_code(bytea,timestamptz,timestamptz) FROM PUBLIC;
REVOKE ALL ON FUNCTION unlink_current_admin_telegram() FROM PUBLIC;
REVOKE ALL ON FUNCTION link_telegram_chat(bytea,bigint) FROM PUBLIC;
REVOKE ALL ON FUNCTION telegram_update_offset() FROM PUBLIC;
REVOKE ALL ON FUNCTION advance_telegram_update_offset(bigint) FROM PUBLIC;
REVOKE ALL ON FUNCTION claim_notification_job(uuid,integer) FROM PUBLIC;
REVOKE ALL ON FUNCTION complete_notification_job(uuid,uuid,text) FROM PUBLIC;
REVOKE ALL ON FUNCTION fail_notification_job(uuid,uuid,text,integer,integer) FROM PUBLIC;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='merchant_admin_runtime') THEN
    GRANT EXECUTE ON FUNCTION enqueue_current_admin_sign_in_notification(uuid,text),
      current_admin_notification_settings(uuid,uuid),
      set_current_admin_notification_preferences(uuid,uuid,text[],text[],text,timestamptz),
      set_current_admin_account_notifications(boolean,boolean,text,timestamptz),
      create_current_admin_telegram_link_code(bytea,timestamptz,timestamptz),
      unlink_current_admin_telegram() TO merchant_admin_runtime;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='merchant_notification_worker') THEN
    GRANT EXECUTE ON FUNCTION claim_notification_job(uuid,integer),complete_notification_job(uuid,uuid,text),
      fail_notification_job(uuid,uuid,text,integer,integer),link_telegram_chat(bytea,bigint),
      telegram_update_offset(),advance_telegram_update_offset(bigint) TO merchant_notification_worker;
  END IF;
END $$;

COMMIT;
