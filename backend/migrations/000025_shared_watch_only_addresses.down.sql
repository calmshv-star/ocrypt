BEGIN;

DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM address_assignments
    WHERE status IN ('leased','bound')
    GROUP BY address_id HAVING count(*)>1
  ) THEN
    RAISE EXCEPTION 'cannot restore exclusive address assignments while shared assignments are active';
  END IF;
END $$;

DROP INDEX IF EXISTS address_assignments_active_address_lookup_idx;
CREATE UNIQUE INDEX address_assignments_active_address_idx
  ON address_assignments(address_id)
  WHERE status IN ('leased','bound');

COMMIT;
