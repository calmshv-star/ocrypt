BEGIN;

ALTER TABLE admin_sessions DROP COLUMN IF EXISTS device_label;

COMMIT;
