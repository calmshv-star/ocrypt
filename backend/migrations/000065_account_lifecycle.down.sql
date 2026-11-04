BEGIN;

DROP FUNCTION IF EXISTS delete_current_admin_account(uuid[],timestamptz);
DROP FUNCTION IF EXISTS current_admin_account_export();
DROP FUNCTION IF EXISTS current_admin_sole_owned_merchants();
DROP FUNCTION IF EXISTS sync_current_admin_email(text,timestamptz);
-- The claim of 000063 (no fixed recipient).
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

DELETE FROM notification_jobs WHERE event IN ('email_changed','account_deleted');
ALTER TABLE notification_jobs DROP CONSTRAINT notification_jobs_recipient_check;
ALTER TABLE notification_jobs DROP CONSTRAINT notification_jobs_check;
ALTER TABLE notification_jobs ADD CONSTRAINT notification_jobs_check CHECK ((event = 'sign_in') = (merchant_id IS NULL));
ALTER TABLE notification_jobs DROP CONSTRAINT notification_jobs_event_check;
ALTER TABLE notification_jobs ADD CONSTRAINT notification_jobs_event_check
  CHECK (event IN ('paid','underpaid','review','webhook_failed','sign_in','sanctioned'));
ALTER TABLE notification_jobs DROP COLUMN recipient_email;

COMMIT;
