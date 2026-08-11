BEGIN;

DO $grants$ BEGIN
 IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='merchant_management_runtime') THEN
   REVOKE SELECT (id,tenant_id,merchant_id,payment_url_origins)
     ON hosted_provider_configs FROM merchant_management_runtime;
 END IF;
END $grants$;

COMMIT;
