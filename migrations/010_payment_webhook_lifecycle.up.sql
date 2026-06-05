-- Extend payment orders for verified webhook fulfillment lifecycle.

BEGIN;

ALTER TABLE payment_orders
    DROP CONSTRAINT IF EXISTS payment_orders_status_check;

ALTER TABLE payment_orders
    ADD CONSTRAINT payment_orders_status_check
    CHECK (status IN ('pending','paid','completed','failed','canceled','expired','refunded'));

ALTER TABLE payment_orders
    ADD COLUMN IF NOT EXISTS paid_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS completed_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS failed_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS failed_reason TEXT NOT NULL DEFAULT '';

UPDATE payment_orders
SET paid_at = COALESCE(paid_at, created_at)
WHERE status IN ('paid','completed','failed') AND paid_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_payment_orders_status_created
    ON payment_orders(status, created_at DESC);

CREATE UNIQUE INDEX IF NOT EXISTS uq_user_subscriptions_provider_order_plan
    ON user_subscriptions(provider, external_order_id, plan_code)
    WHERE external_order_id <> '';

COMMIT;
