BEGIN;

DO $patch$
DECLARE
  definition text;
  patched text;
  old_select constant text := 'SELECT configured.*,c.updated_at AS last_scheduled_at';
  new_select constant text := 'SELECT configured.*';
  old_order constant text := 'ORDER BY min(e.last_scheduled_at),min(e.binding_id::text) LIMIT 1';
  new_order constant text := 'ORDER BY min(e.binding_id::text) LIMIT 1';
  old_onchain constant text := '((b.provider_kind=''on_chain'' AND b.chain_id IS NOT NULL AND public.provider_operation_binding_policy_current(b.id)) AND EXISTS (SELECT 1 FROM public.platform_config_heads chain_head JOIN public.platform_config_snapshots chain_snapshot ON chain_snapshot.id=chain_head.snapshot_id AND chain_snapshot.scope_id=chain_head.scope_id WHERE chain_head.scope_id=public.platform_scope_uuid(NULL) AND chain_head.kind=''chain'' AND chain_head.logical_key=b.chain_id AND chain_snapshot.payload->>''status''=''active''))';
  new_onchain constant text := '(b.provider_kind=''on_chain'' AND b.chain_id IS NOT NULL AND public.provider_operation_binding_policy_current(b.id))';
BEGIN
  SELECT pg_get_functiondef('public.claim_provider_health_probes(text,integer,timestamptz)'::regprocedure)
    INTO definition;
  IF position(old_select IN definition)=0
     OR position(old_order IN definition)=0 OR position(old_onchain IN definition)=0 THEN
    RAISE EXCEPTION 'claim_provider_health_probes has an unexpected fairness selector';
  END IF;
  patched := replace(definition,old_select,new_select);
  patched := replace(patched,old_order,new_order);
  patched := replace(patched,old_onchain,new_onchain);
  IF position(old_select IN patched)>0 OR position(new_select IN patched)=0
     OR position(old_order IN patched)>0 OR position(new_order IN patched)=0
     OR position(old_onchain IN patched)>0 OR position(new_onchain IN patched)=0 THEN
    RAISE EXCEPTION 'claim_provider_health_probes fairness rollback was not exact';
  END IF;
  EXECUTE patched;
END $patch$;

COMMIT;
