BEGIN;

DROP TRIGGER IF EXISTS trg_notification_outbox_updated_at ON notification_outbox;
DROP TABLE IF EXISTS notification_outbox;

ALTER TABLE user_notification_preferences
    DROP COLUMN IF EXISTS webhook_secret;

COMMIT;
