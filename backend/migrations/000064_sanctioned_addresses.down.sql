BEGIN;

DROP TRIGGER IF EXISTS addresses_screen_deposit ON addresses;
DROP TRIGGER IF EXISTS merchant_go_live_addresses_screen ON merchant_go_live_addresses;
DROP TRIGGER IF EXISTS transfer_events_screen_sender ON transfer_events;
DROP FUNCTION IF EXISTS list_sanctioned_transfer_alerts(uuid,uuid,integer);
DROP FUNCTION IF EXISTS refuse_sanctioned_receiving_address();
DROP FUNCTION IF EXISTS screen_transfer_sender();
DROP FUNCTION IF EXISTS enqueue_sanctioned_transfer_notification(uuid,uuid,text,jsonb);
DROP FUNCTION IF EXISTS sanctioned_address_matches(text,text);
DROP FUNCTION IF EXISTS replace_sanctioned_addresses(text,jsonb);
DELETE FROM notification_jobs WHERE event='sanctioned';
ALTER TABLE notification_jobs DROP CONSTRAINT notification_jobs_event_check;
ALTER TABLE notification_jobs ADD CONSTRAINT notification_jobs_event_check
  CHECK (event IN ('paid','underpaid','review','webhook_failed','sign_in'));
-- The tenant-scoped access check of 000063.
CREATE OR REPLACE FUNCTION notification_recipient_allowed(p_user uuid,p_tenant uuid,p_merchant uuid,p_permission text) RETURNS boolean
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path=pg_catalog,public SET row_security=off AS $$
  SELECT EXISTS (
    SELECT 1 FROM public.admin_users u
      JOIN public.tenants t ON t.id=p_tenant AND t.status='active'
      JOIN public.merchants m ON m.id=p_merchant AND m.tenant_id=p_tenant AND m.status='active'
     WHERE u.id=p_user AND u.status='active' AND p_permission IN ('payments:read','webhooks:read') AND (
       EXISTS (SELECT 1 FROM public.admin_role_bindings b
                 JOIN public.admin_role_permissions rp ON rp.role_key=b.role_key AND rp.permission_key=p_permission
                WHERE b.user_id=u.id AND b.revoked_at IS NULL AND (b.expires_at IS NULL OR b.expires_at>now())
                  AND b.tenant_id=p_tenant AND (b.merchant_id IS NULL OR b.merchant_id=p_merchant))
       OR EXISTS (SELECT 1 FROM public.notification_default_owners(p_tenant,p_merchant) o
                    JOIN public.admin_role_permissions rp ON rp.role_key='merchant_owner' AND rp.permission_key=p_permission
                   WHERE o.user_id=u.id)))
$$;
DROP TABLE IF EXISTS sanctioned_transfer_alerts;
DROP TABLE IF EXISTS sanctioned_addresses;

COMMIT;
