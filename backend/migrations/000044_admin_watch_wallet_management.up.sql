BEGIN;

-- A merchant has one admitted watch-only wallet pool per chain and exactly one
-- address in that pool that may be allocated to new invoices. Historic
-- addresses are retained because already issued payment routes still refer to
-- them and the scanner continues to watch those routes through their grace
-- period.
DO $$ BEGIN
  IF EXISTS(
    SELECT 1 FROM public.wallets
    WHERE merchant_id IS NOT NULL AND custody_mode='watch_only'
    GROUP BY tenant_id,merchant_id,chain_id HAVING count(*)>1
  ) OR EXISTS(
    SELECT 1 FROM public.addresses
    WHERE purpose='deposit' AND status IN('available','assigned')
    GROUP BY wallet_id HAVING count(*)>1
  ) THEN
    RAISE EXCEPTION 'watch wallet inventory must be normalized before migration' USING ERRCODE='MP002';
  END IF;
END $$;

CREATE UNIQUE INDEX merchant_watch_wallet_chain_idx
  ON wallets(tenant_id,merchant_id,chain_id)
  WHERE merchant_id IS NOT NULL AND custody_mode='watch_only';

CREATE UNIQUE INDEX wallet_current_deposit_address_idx
  ON addresses(wallet_id)
  WHERE purpose='deposit' AND status IN('available','assigned');

CREATE FUNCTION admin_watch_wallet_inventory(requested_tenant uuid,requested_merchant uuid)
RETURNS TABLE(
  wallet_id uuid,
  chain_id text,
  chain_name text,
  address text,
  status text,
  version bigint
)
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path=pg_catalog,public SET row_security=off
AS $$
  SELECT w.id,w.chain_id,c.network_name,coalesce(current_address.display_address,''),w.status,w.version
  FROM public.wallets w
  JOIN public.chains c ON c.id=w.chain_id
  LEFT JOIN LATERAL (
    SELECT a.display_address
    FROM public.addresses a
    WHERE a.tenant_id=w.tenant_id AND a.wallet_id=w.id AND a.purpose='deposit'
      AND a.status IN('available','assigned')
    ORDER BY a.updated_at DESC,a.id DESC
    LIMIT 1
  ) current_address ON true
  WHERE w.tenant_id=requested_tenant AND w.merchant_id=requested_merchant
    AND current_setting('app.tenant_id',true)=requested_tenant::text
    AND requested_merchant=ANY(coalesce(nullif(current_setting('app.admin_merchant_ids',true),''),'{}')::uuid[])
    AND w.custody_mode='watch_only' AND w.signer_key_reference IS NULL
    AND EXISTS(SELECT 1 FROM public.platform_wallet_runtime_admission(w.tenant_id,w.id,w.chain_id))
  ORDER BY c.network_name,w.chain_id
$$;

CREATE FUNCTION admin_replace_watch_wallet_address(
  requested_tenant uuid,
  requested_merchant uuid,
  requested_actor uuid,
  requested_wallet uuid,
  requested_address_id uuid,
  requested_chain text,
  requested_canonical text,
  requested_display text,
  expected_wallet_version bigint,
  requested_reason text
)
RETURNS TABLE(
  wallet_id uuid,
  chain_id text,
  chain_name text,
  address text,
  status text,
  version bigint
)
LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,public SET row_security=off
AS $$
DECLARE
  target public.wallets%ROWTYPE;
  existing public.addresses%ROWTYPE;
  existing_found boolean:=false;
  changed boolean:=false;
