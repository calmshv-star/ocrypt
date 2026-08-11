BEGIN;

-- Financial cabinet permissions are deliberately separate from the older
-- reconciliation dashboard permission. They form the complete, closed set an
-- admin BFF is allowed to project into a financial-api assertion.
INSERT INTO admin_permissions(permission_key,description) VALUES
('financial:read','Read treasury, refund, and financial reconciliation records'),
('financial:sweep_create','Request a policy-constrained treasury sweep'),
('financial:sweep_cancel','Cancel a sweep before custody execution'),
('financial:sweep_approve','Independently approve a treasury sweep'),
('financial:refund_create','Request a policy-constrained refund'),
('financial:refund_cancel','Cancel a refund before custody execution'),
('financial:refund_approve','Independently approve a refund'),
('financial:reconciliation_request','Request a financial reconciliation run'),
('financial:reconciliation_execute','Execute a requested reconciliation run');

INSERT INTO admin_role_permissions(role_key,permission_key) VALUES
('treasury_operator','financial:read'),
('treasury_operator','financial:sweep_create'),
('treasury_operator','financial:sweep_cancel'),
('treasury_operator','financial:refund_create'),
('treasury_operator','financial:refund_cancel'),
('treasury_operator','financial:reconciliation_request'),
('senior_approver','financial:read'),
('senior_approver','financial:sweep_approve'),
('senior_approver','financial:refund_approve'),
('senior_approver','financial:reconciliation_execute');

-- This narrow projection is the only source for financial proxy grants. The
-- caller supplies a tenant, never a permission. Global bindings and bindings
-- for that exact tenant are admitted; merchant-scoped bindings are excluded.
CREATE FUNCTION list_current_admin_financial_permissions(requested_tenant uuid)
RETURNS TABLE(permission_key text)
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path=pg_catalog,public
SET row_security=off AS $$
  SELECT DISTINCT rp.permission_key
    FROM public.admin_users u
    JOIN public.admin_role_bindings b ON b.user_id=u.id
      AND b.merchant_id IS NULL
      AND (b.tenant_id IS NULL OR b.tenant_id=requested_tenant)
      AND b.revoked_at IS NULL
      AND (b.expires_at IS NULL OR b.expires_at>clock_timestamp())
    JOIN public.admin_role_permissions rp ON rp.role_key=b.role_key
    JOIN public.tenants t ON t.id=requested_tenant AND t.status='active'
   WHERE u.id=nullif(current_setting('app.admin_user_id',true),'')::uuid
     AND u.status='active'
     AND rp.permission_key IN (
       'financial:read','financial:sweep_create','financial:sweep_cancel','financial:sweep_approve',
       'financial:refund_create','financial:refund_cancel','financial:refund_approve',
       'financial:reconciliation_request','financial:reconciliation_execute'
     )
   ORDER BY rp.permission_key
$$;
REVOKE ALL ON FUNCTION list_current_admin_financial_permissions(uuid) FROM PUBLIC;

-- Decision mutations use a method/path/body fingerprint and retain the exact
-- response in the same transaction as aggregate, audit, ledger, and outbox.
CREATE TABLE financial_operator_idempotency (
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE RESTRICT,
  actor_id text NOT NULL CHECK(length(actor_id) BETWEEN 1 AND 255),
  operation text NOT NULL CHECK(operation IN (
    'sweep.approve','sweep.cancel','refund.approve','refund.cancel','reconciliation.execute'
  )),
  idempotency_key text NOT NULL CHECK(length(idempotency_key) BETWEEN 8 AND 255 AND idempotency_key=btrim(idempotency_key)),
  request_fingerprint bytea NOT NULL CHECK(octet_length(request_fingerprint)=32),
  response jsonb NOT NULL CHECK(jsonb_typeof(response)='object' AND pg_column_size(response)<=262144),
  created_at timestamptz NOT NULL,
  PRIMARY KEY(tenant_id,actor_id,operation,idempotency_key)
);
ALTER TABLE financial_operator_idempotency ENABLE ROW LEVEL SECURITY;
ALTER TABLE financial_operator_idempotency FORCE ROW LEVEL SECURITY;
CREATE POLICY financial_operator_idempotency_tenant_policy ON financial_operator_idempotency
  USING(tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid)
  WITH CHECK(tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid);
REVOKE ALL ON financial_operator_idempotency FROM PUBLIC;

DO $$ BEGIN
  IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='merchant_admin_runtime') THEN
    GRANT EXECUTE ON FUNCTION list_current_admin_financial_permissions(uuid) TO merchant_admin_runtime;
  END IF;
  IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='merchant_financial_runtime') THEN
    GRANT SELECT,INSERT ON financial_operator_idempotency TO merchant_financial_runtime;
  END IF;
END $$;

COMMIT;
