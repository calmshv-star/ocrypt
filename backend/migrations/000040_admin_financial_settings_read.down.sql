BEGIN;

REVOKE EXECUTE ON FUNCTION admin_financial_settings_inventory(uuid,uuid) FROM merchant_admin_runtime;
DROP FUNCTION IF EXISTS admin_financial_settings_inventory(uuid,uuid);

COMMIT;
