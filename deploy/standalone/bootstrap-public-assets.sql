\set ON_ERROR_STOP on

-- Conservative keyless catalog: established public chains plus native assets
-- and issuer-native USDC/USDT contracts only. Bridged/pegged lookalikes are
-- deliberately excluded. Every EVM chain reuses the operator-owned EVM deposit
-- address; the scanner is watch-only and never receives a private key.

CREATE TEMP TABLE public_evm_chain_catalog(
  chain_id text PRIMARY KEY,
  network_key text NOT NULL,
  network_name text NOT NULL,
  genesis_hash text NOT NULL,
  native_asset_id text NOT NULL UNIQUE,
  native_symbol text NOT NULL,
  native_name text NOT NULL,
  confirmations smallint NOT NULL,
  explorer_template text NOT NULL,
  wallet_id uuid NOT NULL UNIQUE,
  address_id uuid NOT NULL UNIQUE
) ON COMMIT DROP;

INSERT INTO public_evm_chain_catalog VALUES
('eip155:8453','base-mainnet','Base Mainnet','0xf712aa9241cc24369b143cf6dce85f0902a9731e70d66818a3a5845b296c73dd','eth-base','ETH','Ether',1,'https://basescan.org/tx/{tx}','0198a100-0000-7000-8000-000000000060','0198a100-0000-7000-8000-000000000070'),
('eip155:42161','arbitrum-one','Arbitrum One','0x7ee576b35482195fc49205cec9af72ce14f003b9ae69f6ba0faef4514be8b442','eth-arbitrum','ETH','Ether',1,'https://arbiscan.io/tx/{tx}','0198a100-0000-7000-8000-000000000061','0198a100-0000-7000-8000-000000000071'),
('eip155:10','optimism-mainnet','OP Mainnet','0x7ca38a1916c42007829c55e69d3e9a73265554b586a499015373241b8a3fa48b','eth-optimism','ETH','Ether',1,'https://optimistic.etherscan.io/tx/{tx}','0198a100-0000-7000-8000-000000000062','0198a100-0000-7000-8000-000000000072'),
('eip155:43114','avalanche-c-chain','Avalanche C-Chain','0x31ced5b9beb7f8782b014660da0cb18cc409f121f408186886e1ca3e8eeca96b','avax-avalanche','AVAX','Avalanche',1,'https://snowtrace.io/tx/{tx}','0198a100-0000-7000-8000-000000000063','0198a100-0000-7000-8000-000000000073'),
('eip155:137','polygon-pos','Polygon PoS','0xa9c28ce2141b56c474f1dc504bee9b01eb1bd7d1a507580d5519d4437a97de1b','pol-polygon','POL','Polygon Ecosystem Token',3,'https://polygonscan.com/tx/{tx}','0198a100-0000-7000-8000-000000000064','0198a100-0000-7000-8000-000000000074'),
('eip155:56','bsc-mainnet','BNB Smart Chain','0x0d21840abff46b96c84b2ac9e10e4f5cdaeb5693cb665db62a2f3b02d2d57b5b','bnb-bsc','BNB','BNB',3,'https://bscscan.com/tx/{tx}','0198a100-0000-7000-8000-000000000065','0198a100-0000-7000-8000-000000000075'),
('eip155:9745','plasma-mainnet','Plasma Mainnet','0x0d0ccca452bdb244100115e37de64ca640a255585d5a94df5610052a6dada558','xpl-plasma','XPL','Plasma',1,'https://plasmascan.to/tx/{tx}','0198a100-0000-7000-8000-000000000066','0198a100-0000-7000-8000-000000000076');

CREATE TEMP TABLE public_token_catalog(
  asset_id text PRIMARY KEY,
  chain_id text NOT NULL,
  symbol text NOT NULL,
  asset_name text NOT NULL,
  contract text NOT NULL,
  decimals smallint NOT NULL
) ON COMMIT DROP;

