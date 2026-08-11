BEGIN;

DROP FUNCTION IF EXISTS provider_health_worker_status(timestamptz);
DROP FUNCTION IF EXISTS load_hosted_provider_health_probe(uuid,text,bigint,bigint);
DROP FUNCTION IF EXISTS complete_provider_health_probe(uuid,text,text,bigint,bigint,boolean,text,integer,bigint,timestamptz);
DROP FUNCTION IF EXISTS claim_provider_health_probes(text,integer,timestamptz);
DROP FUNCTION IF EXISTS decide_hosted_provider_policy(uuid,uuid,bigint,boolean,text,uuid,text,timestamptz,timestamptz);
DROP FUNCTION IF EXISTS request_hosted_provider_policy(uuid,uuid,uuid,bigint,jsonb,text,text,uuid,text,timestamptz,timestamptz);
DROP FUNCTION IF EXISTS decide_provider_operation_change(uuid,uuid,bigint,boolean,text,uuid,text,timestamptz,timestamptz);
DROP FUNCTION IF EXISTS request_provider_operation_change(uuid,uuid,uuid,text,bigint,text,uuid,text,timestamptz,timestamptz);
DROP FUNCTION IF EXISTS provider_operation_binding_health_ready(uuid,timestamptz);
DROP FUNCTION IF EXISTS provider_operation_binding_policy_current(uuid);

CREATE OR REPLACE FUNCTION hosted_provider_callback_admitted(requested_provider_id text)
RETURNS TABLE (
  id text,tenant_id uuid,merchant_id uuid,adapter_kind text,api_origin text,
  create_path text,cancel_path text,status_path text,refund_path text,reconcile_path text,
  payment_url_origins text[],api_credential_ref text,api_key_id text,callback_secret_ref text,
  callback_key_id text,signature_scheme text,asset_id text,asset_decimals smallint,currency text,status text
)
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path=pg_catalog,public SET row_security=off AS $$
SELECT c.id,c.tenant_id,c.merchant_id,c.adapter_kind,c.api_origin,c.create_path,c.cancel_path,c.status_path,c.refund_path,c.reconcile_path,
       c.payment_url_origins,c.api_credential_ref,c.api_key_id,c.callback_secret_ref,c.callback_key_id,c.signature_scheme,c.asset_id,c.asset_decimals,c.currency::text,c.status
FROM public.hosted_provider_configs c
JOIN public.tenants t ON t.id=c.tenant_id AND t.status='active'
JOIN public.merchants m ON m.id=c.merchant_id AND m.tenant_id=c.tenant_id AND m.status='active'
WHERE c.id=requested_provider_id AND c.status='active'
$$;
REVOKE ALL ON FUNCTION hosted_provider_callback_admitted(text) FROM PUBLIC;
DO $$ BEGIN IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='merchant_api_runtime') THEN GRANT EXECUTE ON FUNCTION hosted_provider_callback_admitted(text) TO merchant_api_runtime; END IF; END $$;

CREATE OR REPLACE FUNCTION claim_hosted_create_recoveries(claim_now timestamptz,lease_until timestamptz,claim_limit integer)
RETURNS TABLE (
  id uuid,claim_token uuid,attempt integer,tenant_id uuid,merchant_id uuid,provider_id text,intent_id uuid,
  idempotency_key text,request_hash text,asset_id text,fiat_amount_minor text,currency text,currency_scale smallint,
  expires_at timestamptz,create_state text,provider_order_id text,provider_reference text,payment_url text,
  amount_atomic text,asset_decimals smallint,quote_id text,rate_numerator text,rate_denominator text,
  quote_issued_at timestamptz,create_response_body bytea,create_response_digest bytea,create_response_received_at timestamptz
)
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
BEGIN
  IF claim_limit<1 OR claim_limit>100 OR lease_until<=claim_now OR lease_until>claim_now+interval '5 minutes' THEN
    RAISE EXCEPTION 'invalid hosted recovery claim bounds' USING ERRCODE='22023';
  END IF;
  RETURN QUERY
  WITH picked AS (
    SELECT a.id FROM public.hosted_provider_create_attempts a
    JOIN public.payment_intents i ON i.id=a.intent_id AND i.tenant_id=a.tenant_id AND i.merchant_id=a.merchant_id
    WHERE a.recovery_status='pending' AND a.next_recovery_at<=claim_now
      AND (a.state IN('retry','completed') OR a.state='claimed' AND a.claim_until<claim_now)
    ORDER BY a.next_recovery_at,a.id FOR UPDATE OF a SKIP LOCKED LIMIT claim_limit
  ), claimed AS (
    UPDATE public.hosted_provider_create_attempts a
    SET recovery_status='claimed',recovery_claim_token=gen_random_uuid(),recovery_claim_until=lease_until,
        recovery_attempt_count=a.recovery_attempt_count+1,updated_at=claim_now,version=a.version+1
    FROM picked WHERE a.id=picked.id RETURNING a.*
  )
  SELECT a.id,a.recovery_claim_token,a.recovery_attempt_count,a.tenant_id,a.merchant_id,a.provider_id,a.intent_id,
    a.idempotency_key,encode(a.request_hash,'hex'),a.asset_id,a.fiat_amount_minor::text,a.currency::text,a.currency_scale,
    a.expires_at,a.state,COALESCE(a.provider_order_id::text,''),COALESCE(a.provider_reference,''),COALESCE(a.payment_url,''),
    COALESCE(a.amount_atomic::text,''),COALESCE(a.asset_decimals,0),COALESCE(a.quote_id,''),
    COALESCE(a.rate_numerator::text,''),COALESCE(a.rate_denominator::text,''),a.quote_issued_at,
    COALESCE(a.create_response_body,''::bytea),COALESCE(a.create_response_digest,''::bytea),a.create_response_received_at
  FROM claimed a ORDER BY a.next_recovery_at,a.id;
