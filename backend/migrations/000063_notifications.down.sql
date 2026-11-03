BEGIN;

DROP TRIGGER IF EXISTS callback_deliveries_notify_dead_letter ON callback_deliveries;
DROP TRIGGER IF EXISTS callback_events_notify ON callback_events;
DROP FUNCTION IF EXISTS fail_notification_job(uuid,uuid,text,integer,integer);
DROP FUNCTION IF EXISTS complete_notification_job(uuid,uuid,text);
DROP FUNCTION IF EXISTS claim_notification_job(uuid,integer);
DROP FUNCTION IF EXISTS advance_telegram_update_offset(bigint);
DROP FUNCTION IF EXISTS telegram_update_offset();
DROP FUNCTION IF EXISTS link_telegram_chat(bytea,bigint);
DROP FUNCTION IF EXISTS unlink_current_admin_telegram();
DROP FUNCTION IF EXISTS create_current_admin_telegram_link_code(bytea,timestamptz,timestamptz);
DROP FUNCTION IF EXISTS set_current_admin_account_notifications(boolean,boolean,text,timestamptz);
DROP FUNCTION IF EXISTS set_current_admin_notification_preferences(uuid,uuid,text[],text[],text,timestamptz);
DROP FUNCTION IF EXISTS current_admin_notification_settings(uuid,uuid);
DROP FUNCTION IF EXISTS enqueue_current_admin_sign_in_notification(uuid,text);
DROP FUNCTION IF EXISTS notify_on_callback_dead_letter();
DROP FUNCTION IF EXISTS notify_on_callback_event();
DROP FUNCTION IF EXISTS enqueue_merchant_notification(uuid,uuid,text,text,text,jsonb);
DROP FUNCTION IF EXISTS notification_recipient_allowed(uuid,uuid,uuid,text);
DROP FUNCTION IF EXISTS notification_default_owners(uuid,uuid);
DROP TABLE IF EXISTS notification_jobs;
DROP TABLE IF EXISTS telegram_update_state;
DROP TABLE IF EXISTS telegram_link_codes;
DROP TABLE IF EXISTS telegram_chat_links;
DROP TABLE IF EXISTS notification_preferences;
DROP TABLE IF EXISTS notification_account_settings;

COMMIT;
