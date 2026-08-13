BEGIN;

-- A merchant owner needs one effective, read-only financial inventory without
-- receiving direct SELECT on wallets, addresses, secrets, or mutable platform
-- control-plane tables. Address values and signer references never cross this
-- contract; only pool capacity counts are returned.
CREATE FUNCTION admin_financial_settings_inventory(requested_tenant uuid, requested_merchant uuid)
RETURNS TABLE(
  merchant_currency text,
  route_currency text,
  chain_id text,
  asset_id text,
  asset_symbol text,
  asset_status text,
  chain_status text,
  route_status text,
  wallet_count bigint,
  active_wallet_count bigint,
  address_count bigint,
  available_address_count bigint,
  assigned_address_count bigint,
  quarantined_address_count bigint
)
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path=pg_catalog,public SET row_security=off
AS $$
  WITH merchant AS (
    SELECT m.settlement_currency::text AS settlement_currency
    FROM public.merchants m
    JOIN public.tenants t ON t.id=m.tenant_id AND t.status='active'
    WHERE m.tenant_id=requested_tenant AND m.id=requested_merchant
      AND m.status='active'
  ), admitted_routes AS (
    -- Legacy compatibility is the production route contract currently used by
    -- Showy. This exposes no protocol credential, callback key, PID or IP data.
    SELECT DISTINCT c.currency::text,c.chain_id,c.asset_id,c.status
    FROM public.legacy_compat_configs c
    WHERE c.tenant_id=requested_tenant AND c.merchant_id=requested_merchant
      AND c.sunset_at>clock_timestamp()
  ), wallet_capacity AS (
    SELECT w.chain_id,
      count(DISTINCT w.id)::bigint wallet_count,
      count(DISTINCT w.id) FILTER(WHERE w.status='active')::bigint active_wallet_count,
      count(a.id)::bigint address_count,
      count(a.id) FILTER(WHERE a.status='available')::bigint available_address_count,
      count(a.id) FILTER(WHERE a.status='assigned')::bigint assigned_address_count,
      count(a.id) FILTER(WHERE a.status='quarantined')::bigint quarantined_address_count
    FROM public.wallets w
    LEFT JOIN public.addresses a ON a.tenant_id=w.tenant_id AND a.wallet_id=w.id
    WHERE w.tenant_id=requested_tenant
      AND (w.merchant_id IS NULL OR w.merchant_id=requested_merchant)
    GROUP BY w.chain_id
  )
  SELECT m.settlement_currency,r.currency,r.chain_id,r.asset_id,a.symbol,
    a.status,c.status,r.status,
    coalesce(w.wallet_count,0),coalesce(w.active_wallet_count,0),
    coalesce(w.address_count,0),coalesce(w.available_address_count,0),
    coalesce(w.assigned_address_count,0),coalesce(w.quarantined_address_count,0)
  FROM merchant m
  JOIN admitted_routes r ON true
  JOIN public.chains c ON c.id=r.chain_id
  JOIN public.assets a ON a.id=r.asset_id AND a.chain_id=r.chain_id
  LEFT JOIN wallet_capacity w ON w.chain_id=r.chain_id
  ORDER BY r.currency,r.chain_id,r.asset_id
$$;

REVOKE ALL ON FUNCTION admin_financial_settings_inventory(uuid,uuid) FROM PUBLIC;
DO $$ BEGIN
  IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='merchant_admin_runtime') THEN
    GRANT EXECUTE ON FUNCTION admin_financial_settings_inventory(uuid,uuid) TO merchant_admin_runtime;
  END IF;
END $$;

COMMIT;
