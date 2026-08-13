-- An endpoint status is activated only after a successful challenge. Intent
-- admission still reads the immutable verification evidence directly so a
-- repaired or imported status flag cannot bypass endpoint ownership proof.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='merchant_api_runtime') THEN
    GRANT SELECT ON management_webhook_verifications TO merchant_api_runtime;
  END IF;
END $$;
