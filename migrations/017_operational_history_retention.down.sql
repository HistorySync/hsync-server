BEGIN;

DROP TABLE IF EXISTS notification_outbox_archive;
DROP TABLE IF EXISTS ops_check_runs_archive;
DROP TABLE IF EXISTS audit_logs_archive;

COMMIT;
