BEGIN;

ALTER TABLE devices
    ADD COLUMN IF NOT EXISTS token_expires_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_devices_token_hash_active
    ON devices(token_hash)
    WHERE token_hash IS NOT NULL AND revoked_at IS NULL;

COMMIT;
