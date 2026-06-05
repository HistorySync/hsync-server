BEGIN;

UPDATE notification_outbox
SET status = 'failed',
    updated_at = now()
WHERE status = 'discarded';

ALTER TABLE notification_outbox
    DROP CONSTRAINT IF EXISTS notification_outbox_status_check;

ALTER TABLE notification_outbox
    ADD CONSTRAINT notification_outbox_status_check
    CHECK (status IN ('pending','processing','sent','failed'));

COMMIT;
