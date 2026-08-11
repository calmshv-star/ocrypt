BEGIN;

ALTER TABLE rate_quotes
  ADD COLUMN platform_environment_snapshot_id uuid REFERENCES platform_config_snapshots(id),
  ADD COLUMN platform_environment_fence bigint CHECK(platform_environment_fence>0),
  ADD COLUMN platform_chain_snapshot_id uuid REFERENCES platform_config_snapshots(id),
  ADD COLUMN platform_chain_fence bigint CHECK(platform_chain_fence>0),
  ADD COLUMN platform_asset_snapshot_id uuid REFERENCES platform_config_snapshots(id),
  ADD COLUMN platform_asset_fence bigint CHECK(platform_asset_fence>0),
  ADD COLUMN platform_finality_snapshot_id uuid REFERENCES platform_config_snapshots(id),
  ADD COLUMN platform_finality_fence bigint CHECK(platform_finality_fence>0),
  ADD COLUMN runtime_required_finality bigint CHECK(runtime_required_finality>=0),
  ADD CONSTRAINT rate_quote_runtime_evidence_complete CHECK (
    (platform_environment_snapshot_id IS NULL AND platform_environment_fence IS NULL AND
     platform_chain_snapshot_id IS NULL AND platform_chain_fence IS NULL AND
     platform_asset_snapshot_id IS NULL AND platform_asset_fence IS NULL AND
     platform_finality_snapshot_id IS NULL AND platform_finality_fence IS NULL AND
     runtime_required_finality IS NULL)
    OR
    (platform_environment_snapshot_id IS NOT NULL AND platform_environment_fence IS NOT NULL AND
     platform_chain_snapshot_id IS NOT NULL AND platform_chain_fence IS NOT NULL AND
     platform_asset_snapshot_id IS NOT NULL AND platform_asset_fence IS NOT NULL AND
     platform_finality_snapshot_id IS NOT NULL AND platform_finality_fence IS NOT NULL AND
     runtime_required_finality IS NOT NULL)
  );

ALTER TABLE address_assignments
  ADD COLUMN platform_wallet_pool_snapshot_id uuid REFERENCES platform_config_snapshots(id),
  ADD COLUMN platform_wallet_pool_fence bigint CHECK(platform_wallet_pool_fence>0),
  ADD CONSTRAINT address_assignment_runtime_evidence_complete CHECK (
    (platform_wallet_pool_snapshot_id IS NULL)=(platform_wallet_pool_fence IS NULL)
  );

CREATE TABLE scanner_runtime_config_evidence (
  id uuid PRIMARY KEY,
  chain_id text NOT NULL REFERENCES chains(id),
  scanner_shard text NOT NULL,
  capability text NOT NULL,
  from_height uint256 NOT NULL,
  to_height uint256 NOT NULL CHECK(to_height>=from_height),
  config_evidence jsonb NOT NULL CHECK(jsonb_typeof(config_evidence)='array' AND jsonb_array_length(config_evidence)>0 AND pg_column_size(config_evidence)<=131072),
  evidence_hash bytea NOT NULL CHECK(octet_length(evidence_hash)=32 AND evidence_hash=digest(config_evidence::text,'sha256')),
  committed_at timestamptz NOT NULL
);
CREATE INDEX scanner_runtime_evidence_range_idx ON scanner_runtime_config_evidence(chain_id,scanner_shard,to_height DESC);
CREATE TRIGGER scanner_runtime_evidence_immutable BEFORE UPDATE OR DELETE ON scanner_runtime_config_evidence FOR EACH ROW EXECUTE FUNCTION platform_immutable_row();
REVOKE UPDATE,DELETE,TRUNCATE ON scanner_runtime_config_evidence FROM PUBLIC;

CREATE FUNCTION platform_latest_pause(requested_tenant uuid, requested_kind platform_config_kind, requested_key text)
RETURNS boolean LANGUAGE sql STABLE SECURITY DEFINER
SET search_path=pg_catalog,public SET row_security=off AS $$
  SELECT COALESCE((SELECT action='pause' FROM public.platform_emergency_pause_events
    WHERE scope_id=public.platform_scope_uuid(requested_tenant) AND kind=requested_kind AND logical_key=requested_key
    ORDER BY occurred_at DESC,id DESC LIMIT 1),false)
$$;

