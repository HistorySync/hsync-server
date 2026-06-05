-- migrations/009_billing.down.sql
-- Remove paid plans, Cloud pricing, and the AI credit ledger.

BEGIN;

DROP TABLE IF EXISTS payment_orders;
DROP TABLE IF EXISTS ai_credit_ledger;
DROP TABLE IF EXISTS user_subscriptions;
DROP TABLE IF EXISTS user_entitlements;
DROP TABLE IF EXISTS plan_prices;
DROP TABLE IF EXISTS plans;

COMMIT;
