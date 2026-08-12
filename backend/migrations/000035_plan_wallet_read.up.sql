DO $$ BEGIN
  IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='merchant_plan_worker') THEN
    GRANT SELECT ON wallets TO merchant_plan_worker;
  END IF;
END $$;
