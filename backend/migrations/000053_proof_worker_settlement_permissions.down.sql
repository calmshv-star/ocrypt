BEGIN;

DO $roles$
BEGIN
  IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='merchant_proof_worker') THEN
    REVOKE SELECT ON assets FROM merchant_proof_worker;
    REVOKE SELECT,INSERT,UPDATE,DELETE,TRUNCATE ON payment_observations FROM merchant_proof_worker;
    REVOKE SELECT,INSERT,UPDATE,DELETE,TRUNCATE ON payment_observation_events FROM merchant_proof_worker;
  END IF;
END $roles$;

COMMIT;
