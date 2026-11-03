-- Self-service owners may create payment links and API clients through the
-- management API: the actor check now accepts the merchant_owner projection.
BEGIN;

CREATE OR REPLACE FUNCTION validate_management_actor(p_actor uuid,p_tenant uuid,p_merchant uuid,p_permission text)
RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE allowed boolean;
BEGIN
    SELECT EXISTS(SELECT 1 FROM public.api_clients c WHERE c.id=p_actor AND c.tenant_id=p_tenant AND c.merchant_id=p_merchant AND c.revoked_at IS NULL AND c.valid_from<=clock_timestamp() AND (c.valid_until IS NULL OR c.valid_until>clock_timestamp())
      AND ('management:*'=ANY(c.scopes) OR CASE p_permission
        WHEN 'payment_links:read' THEN c.scopes && ARRAY['payment-links:read','payment-links:write']::text[]
        WHEN 'payment_links:write' THEN 'payment-links:write'=ANY(c.scopes)
        WHEN 'checkout:write' THEN 'checkout:write'=ANY(c.scopes)
        WHEN 'webhook_settings:read' THEN c.scopes && ARRAY['webhooks:read','webhooks:write']::text[]
        WHEN 'webhook_settings:write' THEN 'webhooks:write'=ANY(c.scopes)
        WHEN 'webhook_settings:rotate' THEN 'webhooks:rotate'=ANY(c.scopes)
        WHEN 'webhook_settings:disable' THEN 'webhooks:disable'=ANY(c.scopes)
        WHEN 'api_clients:read' THEN c.scopes && ARRAY['credentials:read','credentials:write']::text[]
        WHEN 'api_clients:write' THEN 'credentials:write'=ANY(c.scopes)
        WHEN 'api_clients:rotate' THEN 'credentials:rotate'=ANY(c.scopes)
        WHEN 'api_clients:revoke' THEN 'credentials:revoke'=ANY(c.scopes)
        WHEN 'management_audit:read' THEN 'audit:read'=ANY(c.scopes)
        ELSE false END)) INTO allowed;
    IF NOT allowed THEN
        SELECT EXISTS(SELECT 1 FROM public.admin_users u JOIN public.admin_role_bindings b ON b.user_id=u.id
          JOIN public.admin_role_permissions rp ON rp.role_key=b.role_key AND rp.permission_key=p_permission
          WHERE u.id=p_actor AND u.status='active' AND b.revoked_at IS NULL AND (b.expires_at IS NULL OR b.expires_at>clock_timestamp())
            AND (b.tenant_id IS NULL OR b.tenant_id=p_tenant) AND (b.merchant_id IS NULL OR b.merchant_id=p_merchant)) INTO allowed;
    END IF;
    -- A self-service owner holds merchant_owner by projection from an
    -- active owner membership (list_current_admin_self_service_owner_grants),
    -- never as a stored binding, so the cabinet let it act while this check
    -- refused every payment link, API client and audit row it wrote.
    IF NOT allowed THEN
        SELECT EXISTS(SELECT 1 FROM public.admin_users u
          JOIN public.merchant_members mm ON mm.admin_user_id=u.id AND mm.tenant_id=p_tenant AND mm.merchant_id=p_merchant AND mm.status='active'
            AND mm.oidc_issuer=u.oidc_issuer AND mm.oidc_subject=u.oidc_subject AND lower(mm.email)=lower(u.email)
          JOIN public.tenants t ON t.id=p_tenant AND t.status='active'
          JOIN public.merchants m ON m.id=p_merchant AND m.tenant_id=p_tenant AND m.status='active'
          JOIN public.merchant_signups s ON s.merchant_id=p_merchant AND s.tenant_id=p_tenant
          JOIN public.merchant_member_role_bindings b ON b.member_id=mm.id AND b.tenant_id=p_tenant AND b.merchant_id=p_merchant AND b.role_key='owner' AND b.revoked_at IS NULL
          JOIN public.admin_role_permissions rp ON rp.role_key='merchant_owner' AND rp.permission_key=p_permission
          WHERE u.id=p_actor AND u.status='active') INTO allowed;
    END IF;
    RETURN allowed;
END $$;

COMMIT;
