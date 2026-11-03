BEGIN;

-- A pending request owns a pending policy change; refuse to strand either.
-- Merchants already switched to live and their activated policies stay; only
-- the request records go. A pending request is refused so nothing is stranded.
DO $$
BEGIN
  -- Nested so the table is resolved only when it exists (a second run).
  IF to_regclass('public.merchant_go_live_requests') IS NOT NULL THEN
    IF EXISTS (SELECT 1 FROM merchant_go_live_requests WHERE status='pending') THEN
      RAISE EXCEPTION 'decide every pending go-live request before rolling back 000061';
    END IF;
  END IF;
END $$;

DROP FUNCTION IF EXISTS decide_go_live_request(uuid,text,text,timestamptz);
DROP FUNCTION IF EXISTS prepare_go_live_wallets(uuid,timestamptz);
DROP FUNCTION IF EXISTS list_go_live_requests(integer);
DROP FUNCTION IF EXISTS go_live_route_admitted(uuid,uuid,text);
DROP FUNCTION IF EXISTS go_live_address_admitted(uuid,uuid,text,text);
DROP FUNCTION IF EXISTS merchant_go_live_state(uuid,uuid);
DROP FUNCTION IF EXISTS submit_merchant_go_live_request(uuid,uuid,uuid,text,jsonb,timestamptz);
DROP FUNCTION IF EXISTS current_admin_owns_merchant(uuid,uuid);
DROP FUNCTION IF EXISTS current_admin_member_of_merchant(uuid,uuid);
DROP TABLE IF EXISTS merchant_go_live_addresses;
DROP TABLE IF EXISTS merchant_go_live_requests;
DROP TABLE IF EXISTS acceptance_presets;
DELETE FROM admin_role_permissions WHERE permission_key IN ('go_live:request','go_live_reviews:read','go_live_reviews:decide');
DELETE FROM admin_permissions WHERE permission_key IN ('go_live:request','go_live_reviews:read','go_live_reviews:decide');

COMMIT;
