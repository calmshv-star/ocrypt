BEGIN;

CREATE OR REPLACE FUNCTION rate_runtime_snapshot_current(
  requested_tenant uuid,
  requested_kind platform_config_kind,
  requested_key text,
  requested_snapshot uuid,
  requested_fence bigint
) RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,public
SET row_security=off
AS $$
DECLARE
  admitted boolean := false;
BEGIN
  IF requested_kind NOT IN ('rate_policy','rate_source') OR requested_key IS NULL OR
     requested_key !~ '^[a-z0-9](?:[a-z0-9._:/-]{0,126}[a-z0-9])?$' OR
     requested_snapshot IS NULL OR requested_fence<1 THEN
    RETURN false;
  END IF;
  SELECT true INTO admitted
  FROM public.platform_config_heads
  WHERE scope_id=public.platform_scope_uuid(requested_tenant)
    AND kind=requested_kind
    AND logical_key=requested_key
    AND snapshot_id=requested_snapshot
    AND fence_token=requested_fence
  FOR SHARE;
  RETURN COALESCE(admitted,false);
END $$;

REVOKE ALL ON FUNCTION rate_runtime_snapshot_current(uuid,platform_config_kind,text,uuid,bigint) FROM PUBLIC;

DO $$ BEGIN
  IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='rate_runtime_worker') THEN
    GRANT EXECUTE ON FUNCTION rate_runtime_snapshot_current(uuid,platform_config_kind,text,uuid,bigint) TO rate_runtime_worker;
  END IF;
  IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='merchant_plan_worker') THEN
    GRANT SELECT ON payment_matches,payment_match_aggregates TO merchant_plan_worker;
  END IF;
END $$;

COMMIT;
