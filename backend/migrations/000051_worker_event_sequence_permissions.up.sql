BEGIN;

-- Migration 000008 originally created the allocator before proof and plan
-- workers became callback producers. Keep the immutable historical migration
-- byte-for-byte intact and grant the later roles in this forward migration.
DO $roles$
BEGIN
  IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='merchant_proof_worker') THEN
    GRANT SELECT,INSERT,UPDATE ON merchant_event_sequences TO merchant_proof_worker;
    REVOKE DELETE,TRUNCATE ON merchant_event_sequences FROM merchant_proof_worker;
  END IF;
  IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='merchant_plan_worker') THEN
    GRANT SELECT,INSERT,UPDATE ON merchant_event_sequences TO merchant_plan_worker;
    REVOKE DELETE,TRUNCATE ON merchant_event_sequences FROM merchant_plan_worker;
  END IF;
END $roles$;

COMMIT;
