BEGIN;

DROP FUNCTION IF EXISTS rate_runtime_snapshot_current(uuid,platform_config_kind,text,uuid,bigint);

COMMIT;
