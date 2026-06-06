-- Archive tables for operational history retained beyond the hot query window.

BEGIN;

CREATE TABLE IF NOT EXISTS audit_logs_archive (
    LIKE audit_logs INCLUDING DEFAULTS INCLUDING CONSTRAINTS,
    archived_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'audit_logs_archive_pkey'
    ) THEN
        ALTER TABLE audit_logs_archive
            ADD CONSTRAINT audit_logs_archive_pkey PRIMARY KEY (id);
    END IF;
END $$;
CREATE INDEX IF NOT EXISTS idx_audit_logs_archive_created_at
    ON audit_logs_archive(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_audit_logs_archive_event_created_at
    ON audit_logs_archive(event_type, created_at DESC);

CREATE TABLE IF NOT EXISTS ops_check_runs_archive (
    LIKE ops_check_runs INCLUDING DEFAULTS INCLUDING CONSTRAINTS,
    archived_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'ops_check_runs_archive_pkey'
    ) THEN
        ALTER TABLE ops_check_runs_archive
            ADD CONSTRAINT ops_check_runs_archive_pkey PRIMARY KEY (id);
    END IF;
END $$;
CREATE INDEX IF NOT EXISTS idx_ops_check_runs_archive_recent
    ON ops_check_runs_archive(run_type, started_at DESC);

CREATE TABLE IF NOT EXISTS notification_outbox_archive (
    LIKE notification_outbox INCLUDING DEFAULTS INCLUDING CONSTRAINTS,
    archived_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'notification_outbox_archive_pkey'
    ) THEN
        ALTER TABLE notification_outbox_archive
            ADD CONSTRAINT notification_outbox_archive_pkey PRIMARY KEY (id);
    END IF;
END $$;
CREATE INDEX IF NOT EXISTS idx_notification_outbox_archive_updated_at
    ON notification_outbox_archive(updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_notification_outbox_archive_type
    ON notification_outbox_archive(category, type, updated_at DESC);

COMMIT;
