-- Revert payment order lifecycle columns added for webhook fulfillment.

BEGIN;

DROP INDEX IF EXISTS uq_user_subscriptions_provider_order_plan;
DROP INDEX IF EXISTS idx_payment_orders_status_created;

ALTER TABLE payment_orders
    DROP CONSTRAINT IF EXISTS payment_orders_status_check;

UPDATE payment_orders
SET status = 'paid'
WHERE status IN ('completed','failed','expired');

ALTER TABLE payment_orders
    ADD CONSTRAINT payment_orders_status_check
    CHECK (status IN ('pending','paid','refunded','canceled'));

ALTER TABLE payment_orders
    DROP COLUMN IF EXISTS paid_at,
    DROP COLUMN IF EXISTS completed_at,
    DROP COLUMN IF EXISTS failed_at,
    DROP COLUMN IF EXISTS failed_reason;

COMMIT;
