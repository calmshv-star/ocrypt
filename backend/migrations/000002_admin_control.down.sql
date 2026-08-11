BEGIN;

DROP FUNCTION IF EXISTS append_admin_audit(uuid,uuid,uuid,uuid,uuid,text,text,text,text,text,bytea,bytea,jsonb,inet,bytea,timestamptz);
DROP TRIGGER IF EXISTS admin_action_transition_guard ON admin_action_requests;
DROP FUNCTION IF EXISTS enforce_admin_action_transition();
DROP TRIGGER IF EXISTS admin_audit_append_only ON admin_audit_log;
DROP FUNCTION IF EXISTS prevent_admin_audit_mutation();
DROP TABLE IF EXISTS admin_audit_log;
DROP TABLE IF EXISTS admin_operator_idempotency;
DROP TABLE IF EXISTS admin_action_requests;
DROP TRIGGER IF EXISTS manual_resolution_actor_guard ON manual_resolutions;
DROP FUNCTION IF EXISTS validate_manual_resolution_actor();
DO $$ BEGIN
  IF EXISTS (SELECT 1 FROM manual_resolutions mr JOIN admin_users u ON u.id IN (mr.requested_by,mr.approved_by)) THEN
    RAISE EXCEPTION 'cannot roll back admin control migration while admin-created manual resolutions exist';
  END IF;
END $$;
ALTER TABLE manual_resolutions DROP CONSTRAINT IF EXISTS manual_resolution_approval_state;
ALTER TABLE manual_resolutions DROP CONSTRAINT IF EXISTS manual_resolution_distinct_actors;
ALTER TABLE manual_resolutions ADD CHECK (approved_by IS NULL OR approved_by <> requested_by);
ALTER TABLE manual_resolutions ADD CHECK (NOT (accept_shortfall OR accept_cross_asset) OR approved_by IS NOT NULL);
ALTER TABLE manual_resolutions ADD FOREIGN KEY (requested_by,tenant_id) REFERENCES api_clients(id,tenant_id);
ALTER TABLE manual_resolutions ADD FOREIGN KEY (approved_by,tenant_id) REFERENCES api_clients(id,tenant_id);
DROP TABLE IF EXISTS admin_sessions;
DROP TABLE IF EXISTS admin_login_attempts;
DROP TABLE IF EXISTS admin_role_bindings;
DROP TABLE IF EXISTS admin_role_permissions;
DROP TABLE IF EXISTS admin_permissions;
DROP TABLE IF EXISTS admin_roles;
DROP TABLE IF EXISTS admin_users;

COMMIT;
