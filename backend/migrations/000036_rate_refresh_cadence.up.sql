BEGIN;

-- A route may wake one stale pair, but it must not turn repeated checkout
-- attempts into an upstream-rate polling loop. Freshness is fixed at thirty
-- minutes here rather than delegated to the caller.
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

  -- The SECURITY DEFINER function still honours the forced runtime RLS policy.
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

  -- One request can advance a sleeping job. A failed job keeps at least a
  -- thirty-second backoff even if many clients request the same pair.
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

-- Existing installations have immutable activated snapshots. Replace their
-- heads with audited snapshots using a 30 minute poll interval and a small
-- five-minute expiry margin so a rate never disappears at the refresh edge.
DO $$
DECLARE
  item record;
  next_payload jsonb;
  next_version bigint;
  new_request_id uuid;
  new_snapshot_id uuid;
  new_activation_id uuid;
  requester uuid := '0198a100-0000-7000-8000-000000000003';
  approver uuid := '0198a100-0000-7000-8000-000000000004';
  now_at timestamptz;
BEGIN
  FOR item IN
    SELECT h.scope_id,h.kind,h.logical_key,h.snapshot_id,h.fence_token,
           s.version,s.payload,s.activated_by
    FROM platform_config_heads h
    JOIN platform_config_snapshots s ON s.id=h.snapshot_id
    WHERE h.scope_id=platform_scope_uuid(NULL)
      AND h.kind IN ('rate_source','rate_policy')
    ORDER BY h.kind,h.logical_key
    FOR UPDATE OF h
  LOOP
    next_payload := jsonb_set(item.payload,'{max_age_seconds}','2100'::jsonb,false);
    IF item.kind='rate_policy' THEN
      next_payload := jsonb_set(next_payload,'{poll_interval_seconds}','1800'::jsonb,true);
    END IF;
    IF next_payload=item.payload THEN
      CONTINUE;
    END IF;

    SELECT GREATEST(item.version,COALESCE(max(version),0))+1 INTO next_version
    FROM platform_config_change_requests
    WHERE scope_id=item.scope_id AND kind=item.kind AND logical_key=item.logical_key;
    new_request_id:=uuidv7();
    new_snapshot_id:=uuidv7();
    new_activation_id:=uuidv7();
    now_at:=clock_timestamp();

    INSERT INTO platform_config_change_requests(
      id,scope_id,kind,logical_key,version,based_on_version,payload,payload_hash,status,reason,
      requested_by,approved_by,scheduled_by,activated_by,requested_at,decided_at,scheduled_for,activated_at,
      created_at,updated_at,row_version)
    VALUES(new_request_id,item.scope_id,item.kind,item.logical_key,next_version,item.version,
      next_payload,digest(next_payload::text,'sha256'),'active',
      'Thirty minute background rate cadence with stale-only checkout wakeup',
      requester,approver,approver,approver,
      now_at,now_at,now_at,now_at,now_at,now_at,4);
    INSERT INTO platform_config_snapshots(
      id,scope_id,change_request_id,kind,logical_key,version,payload,payload_hash,activated_by,activated_at)
    VALUES(new_snapshot_id,item.scope_id,new_request_id,item.kind,item.logical_key,next_version,
      next_payload,digest(next_payload::text,'sha256'),approver,now_at);
    UPDATE platform_config_heads
    SET snapshot_id=new_snapshot_id,fence_token=item.fence_token+1,updated_at=now_at
    WHERE scope_id=item.scope_id AND kind=item.kind AND logical_key=item.logical_key;
    INSERT INTO platform_config_activations(
      id,scope_id,kind,logical_key,snapshot_id,fence_token,activation_type,actor_id,occurred_at)
    VALUES(new_activation_id,item.scope_id,item.kind,item.logical_key,new_snapshot_id,item.fence_token+1,
      'activate',approver,now_at);
  END LOOP;
END $$;

-- During the transition, honour the remaining lifetime of the existing tick.
-- Subsequent successful collections schedule themselves exactly 30 minutes out.
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
