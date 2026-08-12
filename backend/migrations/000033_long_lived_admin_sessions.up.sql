DO $$
DECLARE
    constraint_name text;
BEGIN
    SELECT c.conname
      INTO constraint_name
      FROM pg_constraint c
     WHERE c.conrelid = 'admin_sessions'::regclass
       AND c.contype = 'c'
       AND pg_get_constraintdef(c.oid) LIKE '%absolute_expires_at%12:00:00%';

    IF constraint_name IS NOT NULL THEN
        EXECUTE format('ALTER TABLE admin_sessions DROP CONSTRAINT %I', constraint_name);
    END IF;
END
$$;

ALTER TABLE admin_sessions
    ADD CONSTRAINT admin_sessions_absolute_lifetime_check
    CHECK (absolute_expires_at > created_at AND absolute_expires_at <= created_at + interval '10 years');
