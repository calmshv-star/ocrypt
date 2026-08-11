BEGIN;

DROP FUNCTION IF EXISTS consume_management_assertion(uuid,uuid,timestamptz);
DROP TABLE IF EXISTS management_assertion_nonces;
DROP TRIGGER IF EXISTS payment_links_actor_guard ON payment_links;
DROP TRIGGER IF EXISTS management_api_clients_actor_guard ON management_api_clients;
DROP TRIGGER IF EXISTS management_action_requests_actor_guard ON management_action_requests;
DROP FUNCTION IF EXISTS validate_management_actor_columns();
DROP FUNCTION IF EXISTS append_management_audit(uuid,uuid,uuid,uuid,text,uuid,text,text,uuid,text,jsonb,timestamptz);
DROP FUNCTION IF EXISTS management_permission_for_action(text);
DROP FUNCTION IF EXISTS validate_management_actor(uuid,uuid,uuid,text);
DROP TRIGGER IF EXISTS management_audit_immutable ON management_audit_log;
DROP FUNCTION IF EXISTS reject_management_audit_mutation();
DROP TABLE IF EXISTS management_audit_log;
DROP TABLE IF EXISTS management_action_requests;
DROP TABLE IF EXISTS management_idempotency_records;
DROP TABLE IF EXISTS management_api_client_versions;
DROP TABLE IF EXISTS management_api_clients;
DROP TABLE IF EXISTS management_webhook_verifications;
ALTER TABLE callback_deliveries DROP CONSTRAINT IF EXISTS callback_delivery_signing_key_fk;
ALTER TABLE callback_deliveries DROP COLUMN IF EXISTS signing_key_id;
DROP TABLE IF EXISTS management_webhook_signing_keys;

DROP FUNCTION IF EXISTS lookup_payment_link(bytea);
DROP FUNCTION IF EXISTS lookup_checkout_session(bytea);
CREATE FUNCTION lookup_checkout_session(requested_hash bytea)
RETURNS TABLE (tenant_id uuid, merchant_id uuid, intent_id uuid)
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path=pg_catalog,public SET row_security=off AS $$
    SELECT cs.tenant_id,cs.merchant_id,cs.intent_id
    FROM public.checkout_sessions cs
    JOIN public.tenants t ON t.id=cs.tenant_id AND t.status='active'
    JOIN public.merchants m ON m.id=cs.merchant_id AND m.tenant_id=cs.tenant_id AND m.status='active'
    WHERE cs.token_hash=requested_hash AND cs.revoked_at IS NULL LIMIT 1
$$;
REVOKE ALL ON FUNCTION lookup_checkout_session(bytea) FROM PUBLIC;

-- This table owns a foreign key backed by
-- checkout_sessions_redemption_identity_unique, so remove the dependent table
-- before removing that unique constraint.
DROP TABLE IF EXISTS payment_link_redemptions;

ALTER TABLE checkout_sessions
    DROP CONSTRAINT IF EXISTS checkout_sessions_selected_route_fk,
    DROP CONSTRAINT IF EXISTS checkout_sessions_payment_link_fk,
    DROP CONSTRAINT IF EXISTS checkout_sessions_nonce_unique,
    DROP CONSTRAINT IF EXISTS checkout_sessions_redemption_identity_unique,
    DROP COLUMN IF EXISTS audience,
    DROP COLUMN IF EXISTS allowed_origin,
    DROP COLUMN IF EXISTS allowed_actions,
    DROP COLUMN IF EXISTS selected_route_id,
    DROP COLUMN IF EXISTS payment_link_id,
    DROP COLUMN IF EXISTS session_nonce,
    DROP COLUMN IF EXISTS selection_idempotency_key,
    DROP COLUMN IF EXISTS selection_request_hash,
    DROP COLUMN IF EXISTS version;
DROP INDEX IF EXISTS checkout_sessions_intent_multi_idx;

DROP TABLE IF EXISTS payment_links;

ALTER TABLE payment_routes DROP CONSTRAINT IF EXISTS payment_routes_intent_tenant_unique;
ALTER TABLE webhook_endpoints DROP CONSTRAINT IF EXISTS webhook_endpoints_tenant_merchant_unique;

-- Fails safely if multiple tokens now exist for an intent; operators must not
-- roll back a live management deployment without first draining those rows.
ALTER TABLE checkout_sessions ADD CONSTRAINT checkout_sessions_intent_id_tenant_id_key UNIQUE(intent_id,tenant_id);

DELETE FROM admin_role_permissions WHERE permission_key IN('payment_links:read','payment_links:write','checkout:write','webhook_settings:read','webhook_settings:write','webhook_settings:rotate','webhook_settings:disable','api_clients:read','api_clients:write','api_clients:rotate','api_clients:revoke','management_audit:read');
DELETE FROM admin_permissions WHERE permission_key IN('payment_links:read','payment_links:write','checkout:write','webhook_settings:read','webhook_settings:write','webhook_settings:rotate','webhook_settings:disable','api_clients:read','api_clients:write','api_clients:rotate','api_clients:revoke','management_audit:read');

COMMIT;
