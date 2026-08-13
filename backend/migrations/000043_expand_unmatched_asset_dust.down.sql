BEGIN;

-- Do not reopen historical ignored dust: it remains immutable audit evidence.
-- Restore only thresholds that still equal this release's defaults so an
-- operator change made after the migration is never overwritten.
WITH previous(asset_id,chain_id,applied_threshold,previous_threshold) AS (
  VALUES
    ('eth-ethereum','eip155:1',1000000000::numeric,0::numeric),
    ('usdc-ethereum','eip155:1',100::numeric,0::numeric),
    ('usdt-ethereum','eip155:1',100::numeric,0::numeric),
    ('sol-solana','solana:mainnet',100::numeric,0::numeric),
    ('usdc-solana','solana:mainnet',100::numeric,0::numeric),
    ('usdt-solana','solana:mainnet',100::numeric,0::numeric),
    ('ton-ton','ton:mainnet',1000000::numeric,1000000::numeric),
    ('usdt-ton','ton:mainnet',100::numeric,0::numeric),
    ('trx-tron','tron:mainnet',100::numeric,0::numeric),
    ('usdt-tron','tron:mainnet',100::numeric,0::numeric),
    ('eth-base','eip155:8453',1000000000::numeric,0::numeric),
    ('usdc-base','eip155:8453',100::numeric,0::numeric),
    ('eth-arbitrum','eip155:42161',1000000000::numeric,0::numeric),
    ('usdc-arbitrum','eip155:42161',100::numeric,0::numeric),
    ('eth-optimism','eip155:10',1000000000::numeric,0::numeric),
    ('usdc-optimism','eip155:10',100::numeric,0::numeric),
    ('avax-avalanche','eip155:43114',1000000000::numeric,0::numeric),
    ('usdc-avalanche','eip155:43114',100::numeric,0::numeric),
    ('pol-polygon','eip155:137',1000000000::numeric,0::numeric),
    ('usdc-polygon','eip155:137',100::numeric,0::numeric),
    ('bnb-bsc','eip155:56',1000000000::numeric,0::numeric)
)
UPDATE assets a
SET dust_threshold=p.previous_threshold,
    updated_at=clock_timestamp(),
    version=a.version+1
FROM previous p
WHERE a.id=p.asset_id
  AND a.chain_id=p.chain_id
  AND a.dust_threshold=p.applied_threshold
  AND a.dust_threshold<>p.previous_threshold;

COMMIT;