BEGIN
  IF current_setting('app.tenant_id',true)<>requested_tenant::text
     OR current_setting('app.admin_user_id',true)<>requested_actor::text
     OR NOT (requested_merchant=ANY(coalesce(nullif(current_setting('app.admin_merchant_ids',true),''),'{}')::uuid[]))
     OR expected_wallet_version<1
     OR length(btrim(requested_reason)) NOT BETWEEN 3 AND 1000
     OR requested_display<>btrim(requested_display)
     OR requested_canonical<>btrim(requested_canonical)
     OR length(requested_display) NOT BETWEEN 8 AND 256
     OR length(requested_canonical) NOT BETWEEN 8 AND 256 THEN
    RAISE EXCEPTION 'watch wallet replacement rejected' USING ERRCODE='MP002';
  END IF;

  SELECT w.* INTO target
  FROM public.wallets w
  JOIN public.merchants m ON m.id=w.merchant_id AND m.tenant_id=w.tenant_id AND m.status='active'
  JOIN public.tenants t ON t.id=w.tenant_id AND t.status='active'
  JOIN public.chains c ON c.id=w.chain_id AND c.status='active'
  WHERE w.id=requested_wallet AND w.tenant_id=requested_tenant AND w.merchant_id=requested_merchant
    AND w.chain_id=requested_chain
    AND w.status='active' AND w.custody_mode='watch_only' AND w.signer_key_reference IS NULL
  FOR UPDATE OF w;
  IF NOT FOUND OR target.version<>expected_wallet_version
     OR NOT EXISTS(SELECT 1 FROM public.platform_wallet_runtime_admission(target.tenant_id,target.id,target.chain_id)) THEN
    RAISE EXCEPTION 'watch wallet changed or is not admitted' USING ERRCODE='40001';
  END IF;
  IF NOT EXISTS(
    SELECT 1 FROM public.scanner_cursors cursor
    WHERE cursor.chain_id=target.chain_id AND cursor.capability='normalized_transfers_v1'
      AND cursor.heartbeat_at>=clock_timestamp()-interval '2 minutes'
  ) THEN
    RAISE EXCEPTION 'watch wallet scanner is not ready' USING ERRCODE='55000';
  END IF;

  SELECT a.* INTO existing
  FROM public.addresses a
  WHERE a.chain_id=target.chain_id AND a.canonical_address=requested_canonical
  FOR UPDATE;
  existing_found:=FOUND;
  IF existing_found AND (existing.tenant_id<>requested_tenant OR existing.wallet_id<>target.id OR existing.purpose<>'deposit') THEN
    RAISE EXCEPTION 'receiving address already belongs to another wallet' USING ERRCODE='23505';
  END IF;
  IF existing_found AND existing.status='quarantined' THEN
    RAISE EXCEPTION 'quarantined receiving address cannot be reactivated' USING ERRCODE='55000';
  END IF;

  IF NOT existing_found OR existing.status NOT IN('available','assigned') THEN
    UPDATE public.addresses
       SET status='retired',updated_at=clock_timestamp(),version=version+1
     WHERE tenant_id=requested_tenant AND wallet_id=target.id AND purpose='deposit'
       AND status IN('available','assigned')
       AND (NOT existing_found OR id<>existing.id);

    IF existing_found THEN
      UPDATE public.addresses
         SET display_address=requested_display,status='available',updated_at=clock_timestamp(),version=version+1
       WHERE id=existing.id AND tenant_id=requested_tenant;
    ELSE
      INSERT INTO public.addresses(
        id,tenant_id,wallet_id,chain_id,canonical_address,display_address,purpose,status,
        created_at,updated_at,version
      ) VALUES(
        requested_address_id,requested_tenant,target.id,target.chain_id,requested_canonical,
        requested_display,'deposit','available',clock_timestamp(),clock_timestamp(),1
      );
    END IF;
    changed:=true;
  END IF;

  IF changed THEN
    UPDATE public.wallets
       SET updated_at=clock_timestamp(),version=version+1
     WHERE id=target.id AND tenant_id=requested_tenant;
  END IF;

  RETURN QUERY
  SELECT inventory.wallet_id,inventory.chain_id,inventory.chain_name,
         inventory.address,inventory.status,inventory.version
  FROM public.admin_watch_wallet_inventory(requested_tenant,requested_merchant) inventory
  WHERE inventory.wallet_id=target.id;
END $$;

REVOKE ALL ON FUNCTION admin_watch_wallet_inventory(uuid,uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION admin_replace_watch_wallet_address(uuid,uuid,uuid,uuid,uuid,text,text,text,bigint,text) FROM PUBLIC;
DO $$ BEGIN
  IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='merchant_admin_runtime') THEN
    GRANT EXECUTE ON FUNCTION admin_watch_wallet_inventory(uuid,uuid) TO merchant_admin_runtime;
    GRANT EXECUTE ON FUNCTION admin_replace_watch_wallet_address(uuid,uuid,uuid,uuid,uuid,text,text,text,bigint,text) TO merchant_admin_runtime;
  END IF;
END $$;

COMMIT;
