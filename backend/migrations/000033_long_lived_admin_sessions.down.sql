ALTER TABLE admin_sessions
    DROP CONSTRAINT IF EXISTS admin_sessions_absolute_lifetime_check;

ALTER TABLE admin_sessions
    ADD CONSTRAINT admin_sessions_absolute_lifetime_check
    CHECK (absolute_expires_at > created_at AND absolute_expires_at <= created_at + interval '12 hours');
