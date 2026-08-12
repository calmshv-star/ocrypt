BEGIN;

CREATE OR REPLACE FUNCTION request_rate_refresh_if_stale(
  requested_asset text,
  requested_currency text
) RETURNS TABLE(fresh boolean, accepted boolean)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,public
AS $$
DECLARE
  now_at timestamptz := clock_timestamp();
  normalized_currency text := upper(btrim(requested_currency));
  bound_policy text;
  latest_created_at timestamptz;
  latest_observed_at timestamptz;
  latest_max_age integer;
  changed integer := 0;
BEGIN
  IF requested_asset IS NULL
     OR requested_asset !~ '^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$'
     OR normalized_currency !~ '^[A-Z]{3}$' THEN
    RAISE EXCEPTION 'invalid rate refresh pair' USING ERRCODE='22023';
  END IF;

  PERFORM set_config('app.rate_worker_id','0198a100-0000-7000-8000-000000000050',true);
  PERFORM set_config('app.rate_runtime_global','true',true);
  PERFORM set_config('app.rate_runtime_tenants','',true);

  SELECT b.policy_key INTO bound_policy
  FROM rate_runtime_pair_bindings b
  WHERE b.scope_id=platform_scope_uuid(NULL)
    AND b.base_asset=requested_asset
    AND b.quote_asset=normalized_currency::char(3);

  IF NOT FOUND THEN
    RETURN QUERY SELECT false,false;
    RETURN;
  END IF;

  SELECT t.created_at,t.observed_at,t.max_age_seconds
  INTO latest_created_at,latest_observed_at,latest_max_age
  FROM asset_rate_ticks t
  WHERE t.asset_id=requested_asset
    AND t.fiat_currency=normalized_currency::char(3)
    AND t.status='active'
  ORDER BY t.observed_at DESC,t.id DESC
  LIMIT 1;

  IF FOUND
     AND latest_created_at>now_at-interval '30 minutes'
     AND latest_observed_at+make_interval(secs=>latest_max_age)>now_at THEN
    RETURN QUERY SELECT true,true;
    RETURN;
  END IF;

  UPDATE rate_runtime_jobs j
  SET next_attempt_at=LEAST(j.next_attempt_at,now_at),updated_at=now_at
  WHERE j.scope_id=platform_scope_uuid(NULL)
    AND j.policy_key=bound_policy
    AND j.status='active'
    AND (j.lease_until IS NULL OR j.lease_until<=now_at)
    AND j.next_attempt_at>now_at
    AND j.updated_at<=now_at-interval '30 seconds';
  GET DIAGNOSTICS changed=ROW_COUNT;

  IF changed>0 THEN
    PERFORM pg_notify('ocrypt_rate_refresh',bound_policy);
  END IF;

  RETURN QUERY SELECT false,EXISTS(
    SELECT 1 FROM rate_runtime_jobs j
    WHERE j.scope_id=platform_scope_uuid(NULL)
      AND j.policy_key=bound_policy
      AND j.status='active'
  );
END $$;

REVOKE ALL ON FUNCTION request_rate_refresh_if_stale(text,text) FROM PUBLIC;
DO $$ BEGIN
  IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='merchant_api_runtime') THEN
    GRANT EXECUTE ON FUNCTION request_rate_refresh_if_stale(text,text) TO merchant_api_runtime;
  END IF;
END $$;

UPDATE rate_runtime_jobs j
SET next_attempt_at=GREATEST(clock_timestamp(),COALESCE((
      SELECT t.created_at+make_interval(secs=>LEAST(t.max_age_seconds,1800))
      FROM rate_runtime_pair_bindings b
      JOIN asset_rate_ticks t ON t.asset_id=b.base_asset AND t.fiat_currency=b.quote_asset AND t.status='active'
      WHERE b.scope_id=j.scope_id AND b.policy_key=j.policy_key
      ORDER BY t.observed_at DESC,t.id DESC LIMIT 1
    ),clock_timestamp())),
    updated_at=clock_timestamp()
WHERE j.scope_id=platform_scope_uuid(NULL)
  AND j.status='active'
  AND j.lease_owner IS NULL;

COMMIT;
