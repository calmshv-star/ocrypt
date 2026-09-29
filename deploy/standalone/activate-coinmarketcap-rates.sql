\set ON_ERROR_STOP on

-- Run after the keyless CoinMarketCap gateway and 2-of-3 worker are deployed
-- and the public gateway admits /v1/public/rates/coinmarketcap/.
BEGIN;

CREATE TEMP TABLE cmc_rate_origin(origin text NOT NULL CHECK (
  origin ~ '^https://[A-Za-z0-9.-]+(:[0-9]{1,5})?$'
)) ON COMMIT DROP;
INSERT INTO cmc_rate_origin VALUES (:'rate_gateway_origin');

CREATE TEMP TABLE cmc_rate_pairs(asset_id text NOT NULL, currency text NOT NULL) ON COMMIT DROP;
INSERT INTO cmc_rate_pairs
SELECT asset_id,currency FROM
  (VALUES
    ('eth-ethereum'),('usdc-ethereum'),('usdt-ethereum'),
    ('sol-solana'),('usdc-solana'),('usdt-solana'),
    ('ton-ton'),('usdt-ton'),('trx-tron'),('usdt-tron'),
    ('eth-base'),('usdc-base'),('eth-arbitrum'),('usdc-arbitrum'),
    ('eth-optimism'),('usdc-optimism'),('avax-avalanche'),('usdc-avalanche'),
    ('pol-polygon'),('usdc-polygon'),('usdt-polygon'),('usdce-polygon'),
    ('bnb-bsc'),('usdt-bsc'),('usdc-bsc'),
    ('usdt-plasma'),('usdc-aptos'),('usdt-aptos')
  ) AS assets(asset_id)
  CROSS JOIN (VALUES('RUB'),('USD'),('EUR'),('KZT'),('INR'),('CNY')) AS currencies(currency);

DO $$
BEGIN
  IF (SELECT count(*) FROM cmc_rate_pairs)<>168 OR EXISTS (
    SELECT 1 FROM cmc_rate_pairs p
    LEFT JOIN platform_config_heads h ON h.scope_id=platform_scope_uuid(NULL)
      AND h.kind='rate_policy' AND h.logical_key='rate-'||p.asset_id||'-'||lower(p.currency)
    WHERE h.snapshot_id IS NULL
  ) THEN
    RAISE EXCEPTION 'CoinMarketCap activation requires the complete 28 x 6 rate catalog';
  END IF;
END $$;

CREATE OR REPLACE FUNCTION pg_temp.reconcile_cmc_rate_snapshot(
  requested_kind platform_config_kind, requested_key text, requested_payload jsonb
) RETURNS uuid LANGUAGE plpgsql AS $$
DECLARE
  requested_scope uuid := platform_scope_uuid(NULL);
  requester uuid := '0198a100-0000-7000-8000-000000000003';
  approver uuid := '0198a100-0000-7000-8000-000000000004';
  new_request_id uuid := uuidv7();
  new_snapshot_id uuid := uuidv7();
  new_activation_id uuid := uuidv7();
  old_snapshot platform_config_snapshots%ROWTYPE;
  old_fence bigint;
  new_version bigint := 1;
  now_at timestamptz := clock_timestamp();
