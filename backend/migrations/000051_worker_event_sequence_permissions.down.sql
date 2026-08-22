BEGIN;

DO $roles$
BEGIN
  IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='merchant_proof_worker') THEN
    REVOKE SELECT,INSERT,UPDATE,DELETE,TRUNCATE ON merchant_event_sequences FROM merchant_proof_worker;
  END IF;
  IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='merchant_plan_worker') THEN
    REVOKE SELECT,INSERT,UPDATE,DELETE,TRUNCATE ON merchant_event_sequences FROM merchant_plan_worker;
  END IF;
END $roles$;

COMMIT;