END $$;

CREATE OR REPLACE FUNCTION claim_hosted_order_recoveries(claim_now timestamptz,lease_until timestamptz,claim_limit integer)
RETURNS TABLE (
  id uuid,claim_token uuid,attempt integer,tenant_id uuid,merchant_id uuid,route_id uuid,provider_id text,
  provider_reference text,asset_id text,amount_atomic text,asset_decimals smallint
)
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
BEGIN
  IF claim_limit<1 OR claim_limit>100 OR lease_until<=claim_now OR lease_until>claim_now+interval '5 minutes' THEN
    RAISE EXCEPTION 'invalid hosted recovery claim bounds' USING ERRCODE='22023';
  END IF;
  RETURN QUERY
  WITH picked AS (
    SELECT o.id FROM public.provider_orders o
    WHERE o.provider_status IN('pending','authorized','cancel_requested') AND o.next_reconcile_at<=claim_now
      AND (o.reconcile_claim_token IS NULL OR o.reconcile_claim_until<claim_now)
    ORDER BY o.next_reconcile_at,o.id FOR UPDATE OF o SKIP LOCKED LIMIT claim_limit
  ), claimed AS (
    UPDATE public.provider_orders o
    SET reconcile_claim_token=gen_random_uuid(),reconcile_claim_until=lease_until,
        reconcile_attempt_count=o.reconcile_attempt_count+1,updated_at=claim_now,version=o.version+1
    FROM picked WHERE o.id=picked.id RETURNING o.*
  )
  SELECT o.id,o.reconcile_claim_token,o.reconcile_attempt_count,o.tenant_id,o.merchant_id,o.route_id,
    o.provider_id,o.provider_reference,o.asset_id,o.amount_atomic::text,o.asset_decimals
  FROM claimed o ORDER BY o.next_reconcile_at,o.id;
END $$;

DROP FUNCTION IF EXISTS admit_hosted_provider_operation(uuid,uuid,text,text,timestamptz);
DROP TRIGGER IF EXISTS provider_operation_hosted_config_sync ON hosted_provider_configs;
DROP FUNCTION IF EXISTS provider_operation_sync_hosted_config();
DROP TRIGGER IF EXISTS provider_operation_rpc_head_sync ON platform_config_heads;
DROP FUNCTION IF EXISTS provider_operation_sync_rpc_head();
DROP FUNCTION IF EXISTS provider_operation_apply_rpc_policy(uuid,uuid,timestamptz);
DROP TRIGGER IF EXISTS provider_operation_seed_policy_after_insert ON provider_operation_bindings;
DROP FUNCTION IF EXISTS provider_operation_seed_policy();
DROP TRIGGER IF EXISTS provider_hosted_policy_evidence_immutable_before_update ON provider_hosted_policy_versions;
DROP FUNCTION IF EXISTS provider_hosted_policy_evidence_immutable();

DELETE FROM admin_role_permissions WHERE permission_key IN ('provider_ops:read','provider_ops:request','provider_ops:approve');
DELETE FROM admin_permissions WHERE permission_key IN ('provider_ops:read','provider_ops:request','provider_ops:approve');

DROP TABLE IF EXISTS provider_operation_idempotency;
DROP TABLE IF EXISTS provider_operation_change_requests;
DROP TABLE IF EXISTS provider_health_observations;
DROP TABLE IF EXISTS provider_operation_rate_windows;
DROP TABLE IF EXISTS provider_circuit_states;
DROP TABLE IF EXISTS provider_operation_policies;
DROP TABLE IF EXISTS provider_hosted_policy_versions;
DROP TABLE IF EXISTS provider_operation_bindings;
DROP FUNCTION IF EXISTS provider_operation_policy_payload_valid(jsonb,text[]);

COMMIT;
