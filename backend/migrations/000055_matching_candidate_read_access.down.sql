BEGIN;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='merchant_matching_worker') THEN
    REVOKE SELECT ON match_candidates FROM merchant_matching_worker;
  END IF;
END $$;

COMMIT;
