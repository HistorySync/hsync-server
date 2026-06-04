-- migrations/002_auth_tokens.up.sql
-- Email verification and password reset token tables.
--
-- These back the CE EmailVerificationRepo and PasswordResetRepo so a standalone
-- CE deployment owns every table its repositories use. The DDL matches the
-- Enterprise definitions (which use CREATE TABLE IF NOT EXISTS), so applying the
-- CE and Enterprise migration sets together stays conflict-free.
-- Target: PostgreSQL 16+

BEGIN;

CREATE TABLE IF NOT EXISTS email_verifications (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash BYTEA NOT NULL UNIQUE,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS password_resets (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash BYTEA NOT NULL UNIQUE,
    expires_at TIMESTAMPTZ NOT NULL,
    used_at    TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

COMMIT;
