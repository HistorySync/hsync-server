BEGIN;

DROP INDEX IF EXISTS idx_devices_token_hash_active;

ALTER TABLE devices
    DROP COLUMN IF EXISTS token_expires_at;

COMMIT;
