BEGIN;

DO $patch$
DECLARE
  definition text;
  patched text;
  old_fragment constant text := 'ORDER BY min(e.binding_id) LIMIT 1';
  new_fragment constant text := 'ORDER BY min(e.binding_id::text) LIMIT 1';
BEGIN
  SELECT pg_get_functiondef('public.claim_provider_health_probes(text,integer,timestamptz)'::regprocedure)
    INTO definition;
  IF position(new_fragment IN definition)=0 OR position(old_fragment IN definition)>0 THEN
    RAISE EXCEPTION 'claim_provider_health_probes has an unexpected group-order expression';
  END IF;
  patched := replace(definition,new_fragment,old_fragment);
  IF position(new_fragment IN patched)>0 OR position(old_fragment IN patched)=0 THEN
    RAISE EXCEPTION 'claim_provider_health_probes rollback was not exact';
  END IF;
  EXECUTE patched;
END $patch$;

COMMIT;
