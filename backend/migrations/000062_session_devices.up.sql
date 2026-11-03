BEGIN;

-- A coarse "browser/system" label for the user's own session list, derived
-- from the User-Agent at sign-in. The raw User-Agent is never stored.
ALTER TABLE admin_sessions
  ADD COLUMN device_label text CHECK (device_label IS NULL OR device_label ~ '^[A-Za-z]{1,16}/[A-Za-z]{0,16}$');

COMMIT;