INSERT INTO public_token_catalog VALUES
('usdc-ethereum','eip155:1','USDC','USD Coin','0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48',6),
('usdt-ethereum','eip155:1','USDT','Tether USD','0xdAC17F958D2ee523a2206206994597C13D831ec7',6),
('usdc-solana','solana:mainnet','USDC','USD Coin','EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v',6),
('usdt-solana','solana:mainnet','USDT','Tether USD','Es9vMFrzaCERmJfrF4H2FYD4KCoNkY11McCe8BenwNYB',6),
('usdt-ton','ton:mainnet','USDT','Tether USD','EQCxE6mUtQJKFnGfaROTKOt1lZbDiiX1kCixRv7Nw2Id_sDs',6),
('usdc-base','eip155:8453','USDC','USD Coin','0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913',6),
('usdc-arbitrum','eip155:42161','USDC','USD Coin','0xaf88d065e77c8cC2239327C5EDb3A432268e5831',6),
('usdc-optimism','eip155:10','USDC','USD Coin','0x0b2C639c533813f4Aa9D7837CAf62653d097Ff85',6),
('usdc-avalanche','eip155:43114','USDC','USD Coin','0xB97EF9Ef8734C71904D8002F8b6Bc66Dd9c48a6E',6),
('usdc-polygon','eip155:137','USDC','USD Coin','0x3c499c542cEF5E3811e1192ce70d8cC03d5c3359',6),
('usdt-polygon','eip155:137','USDT','Tether USD','0xc2132D05D31c914a87C6611C10748AEb04B58e8F',6),
('usdce-polygon','eip155:137','USDC.e','Bridged USD Coin','0x2791Bca1f2de4661ED88A30C99A7a9449Aa84174',6),
('usdt-bsc','eip155:56','USDT','Binance-Peg Tether USD','0x55d398326f99059fF775485246999027B3197955',18),
('usdc-bsc','eip155:56','USDC','Binance-Peg USD Coin','0x8AC76a51cc950d9822D68b83fE1Ad97B32Cd580d',18),
('usdt-plasma','eip155:9745','USDT','Tether USD','0xB8CE59FC3717ada4C02eaDF9682A9e934F625ebb',6);

INSERT INTO chains(id,family,network_name,status,required_confirmations,maximum_reorg_depth,transaction_url_template,created_at,updated_at)
SELECT chain_id,'evm',network_name,'active',confirmations,64,explorer_template,clock_timestamp(),clock_timestamp()
FROM public_evm_chain_catalog
ON CONFLICT(id) DO UPDATE SET
  family=EXCLUDED.family,network_name=EXCLUDED.network_name,status='active',
  required_confirmations=EXCLUDED.required_confirmations,
  maximum_reorg_depth=EXCLUDED.maximum_reorg_depth,
  transaction_url_template=EXCLUDED.transaction_url_template,updated_at=clock_timestamp();

INSERT INTO assets(id,chain_id,symbol,name,kind,canonical_contract,decimals,status,created_at,updated_at)
SELECT native_asset_id,chain_id,native_symbol,native_name,'native','native',18,
       CASE WHEN chain_id='eip155:9745' THEN 'deposit_disabled' ELSE 'active' END,
       clock_timestamp(),clock_timestamp()
FROM public_evm_chain_catalog
UNION ALL
SELECT asset_id,chain_id,symbol,asset_name,'fungible_token',contract,decimals,'active',clock_timestamp(),clock_timestamp()
FROM public_token_catalog
ON CONFLICT(id) DO UPDATE SET
  chain_id=EXCLUDED.chain_id,symbol=EXCLUDED.symbol,name=EXCLUDED.name,kind=EXCLUDED.kind,
  canonical_contract=EXCLUDED.canonical_contract,decimals=EXCLUDED.decimals,
  status=EXCLUDED.status,updated_at=clock_timestamp();

INSERT INTO chains(id,family,network_name,status,required_confirmations,maximum_reorg_depth,transaction_url_template,created_at,updated_at)
VALUES('aptos:1','aptos','Aptos Mainnet','disabled',1,64,'https://explorer.aptoslabs.com/txn/{tx}?network=mainnet',clock_timestamp(),clock_timestamp())
ON CONFLICT(id) DO UPDATE SET family=EXCLUDED.family,network_name=EXCLUDED.network_name,status='disabled',required_confirmations=EXCLUDED.required_confirmations,maximum_reorg_depth=EXCLUDED.maximum_reorg_depth,transaction_url_template=EXCLUDED.transaction_url_template,updated_at=clock_timestamp();

INSERT INTO assets(id,chain_id,symbol,name,kind,canonical_contract,decimals,status,created_at,updated_at)
VALUES
  ('usdc-aptos','aptos:1','USDC','USD Coin','fungible_token','0xbae207659db88bea0cbead6da0ed00aac12edcdda169e591cd41c94180b46f3b',6,'deposit_disabled',clock_timestamp(),clock_timestamp()),
  ('usdt-aptos','aptos:1','USDT','Tether USD','fungible_token','0x357b0b74bc833e95a115ad22604854d6b0fca151cecd94111770e5d6ffc9dc2b',6,'deposit_disabled',clock_timestamp(),clock_timestamp())
