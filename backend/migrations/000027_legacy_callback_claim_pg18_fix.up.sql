BEGIN;

CREATE OR REPLACE FUNCTION legacy_claim_callbacks(requested_worker text,requested_limit integer,requested_lease_seconds integer,requested_at timestamptz)
RETURNS TABLE(delivery_id uuid,lease_token uuid,fence bigint,protocol text,event_id uuid,target_url text,http_method text,content_type text,
 frozen_body bytea,credential_version_id uuid,callback_key_id text,attempt_count integer)
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE authoritative_at timestamptz:=clock_timestamp();
BEGIN
	IF length(requested_worker) NOT BETWEEN 1 AND 128 OR requested_limit NOT BETWEEN 1 AND 100 OR
	   requested_lease_seconds NOT BETWEEN 5 AND 300 THEN RETURN; END IF;
	UPDATE public.legacy_compat_callback_deliveries AS d
	   SET status='dead_letter',lease_owner=NULL,lease_token=NULL,lease_until=NULL,
	       last_error_code='lease_expired_attempt_limit',updated_at=authoritative_at
	 WHERE d.status='leased' AND d.lease_until<=authoritative_at AND d.attempt_count>=32;
	RETURN QUERY WITH candidates AS (
	  SELECT d.id FROM public.legacy_compat_callback_deliveries d WHERE
	   (d.status IN ('pending','retry') AND d.next_attempt_at<=authoritative_at OR d.status='leased' AND d.lease_until<=authoritative_at)
	   AND d.attempt_count<32 ORDER BY d.next_attempt_at,d.id FOR UPDATE SKIP LOCKED LIMIT requested_limit
	), updated AS (
	  UPDATE public.legacy_compat_callback_deliveries d SET status='leased',lease_owner=requested_worker,lease_token=gen_random_uuid(),
	   lease_until=authoritative_at+make_interval(secs=>requested_lease_seconds),fence=d.fence+1,attempt_count=d.attempt_count+1,updated_at=authoritative_at
	  FROM candidates c WHERE d.id=c.id RETURNING d.*
	) SELECT u.id,u.lease_token,u.fence,c.protocol,u.event_id,u.target_url,u.http_method,u.content_type,u.frozen_body,
	  u.credential_version_id,u.callback_key_id,u.attempt_count FROM updated u JOIN public.legacy_compat_configs c ON c.id=u.config_id;
END $$;

REVOKE EXECUTE ON FUNCTION legacy_claim_callbacks(text,integer,integer,timestamptz) FROM PUBLIC;
DO $grants$ BEGIN
 IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='legacy_compat_runtime') THEN
   GRANT EXECUTE ON FUNCTION legacy_claim_callbacks(text,integer,integer,timestamptz) TO legacy_compat_runtime;
 END IF;
END $grants$;

COMMIT;
