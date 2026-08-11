BEGIN;
CREATE OR REPLACE FUNCTION management_permission_for_action(p_action text)
RETURNS text LANGUAGE sql IMMUTABLE SET search_path=pg_catalog,public AS $$
    SELECT CASE
      WHEN p_action LIKE 'payment_link.%' THEN 'payment_links:write'
      WHEN p_action LIKE 'checkout.%' THEN 'checkout:write'
      WHEN p_action='webhook.secret_rotated' THEN 'webhook_settings:rotate'
      WHEN p_action='webhook.disabled' THEN 'webhook_settings:disable'
      WHEN p_action IN ('webhook.disable_requested','webhook.disable_rejected') THEN 'webhook_settings:disable'
      WHEN p_action LIKE 'webhook.%' THEN 'webhook_settings:write'
      WHEN p_action='api_client.rotated' THEN 'api_clients:rotate'
      WHEN p_action='api_client.revoked' THEN 'api_clients:revoke'
      WHEN p_action IN ('api_client.revoke_requested','api_client.revoke_rejected') THEN 'api_clients:revoke'
      WHEN p_action LIKE 'api_client.%' THEN 'api_clients:write'
      ELSE NULL END
$$;
DELETE FROM admin_role_permissions WHERE permission_key IN ('matching_policy:read','matching_policy:write','matching_policy:approve','matching_policy:activate');
DELETE FROM admin_permissions WHERE permission_key IN ('matching_policy:read','matching_policy:write','matching_policy:approve','matching_policy:activate');
DROP POLICY IF EXISTS automated_matching_jobs_tenant_policy ON automated_matching_jobs;
DROP POLICY IF EXISTS automated_matching_decisions_tenant_policy ON automated_matching_decisions;
DROP POLICY IF EXISTS payment_match_aggregates_tenant_policy ON payment_match_aggregates;
DROP POLICY IF EXISTS payment_route_policy_bindings_tenant_policy ON payment_route_policy_bindings;
DROP POLICY IF EXISTS automated_matching_policies_tenant_policy ON automated_matching_policies;
DROP TRIGGER IF EXISTS automated_matching_decisions_append_only ON automated_matching_decisions;
DROP TRIGGER IF EXISTS payment_route_policy_bindings_append_only ON payment_route_policy_bindings;
DROP TRIGGER IF EXISTS automated_matching_policies_append_only ON automated_matching_policies;
DROP FUNCTION IF EXISTS prevent_automated_matching_history_mutation();
DROP TABLE IF EXISTS automated_matching_jobs;
DROP TABLE IF EXISTS automated_matching_decisions;
ALTER TABLE payment_matches DROP CONSTRAINT IF EXISTS payment_matches_aggregate_fk;
DROP INDEX IF EXISTS payment_matches_aggregate_idx;
ALTER TABLE payment_matches DROP COLUMN IF EXISTS allocation_role;
ALTER TABLE payment_matches DROP COLUMN IF EXISTS aggregate_id;
DROP TABLE IF EXISTS payment_match_aggregates;
DROP TRIGGER IF EXISTS payment_route_bind_matching_policy ON payment_routes;
DROP FUNCTION IF EXISTS bind_route_matching_policy();
DROP TABLE IF EXISTS payment_route_policy_bindings;
DROP TABLE IF EXISTS automated_matching_policies;
DROP TABLE IF EXISTS automated_matching_policy_idempotency;
DROP TABLE IF EXISTS automated_matching_policy_changes;
COMMIT;
