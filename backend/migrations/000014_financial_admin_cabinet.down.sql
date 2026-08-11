BEGIN;

DO $$ BEGIN
  IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='merchant_admin_runtime') THEN
    REVOKE EXECUTE ON FUNCTION list_current_admin_financial_permissions(uuid) FROM merchant_admin_runtime;
  END IF;
  IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='merchant_financial_runtime') THEN
    REVOKE ALL ON financial_operator_idempotency FROM merchant_financial_runtime;
  END IF;
END $$;

DROP TABLE financial_operator_idempotency;
DROP FUNCTION list_current_admin_financial_permissions(uuid);
DELETE FROM admin_role_permissions WHERE permission_key IN (
  'financial:read','financial:sweep_create','financial:sweep_cancel','financial:sweep_approve',
  'financial:refund_create','financial:refund_cancel','financial:refund_approve',
  'financial:reconciliation_request','financial:reconciliation_execute'
);
DELETE FROM admin_permissions WHERE permission_key IN (
  'financial:read','financial:sweep_create','financial:sweep_cancel','financial:sweep_approve',
  'financial:refund_create','financial:refund_cancel','financial:refund_approve',
  'financial:reconciliation_request','financial:reconciliation_execute'
);

COMMIT;
