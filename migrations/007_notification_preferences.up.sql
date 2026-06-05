BEGIN;

CREATE TABLE user_notification_preferences (
    user_id           UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    security_email    BOOLEAN NOT NULL DEFAULT true,
    security_webhook  BOOLEAN NOT NULL DEFAULT false,
    billing_email     BOOLEAN NOT NULL DEFAULT true,
    billing_webhook   BOOLEAN NOT NULL DEFAULT false,
    webhook_url       TEXT NOT NULL DEFAULT '',
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TRIGGER trg_user_notification_preferences_updated_at
    BEFORE UPDATE ON user_notification_preferences
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

COMMIT;
