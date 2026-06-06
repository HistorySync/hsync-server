-- Enforce case-insensitive uniqueness for user emails at the database layer.

BEGIN;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM users
        GROUP BY lower(trim(email))
        HAVING COUNT(*) > 1
    ) THEN
        RAISE EXCEPTION 'cannot enforce case-insensitive user email uniqueness while duplicate emails exist';
    END IF;
END $$;

UPDATE users
SET email = lower(trim(email))
WHERE email <> lower(trim(email));

DROP INDEX IF EXISTS idx_users_email;
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_email_key;

CREATE UNIQUE INDEX IF NOT EXISTS idx_users_email_lower_unique
    ON users (lower(trim(email)));
CREATE INDEX IF NOT EXISTS idx_users_email_lower_active
    ON users (lower(trim(email)))
    WHERE deleted_at IS NULL;

COMMIT;
