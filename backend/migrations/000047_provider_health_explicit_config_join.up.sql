BEGIN;

-- The final provider-health projection joins claimed circuits and policies
-- before configured evidence. USING(binding_id,operation) is ambiguous there
-- because the left relation exposes the same names more than once. Bind the
-- evidence explicitly to the claimed circuit instead.
DO $patch$
DECLARE
  definition text;
  patched text;
  old_fragment constant text := 'JOIN configured USING(binding_id,operation);';
  new_fragment constant text := 'JOIN configured ON configured.binding_id=c.binding_id AND configured.operation=c.operation;';
BEGIN
  SELECT pg_get_functiondef('public.claim_provider_health_probes(text,integer,timestamptz)'::regprocedure)
    INTO definition;
  IF position(old_fragment IN definition)=0 OR position(new_fragment IN definition)>0 THEN
    RAISE EXCEPTION 'claim_provider_health_probes has an unexpected configured-evidence join';
  END IF;
  patched := replace(definition,old_fragment,new_fragment);
  IF position(old_fragment IN patched)>0 OR position(new_fragment IN patched)=0 THEN
    RAISE EXCEPTION 'claim_provider_health_probes configured-evidence patch was not exact';
  END IF;
  EXECUTE patched;
END $patch$;

COMMIT;
