BEGIN;

UPDATE assets
SET symbol='TON', name='Toncoin', updated_at=clock_timestamp()
WHERE id='ton-ton' AND chain_id='ton:mainnet'
  AND symbol='GRAM' AND name='Gram';

COMMIT;
