BEGIN;

REVOKE ALL ON FUNCTION request_rate_refresh_if_stale(text,text) FROM PUBLIC;
DROP FUNCTION request_rate_refresh_if_stale(text,text);

COMMIT;
