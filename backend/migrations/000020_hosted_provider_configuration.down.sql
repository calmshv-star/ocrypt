BEGIN;

DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='merchant_api_runtime') THEN
   REVOKE EXECUTE ON FUNCTION hosted_provider_callback_config_admitted(text,text) FROM merchant_api_runtime;
   GRANT EXECUTE ON FUNCTION hosted_provider_callback_admitted(text) TO merchant_api_runtime;
 END IF;
END $$;

CREATE OR REPLACE FUNCTION provider_operation_binding_policy_current(requested_binding uuid) RETURNS boolean
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
  SELECT CASE WHEN b.provider_kind='on_chain' THEN
    (SELECT count(*)=5 AND bool_and(p.approved_at IS NOT NULL AND p.policy_snapshot_id=b.platform_snapshot_id AND p.policy_snapshot_version=s.version AND p.policy_fence_token=h.fence_token)
       FROM public.provider_operation_policies p
       JOIN public.platform_config_snapshots s ON s.id=b.platform_snapshot_id
       JOIN public.platform_config_heads h ON h.snapshot_id=s.id AND h.scope_id=s.scope_id
      WHERE p.binding_id=b.id AND p.operation=ANY(ARRAY['health','head','range','transaction_lookup','transfer_verify']))
    WHEN b.provider_kind='hosted' THEN COALESCE(
      (SELECT count(*)=6 AND count(DISTINCT p.hosted_policy_version_id)=1
         AND bool_and(p.approved_at IS NOT NULL AND p.hosted_policy_version_id=v.id)
         AND v.status='active'
         FROM public.provider_operation_policies p
         JOIN public.provider_hosted_policy_versions v ON v.id=p.hosted_policy_version_id AND v.binding_id=b.id
        WHERE p.binding_id=b.id AND p.operation=ANY(ARRAY['health','create','status','cancel','refund','reconciliation'])
        GROUP BY v.id,v.status),false)
    ELSE false END
  FROM public.provider_operation_bindings b WHERE b.id=requested_binding
$$;
REVOKE ALL ON FUNCTION provider_operation_binding_policy_current(uuid) FROM PUBLIC;

DROP TRIGGER IF EXISTS provider_hosted_policy_bind_config_manifest_before_insert ON provider_hosted_policy_versions;
DROP FUNCTION IF EXISTS provider_hosted_policy_bind_config_manifest();
ALTER TABLE provider_hosted_policy_versions DROP CONSTRAINT IF EXISTS provider_hosted_policy_config_manifest_fk;
ALTER TABLE provider_hosted_policy_versions DROP COLUMN IF EXISTS config_manifest_id;

ALTER TABLE provider_prebind_inbox DROP CONSTRAINT IF EXISTS provider_prebind_config_manifest_fk;
ALTER TABLE provider_inbox DROP CONSTRAINT IF EXISTS provider_inbox_config_manifest_fk;

