\set ON_ERROR_STOP on

-- Run once as the database security administrator. The UUID must be copied to
-- the publisher's PLATFORM_OUTBOX_WORKER_ID Secret and never shared by another
-- live replica. A duplicate is an operator-visible error, not an implicit
-- re-enable of an identity disabled during incident response.
BEGIN;
INSERT INTO platform_admin_service_identities(id,name,purpose,enabled,created_at)
VALUES(
  :'platform_outbox_service_identity_id'::uuid,
  :'platform_outbox_service_identity_name',
  'outbox_publisher',
  true,
  clock_timestamp()
);
COMMIT;
