BEGIN;

DROP TRIGGER IF EXISTS merchant_go_live_addresses_screen ON merchant_go_live_addresses;
DROP TRIGGER IF EXISTS transfer_events_screen_sender ON transfer_events;
DROP FUNCTION IF EXISTS list_sanctioned_transfer_alerts(uuid,uuid,integer);
DROP FUNCTION IF EXISTS refuse_sanctioned_go_live_address();
DROP FUNCTION IF EXISTS screen_transfer_sender();
DROP FUNCTION IF EXISTS enqueue_sanctioned_transfer_notification(uuid,uuid,uuid,jsonb);
DROP FUNCTION IF EXISTS sanctioned_address_matches(text,text);
DROP FUNCTION IF EXISTS replace_sanctioned_addresses(text,jsonb);
DELETE FROM notification_jobs WHERE event='sanctioned';
ALTER TABLE notification_jobs DROP CONSTRAINT notification_jobs_event_check;
ALTER TABLE notification_jobs ADD CONSTRAINT notification_jobs_event_check
  CHECK (event IN ('paid','underpaid','review','webhook_failed','sign_in'));
DROP TABLE IF EXISTS sanctioned_transfer_alerts;
DROP TABLE IF EXISTS sanctioned_addresses;

COMMIT;
