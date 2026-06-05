-- migrations/009_billing.up.sql
-- Paid plans, Cloud pricing, and the AI credit ledger.
--
-- This adds the billing/entitlement foundation: a code-driven plan catalog
-- (plans + plan_prices), the user's effective entitlement (user_entitlements),
-- cloud subscriptions (user_subscriptions), an append-only AI credit ledger
-- (ai_credit_ledger), and recorded payment orders (payment_orders).
--
-- Design notes:
--   * Money is stored in BIGINT minor units (USD cents / CNY fen) to avoid
--     floating-point rounding. Render with the currency on the client.
--   * Billing entitlements are intentionally SEPARATE from users.tier and the
--     quota system: users.tier (free/pro/team/enterprise) drives storage quota,
--     while user_entitlements.tier (free/pro/max) drives features, write-back,
--     cloud sync, and AI credits.
--   * The ledger is the source of truth for AI credits. A grant row carries a
--     mutable remaining_amount and an expires_at and acts as a consumable "lot";
--     consumption draws down lots oldest-expiry-first (FIFO). One-time credits
--     have expires_at = NULL and never expire; subscription period credits
--     expire at the period boundary. Non-grant rows have remaining_amount = NULL.
--   * Payment providers (gumroad/afdian/manual) are recorded as a source only;
--     business rules never branch on the provider. raw_metadata may hold a JSON
--     snapshot but must never contain payment secrets/tokens (the service strips
--     them) and is never returned over the API.

BEGIN;