BEGIN
  SELECT s.* INTO old_snapshot
  FROM platform_config_heads h
  JOIN platform_config_snapshots s ON s.id=h.snapshot_id
  WHERE h.scope_id=requested_scope AND h.kind=requested_kind AND h.logical_key=requested_key
  FOR UPDATE OF h;
  IF FOUND THEN
    SELECT h.fence_token INTO old_fence FROM platform_config_heads h
    WHERE h.scope_id=requested_scope AND h.kind=requested_kind AND h.logical_key=requested_key;
    IF old_snapshot.payload=requested_payload THEN
      RETURN old_snapshot.id;
    END IF;
    new_version := old_snapshot.version+1;
  END IF;

  INSERT INTO platform_config_change_requests(
    id,scope_id,kind,logical_key,version,based_on_version,payload,payload_hash,status,reason,
    requested_by,approved_by,scheduled_by,activated_by,requested_at,decided_at,scheduled_for,activated_at,
    created_at,updated_at,row_version)
  VALUES(new_request_id,requested_scope,requested_kind,requested_key,new_version,new_version-1,
    requested_payload,digest(requested_payload::text,'sha256'),'active',
    'Operator-approved independent CoinMarketCap source; retain 2-of-3 rate quorum',
    requester,approver,approver,approver,now_at,now_at,now_at,now_at,now_at,now_at,4);
  INSERT INTO platform_config_snapshots(
    id,scope_id,change_request_id,kind,logical_key,version,payload,payload_hash,activated_by,activated_at)
  VALUES(new_snapshot_id,requested_scope,new_request_id,requested_kind,requested_key,new_version,
    requested_payload,digest(requested_payload::text,'sha256'),approver,now_at);

  IF old_snapshot.id IS NULL THEN
    INSERT INTO platform_config_heads(scope_id,kind,logical_key,snapshot_id,fence_token,updated_at)
    VALUES(requested_scope,requested_kind,requested_key,new_snapshot_id,1,now_at);
    old_fence := 0;
  ELSE
    UPDATE platform_config_change_requests c SET status='superseded',updated_at=now_at,row_version=row_version+1
    WHERE c.id=old_snapshot.change_request_id AND c.status='active';
    UPDATE platform_config_heads SET snapshot_id=new_snapshot_id,fence_token=old_fence+1,updated_at=now_at
    WHERE scope_id=requested_scope AND kind=requested_kind AND logical_key=requested_key;
  END IF;
  INSERT INTO platform_config_activations(
    id,scope_id,kind,logical_key,snapshot_id,previous_snapshot_id,fence_token,
    activation_type,actor_id,occurred_at)
  VALUES(new_activation_id,requested_scope,requested_kind,requested_key,new_snapshot_id,
    old_snapshot.id,old_fence+1,'activate',approver,now_at);
  RETURN new_snapshot_id;
END $$;

SELECT pg_temp.reconcile_cmc_rate_snapshot(
  'rate_source',p.asset_id||'-'||lower(p.currency)||'-coinmarketcap',
  jsonb_build_object(
    'provider_ref','coinmarketcap-keyless-public',
    'endpoint',(SELECT origin FROM cmc_rate_origin)||'/v1/public/rates/coinmarketcap/'||p.asset_id||'/'||p.currency,
    'base_asset',p.asset_id,'quote_asset',p.currency,'max_age_seconds',2100,
    'timeout_ms',10000,'max_response_bytes',4096
  )
) FROM cmc_rate_pairs p;

DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM cmc_rate_pairs p
    JOIN platform_config_heads h ON h.scope_id=platform_scope_uuid(NULL)
      AND h.kind='rate_policy' AND h.logical_key='rate-'||p.asset_id||'-'||lower(p.currency)
    JOIN platform_config_snapshots s ON s.id=h.snapshot_id
    WHERE s.payload->'sources' NOT IN (
      jsonb_build_array(p.asset_id||'-'||lower(p.currency)||'-coingecko',
                        p.asset_id||'-'||lower(p.currency)||'-coinpaprika'),
      jsonb_build_array(p.asset_id||'-'||lower(p.currency)||'-coingecko',
                        p.asset_id||'-'||lower(p.currency)||'-coinpaprika',
                        p.asset_id||'-'||lower(p.currency)||'-coinmarketcap')
    ) OR (s.payload->>'quorum')::integer<>2
  ) THEN
    RAISE EXCEPTION 'rate policy source catalog drift: refusing CoinMarketCap activation';
  END IF;
END $$;

SELECT pg_temp.reconcile_cmc_rate_snapshot(
  'rate_policy','rate-'||p.asset_id||'-'||lower(p.currency),
  jsonb_set(jsonb_set(s.payload,'{sources}',
    jsonb_build_array(p.asset_id||'-'||lower(p.currency)||'-coingecko',
                      p.asset_id||'-'||lower(p.currency)||'-coinpaprika',
                      p.asset_id||'-'||lower(p.currency)||'-coinmarketcap'),false),
    '{poll_interval_seconds}','300'::jsonb,false)
) FROM cmc_rate_pairs p
JOIN platform_config_heads h ON h.scope_id=platform_scope_uuid(NULL)
  AND h.kind='rate_policy' AND h.logical_key='rate-'||p.asset_id||'-'||lower(p.currency)
JOIN platform_config_snapshots s ON s.id=h.snapshot_id;

UPDATE rate_runtime_jobs
SET next_attempt_at=clock_timestamp(),lease_owner=NULL,lease_until=NULL,updated_at=clock_timestamp()
WHERE policy_key LIKE 'rate-%-rub' AND status='active';

COMMIT;
