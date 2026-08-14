BEGIN;

-- Provider request-rate policy is an operational counter, not a monetary
-- amount. Keeping rate_limit and failure_threshold numeric is required by the
-- provider-operations contract; every genuinely money-like JSON field remains
-- an exact decimal string.
CREATE OR REPLACE FUNCTION platform_exact_money_strings(value jsonb) RETURNS boolean
LANGUAGE plpgsql IMMUTABLE PARALLEL SAFE AS $$
DECLARE k text; v jsonb;
BEGIN
    IF jsonb_typeof(value) = 'object' THEN
        FOR k,v IN SELECT item.key,item.value FROM jsonb_each(value) AS item(key,value) LOOP
            IF k NOT IN ('rate_limit','failure_threshold') AND k ~* '(amount|balance|minimum|maximum|threshold|dust|fee|limit)'
               AND jsonb_typeof(v) NOT IN ('string','null') THEN RETURN false; END IF;
            IF k NOT IN ('rate_limit','failure_threshold') AND jsonb_typeof(v) = 'string' AND k ~* '(amount|balance|minimum|maximum|threshold|dust|fee|limit)'
               AND trim(both '"' from v::text) !~ '^(0|[1-9][0-9]{0,77})$' THEN RETURN false; END IF;
            IF NOT platform_exact_money_strings(v) THEN RETURN false; END IF;
        END LOOP;
    ELSIF jsonb_typeof(value) = 'array' THEN
        FOR v IN SELECT item.value FROM jsonb_array_elements(value) AS item(value) LOOP
            IF NOT platform_exact_money_strings(v) THEN RETURN false; END IF;
        END LOOP;
    END IF;
    RETURN true;
END $$;

-- Migration 000017 originally projected a non-existent snapshots.updated_at
-- column while synchronizing newly admitted RPC heads. Existing installations
-- did not expose the defect until a new rpc_provider head was inserted. The
-- head row already supplies the authoritative activation timestamp.
CREATE OR REPLACE FUNCTION provider_operation_sync_rpc_head() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE s record; provider text; chain_ref text; binding uuid; valid_policy boolean;
BEGIN
  IF NEW.kind<>'rpc_provider'::public.platform_config_kind OR NEW.tenant_id IS NOT NULL THEN RETURN NULL; END IF;
  SELECT id,logical_key,payload INTO s
    FROM public.platform_config_snapshots WHERE id=NEW.snapshot_id;
  provider := s.payload->>'provider_id'; chain_ref := s.payload->>'chain_ref';
  IF provider IS NULL OR provider !~ '^[A-Za-z0-9][A-Za-z0-9._/-]{0,127}$' OR chain_ref IS NULL OR length(chain_ref)>128 THEN
    RAISE EXCEPTION 'RPC provider snapshot has no safe stable identity' USING ERRCODE='23514';
  END IF;
  valid_policy:=public.provider_operation_policy_payload_valid(s.payload->'provider_operations',ARRAY['health','head','range','transaction_lookup','transfer_verify']);
  INSERT INTO public.provider_operation_bindings(id,scope_id,provider_kind,provider_id,platform_snapshot_id,config_logical_key,chain_id,status,created_at,updated_at)
  VALUES(gen_random_uuid(),public.platform_scope_uuid(NULL),'on_chain',provider,NEW.snapshot_id,s.logical_key,chain_ref,
    CASE WHEN valid_policy THEN 'active' ELSE 'paused' END,
    NEW.updated_at,NEW.updated_at)
  ON CONFLICT(config_logical_key) WHERE provider_kind='on_chain'
  DO UPDATE SET provider_id=EXCLUDED.provider_id,platform_snapshot_id=EXCLUDED.platform_snapshot_id,chain_id=EXCLUDED.chain_id,status=CASE WHEN valid_policy THEN provider_operation_bindings.status ELSE 'paused' END,updated_at=EXCLUDED.updated_at,version=provider_operation_bindings.version+1
  RETURNING id INTO binding;
  PERFORM public.provider_operation_apply_rpc_policy(binding,NEW.snapshot_id,NEW.updated_at);
  RETURN NULL;
END $$;
REVOKE ALL ON FUNCTION provider_operation_sync_rpc_head() FROM PUBLIC;

COMMIT;