DROP FUNCTION claim_hosted_prebind_recoveries(timestamptz,timestamptz,integer);
CREATE FUNCTION claim_hosted_prebind_recoveries(claim_now timestamptz,lease_until timestamptz,claim_limit integer)
RETURNS TABLE (
  id uuid,claim_token uuid,attempt integer,tenant_id uuid,merchant_id uuid,provider_id text,route_id text,
  provider_event_id text,provider_reference text,provider_status text,asset_id text,amount_atomic text,
  asset_decimals smallint,raw_body bytea,raw_body_digest bytea,signature_scheme text,signature_key_id text,
  signature_digest bytea,provider_paused_at_receipt boolean,provider_occurred_at timestamptz,received_at timestamptz
)
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
BEGIN
  IF claim_limit<1 OR claim_limit>100 OR lease_until<=claim_now OR lease_until>claim_now+interval '5 minutes' THEN
    RAISE EXCEPTION 'invalid hosted recovery claim bounds' USING ERRCODE='22023';
  END IF;
  RETURN QUERY WITH picked AS (
    SELECT p.id FROM public.provider_prebind_inbox p
    LEFT JOIN public.provider_orders o ON o.provider_id=p.provider_id AND o.provider_reference=p.provider_reference
      AND o.tenant_id=p.tenant_id AND o.merchant_id=p.merchant_id
    WHERE p.state='pending' AND p.next_attempt_at<=claim_now AND(p.claim_token IS NULL OR p.claim_until<claim_now)
      AND(o.id IS NOT NULL OR p.expires_at<=claim_now)
    ORDER BY CASE WHEN o.id IS NOT NULL THEN 0 ELSE 1 END,p.next_attempt_at,p.id
    FOR UPDATE OF p SKIP LOCKED LIMIT claim_limit
  ), claimed AS (
    UPDATE public.provider_prebind_inbox p SET claim_token=gen_random_uuid(),claim_until=lease_until,
      attempt_count=p.attempt_count+1,updated_at=claim_now,version=p.version+1
    FROM picked WHERE p.id=picked.id RETURNING p.*
  )
  SELECT p.id,p.claim_token,p.attempt_count,p.tenant_id,p.merchant_id,p.provider_id,
    COALESCE((SELECT o.route_id::text FROM public.provider_orders o WHERE o.provider_id=p.provider_id
      AND o.provider_reference=p.provider_reference AND o.tenant_id=p.tenant_id AND o.merchant_id=p.merchant_id),''),
    p.provider_event_id,p.provider_reference,p.provider_status,p.asset_id,p.amount_atomic::text,p.asset_decimals,
    p.raw_body,p.raw_body_digest,p.signature_scheme,p.signature_key_id,p.signature_digest,
    p.provider_paused_at_receipt,p.provider_occurred_at,p.received_at
  FROM claimed p ORDER BY p.next_attempt_at,p.id;
END $$;
REVOKE ALL ON FUNCTION claim_hosted_prebind_recoveries(timestamptz,timestamptz,integer) FROM PUBLIC;
DO $$ BEGIN IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='merchant_plan_worker') THEN
  GRANT EXECUTE ON FUNCTION claim_hosted_prebind_recoveries(timestamptz,timestamptz,integer) TO merchant_plan_worker;
END IF; END $$;

ALTER TABLE provider_prebind_inbox DROP COLUMN IF EXISTS config_version;
ALTER TABLE provider_prebind_inbox DROP COLUMN IF EXISTS config_manifest_id;
ALTER TABLE provider_inbox DROP COLUMN IF EXISTS config_version;
ALTER TABLE provider_inbox DROP COLUMN IF EXISTS config_manifest_id;

DROP FUNCTION IF EXISTS hosted_provider_callback_config_admitted(text,text);
DROP FUNCTION IF EXISTS hosted_provider_outbound_config_admitted(uuid,uuid,text,text);
DROP FUNCTION IF EXISTS complete_hosted_provider_config_probe(uuid,text,bigint,boolean,text,bytea,bytea,timestamptz);
DROP FUNCTION IF EXISTS claim_hosted_provider_config_probes(text,integer,timestamptz);
DROP FUNCTION IF EXISTS decide_hosted_provider_config(uuid,uuid,bigint,boolean,text,uuid,text,timestamptz,timestamptz);
DROP FUNCTION IF EXISTS request_hosted_provider_config(uuid,uuid,uuid,text,bigint,jsonb,text,uuid,text,timestamptz,timestamptz);
DROP FUNCTION IF EXISTS provider_config_public_rows(uuid,uuid,integer,uuid);
DROP FUNCTION IF EXISTS hosted_provider_config_manifest_valid(jsonb);

DELETE FROM admin_role_permissions WHERE permission_key IN ('provider_config:read','provider_config:request','provider_config:approve');
DELETE FROM admin_permissions WHERE permission_key IN ('provider_config:read','provider_config:request','provider_config:approve');

DROP TRIGGER IF EXISTS hosted_provider_config_manifest_reject_mutation ON hosted_provider_config_manifests;
DROP FUNCTION IF EXISTS hosted_provider_config_manifest_immutable();
DROP TABLE IF EXISTS hosted_provider_config_idempotency;
DROP TABLE IF EXISTS hosted_provider_config_probe_incidents;
DROP TABLE IF EXISTS hosted_provider_config_heads;
DROP TABLE IF EXISTS hosted_provider_config_workflows;
DROP TABLE IF EXISTS hosted_provider_config_manifests;
DROP TYPE IF EXISTS hosted_provider_config_public;

COMMIT;
