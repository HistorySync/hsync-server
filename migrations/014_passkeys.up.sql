-- Passkey/WebAuthn credentials and short-lived challenge sessions.

BEGIN;

CREATE TABLE passkey_credentials (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id          UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name             TEXT NOT NULL DEFAULT '',
    credential_id    BYTEA NOT NULL UNIQUE,
    public_key       BYTEA NOT NULL,
    attestation_type TEXT NOT NULL DEFAULT '',
    aaguid           BYTEA NOT NULL DEFAULT ''::bytea,
    sign_count       BIGINT NOT NULL DEFAULT 0 CHECK (sign_count >= 0),
    clone_warning    BOOLEAN NOT NULL DEFAULT false,
    user_present     BOOLEAN NOT NULL DEFAULT false,
    user_verified    BOOLEAN NOT NULL DEFAULT false,
    backup_eligible  BOOLEAN NOT NULL DEFAULT false,
    backup_state     BOOLEAN NOT NULL DEFAULT false,
    transports       JSONB NOT NULL DEFAULT '[]'::jsonb,
    attachment       TEXT NOT NULL DEFAULT '',
    last_used_at     TIMESTAMPTZ,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_passkey_credentials_user
    ON passkey_credentials(user_id, created_at DESC);

CREATE TRIGGER trg_passkey_credentials_updated_at
    BEFORE UPDATE ON passkey_credentials
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

CREATE TABLE passkey_challenges (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      UUID REFERENCES users(id) ON DELETE CASCADE,
    type         TEXT NOT NULL CHECK (type IN ('registration','login','step_up')),
    challenge    TEXT NOT NULL,
    session_json JSONB NOT NULL,
    expires_at   TIMESTAMPTZ NOT NULL,
    consumed_at  TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (type, challenge)
);

CREATE INDEX idx_passkey_challenges_expires
    ON passkey_challenges(expires_at);

CREATE INDEX idx_passkey_challenges_user_type
    ON passkey_challenges(user_id, type, created_at DESC)
    WHERE consumed_at IS NULL;

COMMIT;
