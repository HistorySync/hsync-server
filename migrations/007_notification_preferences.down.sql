BEGIN;

DROP TRIGGER IF EXISTS trg_user_notification_preferences_updated_at ON user_notification_preferences;
DROP TABLE IF EXISTS user_notification_preferences;

COMMIT;
