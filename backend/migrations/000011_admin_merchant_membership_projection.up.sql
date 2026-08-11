BEGIN;

-- The admin BFF may project only the currently authenticated admin user's
-- active merchant-cabinet bindings into its session principal. The caller
-- cannot enumerate another user and receives only the closed merchant
-- permission catalogue required by Team and non-financial Settings.
CREATE FUNCTION list_current_admin_merchant_memberships()
RETURNS TABLE(binding_id uuid,tenant_id uuid,merchant_id uuid,role_key text,permission_key text)
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path=pg_catalog,public
SET row_security=off AS $$
  WITH actor AS (
    SELECT u.id,u.oidc_issuer,u.oidc_subject,lower(u.email) AS email
      FROM public.admin_users u
     WHERE u.id=nullif(current_setting('app.admin_user_id',true),'')::uuid
       AND u.status='active'
  )
  SELECT b.id,m.tenant_id,m.merchant_id,b.role_key,rp.permission_key
    FROM actor u
    JOIN public.merchant_members m ON m.admin_user_id=u.id
      AND m.status='active'
      AND m.oidc_issuer=u.oidc_issuer
      AND m.oidc_subject=u.oidc_subject
      AND lower(m.email)=u.email
    JOIN public.tenants t ON t.id=m.tenant_id AND t.status='active'
    JOIN public.merchants merchant ON merchant.id=m.merchant_id AND merchant.tenant_id=m.tenant_id AND merchant.status='active'
    JOIN public.merchant_member_role_bindings b ON b.member_id=m.id
      AND b.tenant_id=m.tenant_id AND b.merchant_id=m.merchant_id AND b.revoked_at IS NULL
    JOIN public.merchant_cabinet_role_permissions rp ON rp.role_key=b.role_key
   WHERE rp.permission_key IN (
     'team:read','team:invite','team:manage','team:security_request','team:security_approve',
     'settings:read','settings:write'
   )
   ORDER BY m.tenant_id,m.merchant_id,b.id,rp.permission_key
$$;

REVOKE ALL ON FUNCTION list_current_admin_merchant_memberships() FROM PUBLIC;

COMMIT;
