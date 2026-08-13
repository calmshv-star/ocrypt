BEGIN;

-- Historical ignored dust remains an immutable operational decision. Rolling
-- back only disables automatic classification for future TON transfers.
UPDATE assets
SET dust_threshold=0,
    updated_at=clock_timestamp(),
    version=version+1
WHERE id='ton-ton'
  AND chain_id='ton:mainnet'
  AND dust_threshold=1000000;

DO $$ BEGIN
  IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='merchant_settlement_worker') THEN
    REVOKE SELECT ON assets FROM merchant_settlement_worker;
  END IF;
END $$;

COMMIT;
