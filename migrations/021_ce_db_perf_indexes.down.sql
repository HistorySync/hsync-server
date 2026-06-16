BEGIN;

DROP INDEX IF EXISTS idx_account_erasure_jobs_user_recent;
DROP INDEX IF EXISTS idx_notification_outbox_failed_recent;
DROP INDEX IF EXISTS idx_ops_check_runs_failed_recent;
DROP INDEX IF EXISTS idx_ops_check_runs_started_at_recent;

COMMIT;
