BEGIN;

ALTER TABLE user_notification_preferences
    ADD COLUMN webhook_secret TEXT NOT NULL DEFAULT '';

CREATE TABLE notification_outbox (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id        UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    channel        TEXT NOT NULL CHECK (channel IN ('email','webhook')),
    category       TEXT NOT NULL,
    type           TEXT NOT NULL,
    payload_json   JSONB NOT NULL DEFAULT '{}'::jsonb,
    status         TEXT NOT NULL DEFAULT 'pending'
                   CHECK (status IN ('pending','processing','sent','failed')),
    attempt_count  INTEGER NOT NULL DEFAULT 0,
    next_retry_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_error     TEXT NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    sent_at        TIMESTAMPTZ
);

CREATE INDEX idx_notification_outbox_due
    ON notification_outbox (next_retry_at, created_at)
    WHERE status = 'pending';

CREATE INDEX idx_notification_outbox_failures
    ON notification_outbox (updated_at DESC)
    WHERE status = 'failed';

CREATE TRIGGER trg_notification_outbox_updated_at
    BEFORE UPDATE ON notification_outbox
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

COMMIT;
