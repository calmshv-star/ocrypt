BEGIN;
DROP FUNCTION IF EXISTS admin_replace_watch_wallet_address(uuid,uuid,uuid,uuid,uuid,text,text,text,bigint,text);
DROP FUNCTION IF EXISTS admin_watch_wallet_inventory(uuid,uuid);
DROP INDEX IF EXISTS wallet_current_deposit_address_idx;
DROP INDEX IF EXISTS merchant_watch_wallet_chain_idx;
COMMIT;
