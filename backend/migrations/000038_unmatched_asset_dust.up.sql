BEGIN;

UPDATE assets
SET dust_threshold=1000000,
    updated_at=clock_timestamp(),
    version=version+1
WHERE id='ton-ton'
  AND chain_id='ton:mainnet'
  AND dust_threshold=0;

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
    WHERE r.provider='on_chain'
      AND r.chain_id=e.chain_id
      AND r.asset_id=e.asset_id
      AND r.receiving_address=e.to_address
      AND r.expected_amount_atomic=e.amount_atomic
      AND r.status IN ('active','expired')
      AND i.status IN ('pending','observed','partially_paid','confirmed','expired','needs_review','reorg_review')
      AND e.on_chain_time BETWEEN r.starts_at AND r.expires_at
  );

DO $$ BEGIN
  IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='merchant_settlement_worker') THEN
    GRANT SELECT ON assets TO merchant_settlement_worker;
  END IF;
END $$;

COMMIT;