ON CONFLICT(id) DO UPDATE SET chain_id=EXCLUDED.chain_id,symbol=EXCLUDED.symbol,name=EXCLUDED.name,kind=EXCLUDED.kind,canonical_contract=EXCLUDED.canonical_contract,decimals=EXCLUDED.decimals,status='deposit_disabled',updated_at=clock_timestamp();

WITH desired(asset_id,chain_id,dust_threshold) AS (
  VALUES
    ('usdc-ethereum','eip155:1',100000::numeric),
    ('usdt-ethereum','eip155:1',100000::numeric),
    ('usdc-solana','solana:mainnet',100000::numeric),
    ('usdt-solana','solana:mainnet',100000::numeric),
    ('usdt-ton','ton:mainnet',100000::numeric),
    ('eth-base','eip155:8453',1000000000::numeric),
    ('usdc-base','eip155:8453',100000::numeric),
    ('eth-arbitrum','eip155:42161',1000000000::numeric),
    ('usdc-arbitrum','eip155:42161',100000::numeric),
    ('eth-optimism','eip155:10',1000000000::numeric),
    ('usdc-optimism','eip155:10',100000::numeric),
    ('avax-avalanche','eip155:43114',1000000000::numeric),
    ('usdc-avalanche','eip155:43114',100000::numeric),
    ('pol-polygon','eip155:137',1000000000::numeric),
    ('usdc-polygon','eip155:137',100000::numeric),
    ('usdt-polygon','eip155:137',100000::numeric),
    ('usdce-polygon','eip155:137',100000::numeric),
    ('bnb-bsc','eip155:56',1000000000::numeric),
    ('usdt-bsc','eip155:56',100000000000000000::numeric),
    ('usdc-bsc','eip155:56',100000000000000000::numeric),
    ('usdt-plasma','eip155:9745',100000::numeric),
    ('usdc-aptos','aptos:1',100000::numeric),
    ('usdt-aptos','aptos:1',100000::numeric)
)
UPDATE assets a SET dust_threshold=GREATEST(a.dust_threshold,d.dust_threshold),updated_at=clock_timestamp(),version=version+1
FROM desired d
WHERE a.id=d.asset_id AND a.chain_id=d.chain_id AND a.dust_threshold<d.dust_threshold;

INSERT INTO wallets(id,tenant_id,merchant_id,chain_id,custody_mode,status,created_at,updated_at)
SELECT wallet_id,'0198a100-0000-7000-8000-000000000001','0198a100-0000-7000-8000-000000000002',chain_id,
       'watch_only','active',clock_timestamp(),clock_timestamp()
FROM public_evm_chain_catalog
ON CONFLICT(id) DO NOTHING;

INSERT INTO wallets(id,tenant_id,merchant_id,chain_id,custody_mode,status,created_at,updated_at)
VALUES('0198a100-0000-7000-8000-000000000067','0198a100-0000-7000-8000-000000000001','0198a100-0000-7000-8000-000000000002','aptos:1','watch_only','disabled',clock_timestamp(),clock_timestamp())
ON CONFLICT(id) DO UPDATE SET status='disabled',updated_at=clock_timestamp();

INSERT INTO addresses(id,tenant_id,wallet_id,chain_id,canonical_address,display_address,purpose,status,created_at,updated_at)
SELECT address_id,'0198a100-0000-7000-8000-000000000001',wallet_id,chain_id,
       lower(:'evm_deposit_address'),:'evm_deposit_address','deposit','available',clock_timestamp(),clock_timestamp()
FROM public_evm_chain_catalog
ON CONFLICT(id) DO NOTHING;

SELECT pg_temp.reconcile_platform_snapshot(
  NULL,'chain',chain_id,
  jsonb_build_object(
    'family','evm','network',network_key,'status','active','genesis_hash',genesis_hash,
    'quorum',2,'overlap',64,'range_size',128,'max_head_age_seconds',60
  ),
  '0198a100-0000-7000-8000-000000000003','0198a100-0000-7000-8000-000000000004'
)
FROM public_evm_chain_catalog c
WHERE c.chain_id IN ('eip155:137','eip155:56')
   OR NOT EXISTS(
      SELECT 1 FROM platform_config_heads
      WHERE scope_id=platform_scope_uuid(NULL) AND kind='chain' AND logical_key=c.chain_id
   );