-- ============================================================
-- 计划目录
-- ============================================================
CREATE TABLE plans (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    code        TEXT NOT NULL UNIQUE,
    name        TEXT NOT NULL DEFAULT '',
    kind        TEXT NOT NULL CHECK (kind IN ('lifetime','subscription','bundle')),
    enabled     BOOLEAN NOT NULL DEFAULT true,
    metadata    JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TRIGGER trg_plans_updated_at
    BEFORE UPDATE ON plans
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

-- ============================================================
-- 计划价格（按地区/币种/计费周期）
-- ============================================================
CREATE TABLE plan_prices (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    plan_code         TEXT NOT NULL REFERENCES plans(code) ON DELETE CASCADE,
    region            TEXT NOT NULL CHECK (region IN ('international','china')),
    currency          TEXT NOT NULL CHECK (currency IN ('USD','CNY')),
    amount            BIGINT NOT NULL,            -- minor units (cents / fen)
    billing_period    TEXT NOT NULL DEFAULT 'none'
                      CHECK (billing_period IN ('none','monthly','yearly')),
    early_bird_amount BIGINT,                     -- minor units; NULL when no early-bird price
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (plan_code, region, billing_period)
);
CREATE INDEX idx_plan_prices_plan ON plan_prices(plan_code);

CREATE TRIGGER trg_plan_prices_updated_at
    BEFORE UPDATE ON plan_prices
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

-- ============================================================
-- 用户权益（每用户一行的有效权益）
-- ============================================================
CREATE TABLE user_entitlements (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id            UUID NOT NULL UNIQUE REFERENCES users(id) ON DELETE CASCADE,
    tier               TEXT NOT NULL DEFAULT 'free' CHECK (tier IN ('free','pro','max')),
    cloud_sync_enabled BOOLEAN NOT NULL DEFAULT false,
    writeback_enabled  BOOLEAN NOT NULL DEFAULT false,
    source_plan_code   TEXT NOT NULL DEFAULT '',
    starts_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    ends_at            TIMESTAMPTZ,               -- NULL for lifetime entitlements
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TRIGGER trg_user_entitlements_updated_at
    BEFORE UPDATE ON user_entitlements
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

-- ============================================================
-- 用户订阅（Cloud 云同步）
-- ============================================================
CREATE TABLE user_subscriptions (
    id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id              UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    plan_code            TEXT NOT NULL,
    status               TEXT NOT NULL DEFAULT 'active'
                         CHECK (status IN ('active','expired','canceled')),
    current_period_start TIMESTAMPTZ NOT NULL DEFAULT now(),
    current_period_end   TIMESTAMPTZ NOT NULL,
    active_until         TIMESTAMPTZ NOT NULL,
    provider             TEXT NOT NULL DEFAULT 'manual'
                         CHECK (provider IN ('gumroad','afdian','manual')),
    external_order_id    TEXT NOT NULL DEFAULT '',
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_user_subscriptions_user_status ON user_subscriptions(user_id, status);
CREATE INDEX idx_user_subscriptions_active_until ON user_subscriptions(active_until)
    WHERE status = 'active';

CREATE TRIGGER trg_user_subscriptions_updated_at
    BEFORE UPDATE ON user_subscriptions
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

-- ============================================================
-- AI credit 账本（源真相 + 可消耗的发放批次）
-- ============================================================
CREATE TABLE ai_credit_ledger (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id          UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    source           TEXT NOT NULL CHECK (source IN (
                         'free_grant','pro_grant','max_grant','cloud_period_grant',
                         'manual_grant','consume','adjustment','expire')),
    amount           BIGINT NOT NULL,             -- positive for grant, negative for consume/expire
    balance_after    BIGINT NOT NULL DEFAULT 0,   -- live balance snapshot at write time
    remaining_amount BIGINT,                      -- grant lots only; NULL for consume/expire rows
    idempotency_key  TEXT UNIQUE,                 -- multiple NULLs allowed; set by the service
    expires_at       TIMESTAMPTZ,                 -- grant lots only; NULL = never expires
    metadata         JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (remaining_amount IS NULL OR remaining_amount >= 0)
);
CREATE INDEX idx_ai_credit_ledger_user_created ON ai_credit_ledger(user_id, created_at DESC, id DESC);
-- Live consumable lots: oldest-expiry-first scans hit this partial index.
CREATE INDEX idx_ai_credit_ledger_live_lots ON ai_credit_ledger(user_id, expires_at, created_at)
    WHERE remaining_amount > 0;

-- ============================================================
-- 支付订单（仅作来源记录，幂等去重）
-- ============================================================
CREATE TABLE payment_orders (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id           UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    provider          TEXT NOT NULL DEFAULT 'manual'
                      CHECK (provider IN ('gumroad','afdian','manual')),
    external_order_id TEXT NOT NULL DEFAULT '',
    plan_code         TEXT NOT NULL DEFAULT '',
    currency          TEXT NOT NULL DEFAULT '',
    amount            BIGINT NOT NULL DEFAULT 0,  -- minor units
    status            TEXT NOT NULL DEFAULT 'pending'
                      CHECK (status IN ('pending','paid','refunded','canceled')),
    raw_metadata      JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_payment_orders_user ON payment_orders(user_id, created_at DESC);
-- Dedupe real external orders (e.g. webhook replays) while still allowing
-- multiple manual orders that carry no external id.
CREATE UNIQUE INDEX uq_payment_orders_provider_order ON payment_orders(provider, external_order_id)
    WHERE external_order_id <> '';

CREATE TRIGGER trg_payment_orders_updated_at
    BEFORE UPDATE ON payment_orders
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

-- ============================================================
-- Seed: 计划目录（效果写入 plans.metadata）
-- 自托管开箱即用；金额为最小货币单位（USD 分 / CNY 分）。
-- ============================================================
INSERT INTO plans (code, name, kind, metadata) VALUES
    ('free',         'Free',           'lifetime',     '{"tier":"free","one_time_credits":50}'),
    ('pro',          'Pro',            'lifetime',     '{"tier":"pro","one_time_credits":200}'),
    ('max',          'Max',            'lifetime',     '{"tier":"max","one_time_credits":600,"writeback":true}'),
    ('cloud_lite',   'Cloud Lite',     'subscription', '{"period_credits":200,"cloud_sync":true}'),
    ('cloud',        'Cloud',          'subscription', '{"period_credits":500,"cloud_sync":true}'),
    ('max_cloud_1y', 'Max + 1y Cloud', 'bundle',       '{"components":[{"plan_code":"max"},{"plan_code":"cloud","cloud_months":12}]}'),
    ('max_cloud_2y', 'Max + 2y Cloud', 'bundle',       '{"components":[{"plan_code":"max"},{"plan_code":"cloud","cloud_months":24}]}');

INSERT INTO plan_prices (plan_code, region, currency, amount, billing_period, early_bird_amount) VALUES
    -- Free
    ('free',         'international', 'USD',     0, 'none',    NULL),
    ('free',         'china',         'CNY',     0, 'none',    NULL),
    -- Pro ($9.99 / ¥68)
    ('pro',          'international', 'USD',   999, 'none',    NULL),
    ('pro',          'china',         'CNY',  6800, 'none',    NULL),
    -- Max ($19.99 / ¥128)
    ('max',          'international', 'USD',  1999, 'none',    NULL),
    ('max',          'china',         'CNY', 12800, 'none',    NULL),
    -- Cloud Lite (monthly $1.99 / ¥5.9 ; yearly $19.99 / ¥59)
    ('cloud_lite',   'international', 'USD',   199, 'monthly', NULL),
    ('cloud_lite',   'china',         'CNY',   590, 'monthly', NULL),
    ('cloud_lite',   'international', 'USD',  1999, 'yearly',  NULL),
    ('cloud_lite',   'china',         'CNY',  5900, 'yearly',  NULL),
    -- Cloud (monthly $2.99 / ¥9.9 ; yearly $24.99 / ¥88)
    ('cloud',        'international', 'USD',   299, 'monthly', NULL),
    ('cloud',        'china',         'CNY',   990, 'monthly', NULL),
    ('cloud',        'international', 'USD',  2499, 'yearly',  NULL),
    ('cloud',        'china',         'CNY',  8800, 'yearly',  NULL),
    -- Max + 1y Cloud ($34.99 / ¥188 ; early-bird $29.99 / ¥158)
    ('max_cloud_1y', 'international', 'USD',  3499, 'none',  2999),
    ('max_cloud_1y', 'china',         'CNY', 18800, 'none', 15800),
    -- Max + 2y Cloud ($49.99 / ¥258 ; early-bird $39.99 / ¥198)
    ('max_cloud_2y', 'international', 'USD',  4999, 'none',  3999),
    ('max_cloud_2y', 'china',         'CNY', 25800, 'none', 19800);

COMMIT;
