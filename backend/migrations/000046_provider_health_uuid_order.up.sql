BEGIN;

-- PostgreSQL does not define min(uuid). The provider-health group selector
-- only needs a stable ordering key, so order by the canonical UUID text while
-- preserving the rest of the security-definer function byte-for-byte.
DO $patch$
DECLARE
  definition text;
  patched text;
  old_fragment constant text := 'ORDER BY min(e.binding_id) LIMIT 1';
  new_fragment constant text := 'ORDER BY min(e.binding_id::text) LIMIT 1';
BEGIN
  SELECT pg_get_functiondef('public.claim_provider_health_probes(text,integer,timestamptz)'::regprocedure)
    INTO definition;
  IF position(old_fragment IN definition)=0 OR position(new_fragment IN definition)>0 THEN
    RAISE EXCEPTION 'claim_provider_health_probes has an unexpected group-order expression';
  END IF;
  patched := replace(definition,old_fragment,new_fragment);
  IF position(old_fragment IN patched)>0 OR position(new_fragment IN patched)=0 THEN
    RAISE EXCEPTION 'claim_provider_health_probes group-order patch was not exact';
  END IF;
  EXECUTE patched;
END $patch$;

COMMIT;
