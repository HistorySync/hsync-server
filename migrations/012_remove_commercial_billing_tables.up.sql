-- Remove commercial billing tables from CE installations that had already
-- applied the pre-split commercial billing migrations.

BEGIN;

DO $$
DECLARE
    has_enterprise_tables BOOLEAN;
BEGIN
    SELECT EXISTS (
        SELECT 1
        FROM information_schema.tables
        WHERE table_schema = current_schema()
          AND table_name = 'teams'
    ) INTO has_enterprise_tables;

    -- In CE-only databases, remove tables left by the old pre-split commercial
    -- migrations. Enterprise databases own similarly named tables; leave them
    -- to EE migrations and repositories.
    IF NOT has_enterprise_tables THEN
        DROP TABLE IF EXISTS payment_orders;
        DROP TABLE IF EXISTS ai_credit_ledger;
        DROP TABLE IF EXISTS user_subscriptions;
        DROP TABLE IF EXISTS user_entitlements;
        DROP TABLE IF EXISTS plan_prices;
        DROP TABLE IF EXISTS plans;
        DROP TABLE IF EXISTS invoices;
    END IF;
END $$;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM information_schema.tables
        WHERE table_schema = current_schema()
          AND table_name = 'stripe_customers'
    ) AND EXISTS (
        SELECT 1
        FROM information_schema.columns
        WHERE table_schema = current_schema()
          AND table_name = 'users'
          AND column_name = 'stripe_customer_id'
    ) THEN
        INSERT INTO stripe_customers (user_id, stripe_customer_id)
        SELECT id, stripe_customer_id
        FROM users
        WHERE stripe_customer_id IS NOT NULL AND stripe_customer_id <> ''
        ON CONFLICT (user_id) DO UPDATE SET
            stripe_customer_id = EXCLUDED.stripe_customer_id,
            updated_at = now();
    END IF;
END $$;

DO $$
DECLARE
    has_enterprise_tables BOOLEAN;
    has_stripe_customers BOOLEAN;
BEGIN
    SELECT EXISTS (
        SELECT 1
        FROM information_schema.tables
        WHERE table_schema = current_schema()
          AND table_name = 'teams'
    ) INTO has_enterprise_tables;

    SELECT EXISTS (
        SELECT 1
        FROM information_schema.tables
        WHERE table_schema = current_schema()
          AND table_name = 'stripe_customers'
    ) INTO has_stripe_customers;

    -- In Enterprise deployments, keep the legacy column until EE migration 014
    -- has a chance to copy it into stripe_customers. CE-only deployments drop it.
    IF NOT has_enterprise_tables OR has_stripe_customers THEN
        DROP INDEX IF EXISTS idx_users_stripe;
        ALTER TABLE users DROP COLUMN IF EXISTS stripe_customer_id;
    END IF;
END $$;

COMMIT;
