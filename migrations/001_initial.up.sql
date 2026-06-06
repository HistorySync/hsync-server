-- migrations/001_initial.up.sql
-- Initial schema for HistorySync Cloud Server.
-- Target: PostgreSQL 16+

BEGIN;

-- ============================================================
-- Extension: pgcrypto for gen_random_uuid()
-- ============================================================
CREATE EXTENSION IF NOT EXISTS pgcrypto;

-- ============================================================
-- 用户表
-- ============================================================
CREATE TABLE IF NOT EXISTS users (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email             TEXT NOT NULL UNIQUE,
    password_hash     TEXT NOT NULL,
    display_name      TEXT NOT NULL DEFAULT '',
    tier              TEXT NOT NULL DEFAULT 'free'
                      CHECK (tier IN ('free','pro','team','enterprise')),
    status            TEXT NOT NULL DEFAULT 'active'
                      CHECK (status IN ('active','suspended','deleted')),
    email_verified    BOOLEAN NOT NULL DEFAULT false,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at        TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_users_email ON users(email) WHERE deleted_at IS NULL;

-- ============================================================
-- 刷新令牌表
-- ============================================================
CREATE TABLE IF NOT EXISTS refresh_tokens (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash   BYTEA NOT NULL UNIQUE,
    device_info  TEXT NOT NULL DEFAULT '',
    expires_at   TIMESTAMPTZ NOT NULL,
    revoked_at   TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_refresh_tokens_user ON refresh_tokens(user_id);

-- ============================================================
-- 设备表
-- ============================================================
CREATE TABLE IF NOT EXISTS devices (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id         UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    device_uuid     UUID NOT NULL,
    device_name     TEXT NOT NULL DEFAULT '',
    platform        TEXT NOT NULL DEFAULT '',
    app_version     TEXT NOT NULL DEFAULT '',
    token_hash      BYTEA,
    last_sync_at    TIMESTAMPTZ,
    revoked_at      TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(user_id, device_uuid)
);
CREATE INDEX IF NOT EXISTS idx_devices_user ON devices(user_id);

-- ============================================================
-- Bundle 元数据表
-- ============================================================
CREATE TABLE IF NOT EXISTS bundles (
    bundle_id            TEXT NOT NULL,
    user_id              UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    uploader_device_uuid UUID NOT NULL,
    lamport_lo           BIGINT NOT NULL,
    lamport_hi           BIGINT NOT NULL,
    event_count          INTEGER NOT NULL,
    size_bytes           BIGINT NOT NULL,
    cipher_id            SMALLINT NOT NULL DEFAULT 0,
    key_generation       SMALLINT NOT NULL DEFAULT 0,
    uploaded_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at           TIMESTAMPTZ,
    PRIMARY KEY (user_id, bundle_id)
);
CREATE INDEX IF NOT EXISTS idx_bundles_device_lamport ON bundles(user_id, uploader_device_uuid, lamport_lo)
    WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_bundles_uploaded ON bundles(uploaded_at) WHERE deleted_at IS NULL;

-- ============================================================
-- Snapshot 元数据表
-- ============================================================
CREATE TABLE IF NOT EXISTS snapshots (
    snapshot_id    TEXT NOT NULL,
    user_id        UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    base_hlc       BIGINT NOT NULL,
    size_bytes     BIGINT NOT NULL,
    cipher_id      SMALLINT NOT NULL DEFAULT 1,
    key_generation SMALLINT NOT NULL DEFAULT 0,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at     TIMESTAMPTZ,
    PRIMARY KEY (user_id, snapshot_id)
);
CREATE INDEX IF NOT EXISTS idx_snapshots_latest ON snapshots(user_id, created_at DESC);

-- ============================================================
-- 设备吊销事件表
-- ============================================================
CREATE TABLE IF NOT EXISTS device_revocations (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id       UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    device_uuid   UUID NOT NULL,
    revoked_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_by    UUID NOT NULL REFERENCES users(id)
);
CREATE INDEX IF NOT EXISTS idx_revocations_user ON device_revocations(user_id);

-- ============================================================
-- 存储使用量缓存表
-- ============================================================
CREATE TABLE IF NOT EXISTS storage_usage (
    user_id       UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    total_bytes   BIGINT NOT NULL DEFAULT 0,
    bundle_count  INTEGER NOT NULL DEFAULT 0,
    snap_count    INTEGER NOT NULL DEFAULT 0,
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ============================================================
-- 配额限制表
-- ============================================================
CREATE TABLE IF NOT EXISTS quota_limits (
    user_id              UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    storage_limit_bytes  BIGINT NOT NULL DEFAULT 1073741824,
    max_devices          INTEGER NOT NULL DEFAULT 1,
    max_bundle_size      BIGINT NOT NULL DEFAULT 52428800,
    max_snapshots        INTEGER NOT NULL DEFAULT 1,
    max_rpm              INTEGER NOT NULL DEFAULT 100,
    bundle_retention_days INTEGER NOT NULL DEFAULT 30,
    override_reason      TEXT,
    expires_at           TIMESTAMPTZ
);

-- ============================================================
-- 发票表
-- ============================================================
-- ============================================================
-- Trigger: 自动更新 users.updated_at
-- ============================================================
CREATE OR REPLACE FUNCTION update_updated_at_column()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_users_updated_at ON users;
CREATE TRIGGER trg_users_updated_at
    BEFORE UPDATE ON users
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

COMMIT;
