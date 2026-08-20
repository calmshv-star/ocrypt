BEGIN;

-- Stablecoin transfers below 0.1 units are operational dust for the manual
-- matching queue. Exact and policy-tolerated invoice payments remain visible:
-- the settlement path checks exact routes first and this backfill repeats the
-- same plausible-payment guard before ignoring an existing unmatched record.
WITH desired(asset_id,chain_id,dust_threshold) AS (
  VALUES
    ('usdc-ethereum','eip155:1',100000::numeric),
    ('usdt-ethereum','eip155:1',100000::numeric),
    ('usdc-solana','solana:mainnet',100000::numeric),
    ('usdt-solana','solana:mainnet',100000::numeric),
    ('usdt-ton','ton:mainnet',100000::numeric),
    ('usdt-tron','tron:mainnet',100000::numeric),
    ('usdc-base','eip155:8453',100000::numeric),
    ('usdc-arbitrum','eip155:42161',100000::numeric),
    ('usdc-optimism','eip155:10',100000::numeric),
    ('usdc-avalanche','eip155:43114',100000::numeric),
    ('usdc-polygon','eip155:137',100000::numeric),
    ('usdt-polygon','eip155:137',100000::numeric),
    ('usdce-polygon','eip155:137',100000::numeric),
    ('usdt-bsc','eip155:56',100000000000000000::numeric),
    ('usdc-bsc','eip155:56',100000000000000000::numeric),
    ('usdt-plasma','eip155:9745',100000::numeric),
    ('usdc-aptos','aptos:1',100000::numeric),
    ('usdt-aptos','aptos:1',100000::numeric)
)
UPDATE assets a
SET dust_threshold=GREATEST(a.dust_threshold,d.dust_threshold),
    updated_at=clock_timestamp(),
    version=a.version+1
FROM desired d
WHERE a.id=d.asset_id
  AND a.chain_id=d.chain_id
  AND a.dust_threshold<d.dust_threshold;

UPDATE unmatched_payments u
SET classification='dust_below_asset_threshold',
    status='ignored',
    severity='low',
    assigned_operator_id=NULL,
    updated_at=clock_timestamp(),
    version=u.version+1
FROM transfer_events e
JOIN assets a ON a.id=e.asset_id AND a.chain_id=e.chain_id
WHERE u.event_id=e.id
  AND u.status NOT IN ('resolved','ignored','invalid','reorged')
  AND a.dust_threshold>0
  AND e.amount_atomic<=a.dust_threshold
  AND NOT EXISTS (
    SELECT 1 FROM payment_matches pm
    WHERE pm.event_id=e.id AND pm.state<>'reversed'
  )
  AND NOT EXISTS (
    SELECT 1
    FROM payment_routes r
    JOIN payment_intents i ON i.id=r.intent_id AND i.tenant_id=r.tenant_id
    JOIN payment_route_policy_bindings b ON b.route_id=r.id AND b.tenant_id=r.tenant_id
    WHERE r.provider='on_chain'
      AND r.chain_id=e.chain_id
      AND r.asset_id=e.asset_id
      AND r.receiving_address=e.to_address
      AND r.status IN ('active','expired')
      AND i.status IN ('pending','observed','partially_paid','confirmed','expired','needs_review','reorg_review')
      AND (
        e.on_chain_time BETWEEN r.starts_at AND r.expires_at
        OR (
          COALESCE((b.policy_snapshot->>'accept_late_within_grace')::boolean,false)
          AND e.on_chain_time>r.expires_at
          AND e.on_chain_time<=r.grace_ends_at
        )
      )
      AND e.amount_atomic*10000 >= r.expected_amount_atomic*(
        10000-LEAST(10000,GREATEST(0,COALESCE((b.policy_snapshot->>'underpayment_tolerance_bps')::numeric,0)))
      )
  );

COMMIT;
