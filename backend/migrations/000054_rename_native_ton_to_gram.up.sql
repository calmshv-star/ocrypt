BEGIN;

-- Only the native currency was rebranded. The TON blockchain, stablecoin
-- routes and immutable asset ID keep their existing identities.
UPDATE assets
SET symbol='GRAM', name='Gram', updated_at=clock_timestamp()
WHERE id='ton-ton' AND chain_id='ton:mainnet'
  AND (symbol IS DISTINCT FROM 'GRAM' OR name IS DISTINCT FROM 'Gram');

COMMIT;
