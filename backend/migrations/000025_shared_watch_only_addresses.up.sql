BEGIN;

-- Watch-only migrations can legitimately have one receiving address per
-- chain. Exact-amount reservations, rather than exclusive address ownership,
-- disambiguate concurrent invoices on those shared addresses.
DROP INDEX IF EXISTS address_assignments_active_address_idx;
CREATE INDEX address_assignments_active_address_lookup_idx
  ON address_assignments(address_id,valid_until)
  WHERE status IN ('leased','bound');

COMMIT;
