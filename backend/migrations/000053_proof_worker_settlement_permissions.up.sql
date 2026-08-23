BEGIN;

-- A proof worker can discover a finalized transfer before the sequential
-- scanner. It feeds that canonical event through the same observation and
-- settlement transaction, so its capability role needs the observation slice
-- and the asset dust policy read that the settlement worker already owns.
DO $roles$
BEGIN
  IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='merchant_proof_worker') THEN
    GRANT SELECT ON assets TO merchant_proof_worker;
    GRANT SELECT,INSERT,UPDATE ON payment_observations TO merchant_proof_worker;
    GRANT SELECT,INSERT ON payment_observation_events TO merchant_proof_worker;
    REVOKE DELETE,TRUNCATE ON payment_observations,payment_observation_events FROM merchant_proof_worker;
  END IF;
END $roles$;

COMMIT;