SELECT pg_temp.reconcile_platform_snapshot(
  NULL,'finality_policy',chain_id,
  jsonb_build_object('chain_ref',chain_id,'confirmations',confirmations,'reorg_depth',64),
  '0198a100-0000-7000-8000-000000000003','0198a100-0000-7000-8000-000000000004'
)
FROM public_evm_chain_catalog c
WHERE c.chain_id IN ('eip155:137','eip155:56')
   OR NOT EXISTS(
      SELECT 1 FROM platform_config_heads
      WHERE scope_id=platform_scope_uuid(NULL) AND kind='finality_policy' AND logical_key=c.chain_id
   );

SELECT pg_temp.reconcile_platform_snapshot(
  NULL,'chain','aptos:1',
  jsonb_build_object(
    'family','aptos','network','mainnet','status','disabled',
    'genesis_hash','0x3510d3d8a067545615637893121679e7961c9e515c3f2ce60d62e17f6073122f',
    'quorum',2,'overlap',64,'range_size',512,'max_head_age_seconds',120
  ),
  '0198a100-0000-7000-8000-000000000003','0198a100-0000-7000-8000-000000000004'
);

SELECT pg_temp.reconcile_platform_snapshot(
  NULL,'finality_policy','aptos:1',
  jsonb_build_object('chain_ref','aptos:1','confirmations',1,'reorg_depth',64),
  '0198a100-0000-7000-8000-000000000003','0198a100-0000-7000-8000-000000000004'
)
WHERE NOT EXISTS(
  SELECT 1 FROM platform_config_heads
  WHERE scope_id=platform_scope_uuid(NULL) AND kind='finality_policy' AND logical_key='aptos:1'
);

SELECT pg_temp.activate_platform_snapshot(
  NULL,'asset_contract',asset_id,
  jsonb_build_object(
    'chain_ref',chain_id,'asset_code',asset_id,'family','evm',
    'contract','native','decimals',18,'status','active'
  ),
  '0198a100-0000-7000-8000-000000000003','0198a100-0000-7000-8000-000000000004'
)
FROM (
  SELECT chain_id,native_asset_id AS asset_id FROM public_evm_chain_catalog
) a
WHERE a.chain_id<>'eip155:9745'
  AND NOT EXISTS(
  SELECT 1 FROM platform_config_heads
  WHERE scope_id=platform_scope_uuid(NULL) AND kind='asset_contract' AND logical_key=a.asset_id
);

SELECT pg_temp.activate_platform_snapshot(
  NULL,'asset_contract',asset_id,
  jsonb_build_object(
    'chain_ref',chain_id,'asset_code',asset_id,'family',
    CASE
      WHEN chain_id='solana:mainnet' THEN 'solana'
      WHEN chain_id='ton:mainnet' THEN 'ton'
      ELSE 'evm'
    END,
    'contract',contract,'decimals',decimals,'status','active'
  ),
  '0198a100-0000-7000-8000-000000000003','0198a100-0000-7000-8000-000000000004'
)
FROM public_token_catalog a
WHERE NOT EXISTS(
  SELECT 1 FROM platform_config_heads
  WHERE scope_id=platform_scope_uuid(NULL) AND kind='asset_contract' AND logical_key=a.asset_id
);

SELECT pg_temp.reconcile_platform_snapshot(
  NULL,'asset_contract',asset_id,
  jsonb_build_object(
    'chain_ref','aptos:1','asset_code',asset_id,'family','aptos',
    'contract',contract,'decimals',6,'fungible_asset',true,'status','deposit_disabled'
  ),
  '0198a100-0000-7000-8000-000000000003','0198a100-0000-7000-8000-000000000004'
)
FROM (VALUES
  ('usdc-aptos','0xbae207659db88bea0cbead6da0ed00aac12edcdda169e591cd41c94180b46f3b'),
  ('usdt-aptos','0x357b0b74bc833e95a115ad22604854d6b0fca151cecd94111770e5d6ffc9dc2b')
) AS aptos_assets(asset_id,contract);

