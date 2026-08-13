DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='merchant_api_runtime') THEN
    REVOKE SELECT ON management_webhook_verifications FROM merchant_api_runtime;
  END IF;
END $$;
