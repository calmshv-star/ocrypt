-- Run before migrations as the schema owner with CREATEROLE, never as a runtime
-- principal. These are NOLOGIN capability roles; credentialed login roles are
-- provisioned externally and inherit exactly one of them.
DO $roles$
DECLARE role_name text;
BEGIN
  FOREACH role_name IN ARRAY ARRAY[
    'merchant_api_runtime','merchant_management_runtime','merchant_admin_runtime',
    'platform_admin_runtime','platform_outbox_publisher','merchant_financial_runtime','rate_runtime_worker',
    'merchant_scanner_worker','merchant_settlement_worker','merchant_matching_worker',
    'merchant_callback_worker','merchant_outbox_worker','merchant_resolution_worker',
    'merchant_proof_worker','merchant_plan_worker','merchant_financial_worker',
    'merchant_reconciliation_worker','merchant_settings_api_runtime',
    'merchant_session_revocation_worker','merchant_invitation_delivery_worker',
    'retention_archive_worker','retention_control_scheduler','merchant_provider_health_worker','legacy_compat_runtime',
    'migration_control_worker','migration_traffic_actuator',
    'legacy_compat_admission_requester','legacy_compat_admission_approver'
  ] LOOP
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname=role_name) THEN
      EXECUTE format('CREATE ROLE %I NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS',role_name);
    END IF;
    -- Converge pre-existing roles too; CREATE options alone do not repair
    -- accidental privilege drift on a subsequent deploy.
    EXECUTE format('ALTER ROLE %I NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS',role_name);
  END LOOP;
END $roles$;

-- Cross-tenant queue claimers require RLS bypass, but retain no DDL, role,
-- database-creation, replication, ownership, or login capability.
ALTER ROLE merchant_scanner_worker BYPASSRLS;
ALTER ROLE merchant_settlement_worker BYPASSRLS;
ALTER ROLE merchant_matching_worker BYPASSRLS;
ALTER ROLE merchant_callback_worker BYPASSRLS;
ALTER ROLE merchant_outbox_worker BYPASSRLS;
ALTER ROLE merchant_resolution_worker BYPASSRLS;
ALTER ROLE merchant_proof_worker BYPASSRLS;
ALTER ROLE merchant_plan_worker BYPASSRLS;
ALTER ROLE merchant_financial_worker BYPASSRLS;
ALTER ROLE merchant_reconciliation_worker BYPASSRLS;
