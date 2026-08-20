BEGIN;

-- Restore only values written by the up migration. Ignored evidence remains
-- auditable and is not revived into the operator queue during rollback.
WITH previous(asset_id,chain_id,applied_threshold,previous_threshold) AS (
  VALUES
    ('usdc-ethereum','eip155:1',100000::numeric,100::numeric),
    ('usdt-ethereum','eip155:1',100000::numeric,100::numeric),
    ('usdc-solana','solana:mainnet',100000::numeric,100::numeric),
    ('usdt-solana','solana:mainnet',100000::numeric,100::numeric),
    ('usdt-ton','ton:mainnet',100000::numeric,100::numeric),
    ('usdt-tron','tron:mainnet',100000::numeric,100::numeric),
    ('usdc-base','eip155:8453',100000::numeric,100::numeric),
    ('usdc-arbitrum','eip155:42161',100000::numeric,100::numeric),
    ('usdc-optimism','eip155:10',100000::numeric,100::numeric),
    ('usdc-avalanche','eip155:43114',100000::numeric,100::numeric),
    ('usdc-polygon','eip155:137',100000::numeric,100::numeric),
    ('usdt-polygon','eip155:137',100000::numeric,100::numeric),
    ('usdce-polygon','eip155:137',100000::numeric,100::numeric),
    ('usdt-bsc','eip155:56',100000000000000000::numeric,100000000000000::numeric),
    ('usdc-bsc','eip155:56',100000000000000000::numeric,100000000000000::numeric),
    ('usdt-plasma','eip155:9745',100000::numeric,100::numeric),
    ('usdc-aptos','aptos:1',100000::numeric,100::numeric),
    ('usdt-aptos','aptos:1',100000::numeric,100::numeric)
)
UPDATE assets a
SET dust_threshold=p.previous_threshold,
    updated_at=clock_timestamp(),
    version=a.version+1
FROM previous p
WHERE a.id=p.asset_id
  AND a.chain_id=p.chain_id
  AND a.dust_threshold=p.applied_threshold;

COMMIT;
