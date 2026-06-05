-- migrations/005_two_factor.up.sql
-- Two-factor authentication state and backup codes.

BEGIN;

CREATE TABLE user_two_factor (
    user_id          UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    secret_encrypted BYTEA NOT NULL,
    enabled          BOOLEAN NOT NULL DEFAULT false,
    failed_attempts  INT NOT NULL DEFAULT 0,
    locked_until     TIMESTAMPTZ,
    last_used_at     TIMESTAMPTZ,
    enabled_at       TIMESTAMPTZ,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TRIGGER trg_user_two_factor_updated_at
    BEFORE UPDATE ON user_two_factor
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

CREATE TABLE user_two_factor_backup_codes (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    code_hash   TEXT NOT NULL,
    used_at     TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_user_two_factor_backup_codes_user_unused
    ON user_two_factor_backup_codes(user_id)
    WHERE used_at IS NULL;

COMMIT;
