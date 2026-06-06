BEGIN;

DROP INDEX IF EXISTS idx_users_email_lower_active;
DROP INDEX IF EXISTS idx_users_email_lower_unique;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'users_email_key'
          AND conrelid = 'users'::regclass
    ) THEN
        ALTER TABLE users ADD CONSTRAINT users_email_key UNIQUE (email);
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS idx_users_email
    ON users(email)
    WHERE deleted_at IS NULL;

COMMIT;