CREATE FUNCTION platform_route_runtime_admission(requested_tenant uuid, requested_merchant uuid, requested_chain text, requested_asset text)
RETURNS TABLE(
  environment_snapshot_id uuid, environment_fence bigint,
  chain_snapshot_id uuid, chain_fence bigint,
  asset_snapshot_id uuid, asset_fence bigint,
  finality_snapshot_id uuid, finality_fence bigint,
  asset_decimals smallint, required_finality bigint
) LANGUAGE sql STABLE SECURITY DEFINER
SET search_path=pg_catalog,public SET row_security=off AS $$
  WITH
  env AS (
    SELECT h.snapshot_id,h.fence_token,s.payload FROM public.platform_config_heads h
    JOIN public.platform_config_snapshots s ON s.id=h.snapshot_id AND s.scope_id=h.scope_id
    WHERE h.scope_id=public.platform_scope_uuid(requested_tenant) AND h.kind='merchant_environment' AND h.logical_key=requested_merchant::text
      AND s.payload->>'status'='active'
      AND NOT public.platform_latest_pause(requested_tenant,'merchant_environment',requested_merchant::text)
  ), chain_cfg AS (
    SELECT h.snapshot_id,h.fence_token,s.payload FROM public.platform_config_heads h
    JOIN public.platform_config_snapshots s ON s.id=h.snapshot_id AND s.scope_id=h.scope_id
    WHERE h.scope_id=public.platform_scope_uuid(NULL) AND h.kind='chain' AND h.logical_key=requested_chain
      AND s.payload->>'status'='active'
      AND NOT public.platform_latest_pause(NULL,'chain',requested_chain)
  ), asset_cfg AS (
    SELECT h.snapshot_id,h.fence_token,s.payload FROM public.platform_config_heads h
    JOIN public.platform_config_snapshots s ON s.id=h.snapshot_id AND s.scope_id=h.scope_id
    WHERE h.scope_id=public.platform_scope_uuid(NULL) AND h.kind='asset_contract' AND h.logical_key=requested_asset
      AND s.payload->>'status'='active' AND s.payload->>'chain_ref'=requested_chain
      AND NOT public.platform_latest_pause(NULL,'asset_contract',requested_asset)
  ), finality AS (
    SELECT h.snapshot_id,h.fence_token,s.payload FROM public.platform_config_heads h
    JOIN public.platform_config_snapshots s ON s.id=h.snapshot_id AND s.scope_id=h.scope_id
    WHERE h.scope_id=public.platform_scope_uuid(NULL) AND h.kind='finality_policy' AND h.logical_key=requested_chain
      AND s.payload->>'chain_ref'=requested_chain
      AND NOT public.platform_latest_pause(NULL,'finality_policy',requested_chain)
  ), flags AS (
    SELECT (s.payload->>'enabled')::boolean enabled FROM public.platform_config_heads h
      JOIN public.platform_config_snapshots s ON s.id=h.snapshot_id AND s.scope_id=h.scope_id
      WHERE h.scope_id=public.platform_scope_uuid(requested_tenant) AND h.kind='feature_flag' AND h.logical_key='new_routes'
        AND s.payload->>'key'='new_routes' AND (s.payload->>'rollout_bps')::integer=10000
        AND NOT public.platform_latest_pause(requested_tenant,'feature_flag','new_routes')
  ), maintenance AS (
    SELECT NOT EXISTS (
      SELECT 1 FROM public.platform_config_heads h JOIN public.platform_config_snapshots s ON s.id=h.snapshot_id AND s.scope_id=h.scope_id
      WHERE h.kind='maintenance_window' AND h.logical_key IN (requested_chain,'new_routes')
        AND h.scope_id IN (public.platform_scope_uuid(NULL),public.platform_scope_uuid(requested_tenant))
        AND s.payload->>'effect' IN ('read_only','disable_new_routes')
        AND (s.payload->>'starts_at')::timestamptz<=statement_timestamp() AND (s.payload->>'ends_at')::timestamptz>statement_timestamp()
    ) admitted
  )
  SELECT env.snapshot_id,env.fence_token,chain_cfg.snapshot_id,chain_cfg.fence_token,
         asset_cfg.snapshot_id,asset_cfg.fence_token,finality.snapshot_id,finality.fence_token,
         (asset_cfg.payload->>'decimals')::smallint,(finality.payload->>'confirmations')::bigint
  FROM env,chain_cfg,asset_cfg,finality,flags,maintenance
  WHERE flags.enabled AND maintenance.admitted
    AND (asset_cfg.payload->>'decimals') ~ '^[0-9]+$' AND (asset_cfg.payload->>'decimals')::integer BETWEEN 0 AND 77
    AND (finality.payload->>'confirmations') ~ '^[0-9]+$' AND (finality.payload->>'confirmations')::numeric BETWEEN 0 AND 100000
$$;

CREATE FUNCTION platform_wallet_runtime_admission(requested_tenant uuid, requested_wallet uuid, requested_chain text)
RETURNS TABLE(wallet_pool_snapshot_id uuid,wallet_pool_fence bigint)
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path=pg_catalog,public SET row_security=off AS $$
  SELECT h.snapshot_id,h.fence_token FROM public.platform_config_heads h
  JOIN public.platform_config_snapshots s ON s.id=h.snapshot_id AND s.scope_id=h.scope_id
  WHERE h.scope_id=public.platform_scope_uuid(requested_tenant) AND h.kind='wallet_pool' AND h.logical_key=requested_wallet::text
    AND s.payload->>'status'='active' AND s.payload->>'chain_ref'=requested_chain
    AND NOT public.platform_latest_pause(requested_tenant,'wallet_pool',requested_wallet::text)
$$;

REVOKE ALL ON FUNCTION platform_latest_pause(uuid,platform_config_kind,text) FROM PUBLIC;
REVOKE ALL ON FUNCTION platform_route_runtime_admission(uuid,uuid,text,text) FROM PUBLIC;
REVOKE ALL ON FUNCTION platform_wallet_runtime_admission(uuid,uuid,text) FROM PUBLIC;

DO $$ BEGIN
  IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='merchant_api_runtime') THEN
    GRANT EXECUTE ON FUNCTION platform_route_runtime_admission(uuid,uuid,text,text),platform_wallet_runtime_admission(uuid,uuid,text) TO merchant_api_runtime;
  END IF;
  IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='merchant_management_runtime') THEN
    GRANT EXECUTE ON FUNCTION platform_route_runtime_admission(uuid,uuid,text,text),platform_wallet_runtime_admission(uuid,uuid,text) TO merchant_management_runtime;
  END IF;
  IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='merchant_scanner_worker') THEN
    GRANT SELECT ON platform_config_heads,platform_config_snapshots,platform_emergency_pause_events TO merchant_scanner_worker;
    GRANT SELECT,INSERT ON scanner_runtime_config_evidence TO merchant_scanner_worker;
  END IF;
END $$;

COMMIT;
