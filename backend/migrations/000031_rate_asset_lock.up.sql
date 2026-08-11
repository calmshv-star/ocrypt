BEGIN;

CREATE OR REPLACE FUNCTION rate_runtime_asset_active(requested_asset text)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,public
SET row_security=off
AS $$
DECLARE
  admitted boolean := false;
BEGIN
  IF requested_asset IS NULL OR requested_asset !~ '^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$' THEN
    RETURN false;
  END IF;
  SELECT status='active' INTO admitted
  FROM public.assets
  WHERE id=requested_asset
  FOR KEY SHARE;
  RETURN COALESCE(admitted,false);
END $$;

REVOKE ALL ON FUNCTION rate_runtime_asset_active(text) FROM PUBLIC;
DO $$ BEGIN
  IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='rate_runtime_worker') THEN
    GRANT EXECUTE ON FUNCTION rate_runtime_asset_active(text) TO rate_runtime_worker;
  END IF;
END $$;

COMMIT;
