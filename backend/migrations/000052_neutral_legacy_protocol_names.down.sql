BEGIN;

DO $guard$
BEGIN
  IF EXISTS(SELECT 1 FROM legacy_compat_admission_requests WHERE status='pending') THEN
    RAISE EXCEPTION 'pending legacy admission must be decided or expired before protocol rollback';
  END IF;
END $guard$;

CREATE TEMP TABLE legacy_protocol_function_rewrites (
  signature text PRIMARY KEY,
  definition text NOT NULL
) ON COMMIT DROP;

INSERT INTO legacy_protocol_function_rewrites(signature,definition)
SELECT signature,pg_get_functiondef(signature::regprocedure)
FROM unnest(ARRAY[
  'public.legacy_admission_request_guard()',
  'public.request_legacy_compat_config_admission(uuid,uuid,uuid,uuid,uuid,text,text,text,smallint,text,text,text,text,text,cidr[],text,text,text,text,timestamptz,timestamptz,timestamptz,jsonb,bytea)',
  'public.request_legacy_compat_config_admission(uuid,jsonb)',
  'public.approve_legacy_compat_config_admission(uuid,bytea)',
  'public.legacy_lookup_credential(text,text,timestamptz)',
  'public.legacy_lookup_credential_version(uuid)',
  'public.legacy_record_mapping(text,uuid,uuid,text,text,bytea,uuid,uuid,text,text,text,text,text,text,text,text,timestamptz)',
  'public.legacy_enqueue_callback(uuid,uuid,bigint,uuid,text,uuid,text,text,text,text,bytea,timestamptz)',
  'public.create_migration_run(uuid,text,text,text,uuid,text,timestamptz,text,bytea)'
]) AS signatures(signature);

DROP FUNCTION legacy_lookup_credential(text,text,timestamptz);
DROP FUNCTION legacy_lookup_credential_version(uuid);

ALTER TABLE legacy_compat_configs
  DROP CONSTRAINT legacy_compat_configs_protocol_check,
  DROP CONSTRAINT legacy_compat_configs_check2;
ALTER TABLE legacy_compat_mappings
  DROP CONSTRAINT legacy_compat_mappings_protocol_check;
ALTER TABLE migration_runs
  DROP CONSTRAINT migration_runs_profile_check;

UPDATE legacy_compat_configs
SET protocol=CASE protocol WHEN 'json_md5' THEN 'gmpay' WHEN 'form_md5' THEN 'epay' ELSE protocol END,
    legacy_payment_type=CASE legacy_payment_type WHEN 'json_md5' THEN 'gmpay' ELSE legacy_payment_type END;

ALTER TABLE legacy_compat_mappings DISABLE TRIGGER legacy_mapping_immutable;
UPDATE legacy_compat_mappings
SET protocol=CASE protocol WHEN 'json_md5' THEN 'gmpay' WHEN 'form_md5' THEN 'epay' ELSE protocol END;
ALTER TABLE legacy_compat_mappings ENABLE TRIGGER legacy_mapping_immutable;

UPDATE migration_runs
SET profile=CASE profile
  WHEN 'wallet_ledger' THEN 'epusdt'
  WHEN 'json_md5' THEN 'gmpay'
  WHEN 'form_md5' THEN 'epay'
  ELSE profile
END;

ALTER TABLE legacy_compat_configs
  RENAME COLUMN legacy_payment_type TO legacy_epay_type;
ALTER TABLE legacy_compat_configs
  RENAME CONSTRAINT legacy_compat_configs_legacy_payment_type_check
  TO legacy_compat_configs_legacy_epay_type_check;
ALTER TABLE legacy_compat_admission_requests
  RENAME COLUMN legacy_payment_type TO legacy_epay_type;
ALTER TABLE legacy_compat_mappings
  RENAME COLUMN form_md5_type TO epay_type;
ALTER TABLE legacy_compat_mappings
  RENAME CONSTRAINT legacy_compat_mappings_form_md5_type_check
  TO legacy_compat_mappings_epay_type_check;

ALTER TABLE legacy_compat_configs
  ADD CONSTRAINT legacy_compat_configs_protocol_check CHECK(protocol IN ('gmpay','epay')),
  ADD CONSTRAINT legacy_compat_configs_check2 CHECK(protocol='epay' OR legacy_epay_type='gmpay');
ALTER TABLE legacy_compat_mappings
  ADD CONSTRAINT legacy_compat_mappings_protocol_check CHECK(protocol IN ('gmpay','epay'));
ALTER TABLE migration_runs
  ADD CONSTRAINT migration_runs_profile_check CHECK(profile IN ('generic','epusdt','gmpay','epay'));

DO $rewrite$
DECLARE item record; patched text;
BEGIN
  FOR item IN SELECT * FROM legacy_protocol_function_rewrites ORDER BY signature LOOP
    patched:=item.definition;
    patched:=replace(patched,'legacy_payment_type','legacy_epay_type');
    patched:=replace(patched,'form_md5_type','epay_type');
    patched:=replace(patched,'''json_md5''','''gmpay''');
    patched:=replace(patched,'''form_md5''','''epay''');
    patched:=replace(patched,'''wallet_ledger''','''epusdt''');
    EXECUTE patched;
  END LOOP;
END $rewrite$;

DO $verify$
BEGIN
  IF EXISTS(
    SELECT 1 FROM information_schema.columns
    WHERE table_schema='public'
      AND ((table_name IN ('legacy_compat_configs','legacy_compat_admission_requests') AND column_name='legacy_payment_type')
        OR (table_name='legacy_compat_mappings' AND column_name='form_md5_type'))
  ) OR EXISTS(SELECT 1 FROM legacy_compat_configs WHERE protocol IN ('json_md5','form_md5'))
    OR EXISTS(SELECT 1 FROM legacy_compat_mappings WHERE protocol IN ('json_md5','form_md5'))
    OR EXISTS(SELECT 1 FROM migration_runs WHERE profile IN ('wallet_ledger','json_md5','form_md5'))
    OR pg_get_function_result('legacy_lookup_credential(text,text,timestamptz)'::regprocedure) NOT LIKE '%legacy_epay_type text%'
    OR (SELECT tgenabled<>'O' FROM pg_trigger WHERE tgrelid='legacy_compat_mappings'::regclass AND tgname='legacy_mapping_immutable')
  THEN
    RAISE EXCEPTION 'legacy protocol rollback postcondition failed';
  END IF;
END $verify$;

COMMIT;