WITH rpc(logical_key,chain_id,endpoint,indexer_endpoint,indexer_endpoint_ref,head_tag,provider_kind,provider_id,priority,failure_domain) AS (
  VALUES
    ('rpc/polygon-publicnode','eip155:137','https://polygon-bor-rpc.publicnode.com',NULL,NULL,'finalized','evm-jsonrpc','polygon-publicnode',10,'polygon-publicnode'),
    ('rpc/polygon-tenderly','eip155:137','https://tenderly.rpc.polygon.community',NULL,NULL,'finalized','evm-jsonrpc','polygon-tenderly',20,'polygon-tenderly'),
    ('rpc/polygon-drpc','eip155:137','https://polygon.drpc.org',NULL,NULL,'finalized','evm-jsonrpc','polygon-drpc',30,'polygon-drpc'),
    ('rpc/bsc-publicnode','eip155:56','https://bsc-rpc.publicnode.com',NULL,NULL,'finalized','evm-jsonrpc','bsc-publicnode',10,'bsc-publicnode'),
    ('rpc/bsc-blxr','eip155:56','https://bsc.rpc.blxrbdn.com',NULL,NULL,'finalized','evm-jsonrpc','bsc-blxr',20,'blxr-bdn'),
    ('rpc/plasma-public','eip155:9745','https://rpc.plasma.to',NULL,NULL,'finalized','evm-jsonrpc','plasma-public',10,'plasma-public'),
    ('rpc/plasma-thirdweb','eip155:9745','https://9745.rpc.thirdweb.com',NULL,NULL,'finalized','evm-jsonrpc','plasma-thirdweb',20,'plasma-thirdweb'),
    ('rpc/aptos-publicnode','aptos:1','https://aptos-rest.publicnode.com','https://api.mainnet.aptoslabs.com/v1/graphql',NULL,NULL,'aptos-fullnode','aptos-publicnode',10,'aptos-publicnode'),
    ('rpc/aptos-nodit','aptos:1','https://fullnode.mainnet.aptoslabs.com',NULL,'aptos/nodit-indexer',NULL,'aptos-fullnode','aptos-nodit',20,'nodit')
), payloads AS (
  SELECT logical_key,jsonb_strip_nulls(jsonb_build_object(
    'chain_ref',chain_id,'endpoint',endpoint,
    'indexer_endpoint',indexer_endpoint,
    'indexer_endpoint_ref',indexer_endpoint_ref,
    'head_tag',head_tag,
    'capabilities',CASE WHEN provider_kind='evm-jsonrpc'
      THEN jsonb_build_array('blocks','transactions','logs','receipts')
      ELSE jsonb_build_array('blocks','transactions') END,
    'provider_kind',provider_kind,'provider_id',provider_id,'timeout_ms',10000,
    'provider_operations',(
      SELECT jsonb_object_agg(operation,jsonb_build_object(
        'timeout_ms',10000,'max_attempts',2,'backoff_ms',250,
        'rate_limit',60,'rate_window_seconds',60,'max_health_age_seconds',120,
        'failure_threshold',3,'open_seconds',30,'half_open_successes',2,
        'priority',priority,'failure_domain',failure_domain,'max_lag_blocks',128
      ))
      FROM unnest(ARRAY['health','head','range','transaction_lookup','transfer_verify']) operation
    )
  )) payload
  FROM rpc
)
SELECT pg_temp.reconcile_platform_snapshot(
  NULL,'rpc_provider',logical_key,payload,
  '0198a100-0000-7000-8000-000000000003','0198a100-0000-7000-8000-000000000004'
)
FROM payloads p;

-- Retain the old snapshot for audit, but never let the stale public 1RPC
-- endpoint participate in admission after the replacement is installed.
UPDATE provider_operation_bindings
SET status='paused',version=version+1,updated_at=clock_timestamp()
WHERE provider_kind='on_chain' AND provider_id='bsc-1rpc' AND status='active';

-- Retain the earlier generic secondary-indexer snapshot for audit, but do not
-- let it participate after the independent Nodit identity is installed.
UPDATE provider_operation_bindings
SET status='paused',version=version+1,updated_at=clock_timestamp()
WHERE provider_kind='on_chain' AND provider_id='aptos-labs' AND status='active';

SELECT pg_temp.reconcile_platform_snapshot(
  '0198a100-0000-7000-8000-000000000001','wallet_pool',wallet_id::text,
  jsonb_build_object('status','active','chain_ref',chain_id),
  '0198a100-0000-7000-8000-000000000003','0198a100-0000-7000-8000-000000000004'
)
FROM public_evm_chain_catalog w
WHERE NOT EXISTS(
  SELECT 1 FROM platform_config_heads
  WHERE scope_id=platform_scope_uuid('0198a100-0000-7000-8000-000000000001')
    AND kind='wallet_pool' AND logical_key=w.wallet_id::text
);

SELECT pg_temp.reconcile_platform_snapshot(
  '0198a100-0000-7000-8000-000000000001','wallet_pool','0198a100-0000-7000-8000-000000000067',
  jsonb_build_object('status','disabled','chain_ref','aptos:1'),
  '0198a100-0000-7000-8000-000000000003','0198a100-0000-7000-8000-000000000004'
);
