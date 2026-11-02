BEGIN;

-- Counts active wallets that would let the operator hold or sign funds: any
-- custody mode other than watch_only, or any signer reference. The
-- noncustodial (SaaS) edition keeps /readyz closed while this is not zero.
-- Wallets are tenant-isolated under forced row-level security, so the count
-- visits each tenant under its own app.tenant_id and restores the caller's.
CREATE FUNCTION public.platform_custodial_wallet_count() RETURNS bigint
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, public AS $$
DECLARE
  tenant_row record;
  caller_tenant text := COALESCE(current_setting('app.tenant_id', true), '');
  tenant_count bigint;
  total bigint := 0;
BEGIN
  FOR tenant_row IN SELECT id FROM public.tenants LOOP
    PERFORM set_config('app.tenant_id', tenant_row.id::text, true);
    -- The explicit tenant filter keeps the count exact when the owner bypasses
    -- row-level security (for example a superuser in a standalone install).
    SELECT count(*) INTO tenant_count FROM public.wallets
    WHERE tenant_id = tenant_row.id AND status = 'active'
      AND (custody_mode <> 'watch_only' OR signer_key_reference IS NOT NULL);
    total := total + tenant_count;
  END LOOP;
  PERFORM set_config('app.tenant_id', caller_tenant, true);
  RETURN total;
END $$;

REVOKE ALL ON FUNCTION public.platform_custodial_wallet_count() FROM PUBLIC;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='merchant_api_runtime') THEN
    GRANT EXECUTE ON FUNCTION public.platform_custodial_wallet_count() TO merchant_api_runtime;
  END IF;
END $$;

COMMIT;
