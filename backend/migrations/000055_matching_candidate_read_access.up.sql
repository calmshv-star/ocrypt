BEGIN;

-- Matching reads the scanner's ranked candidate evidence before attributing a
-- transfer to a shared-address invoice. It never mutates candidate records.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='merchant_matching_worker') THEN
    GRANT SELECT ON match_candidates TO merchant_matching_worker;
  END IF;
END $$;

COMMIT;
