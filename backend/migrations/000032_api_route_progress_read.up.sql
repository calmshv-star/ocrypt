BEGIN;

DO $$ BEGIN
  IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='merchant_api_runtime') THEN
    GRANT SELECT ON payment_match_aggregates TO merchant_api_runtime;
  END IF;
END $$;

COMMIT;
