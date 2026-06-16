-- migrations/021_ce_db_perf_indexes.up.sql
-- Targeted CE indexes for hot operator and scheduler queries.

BEGIN;

-- Supports ops summary/history list queries ordered by newest started_at first.
CREATE INDEX IF NOT EXISTS idx_ops_check_runs_started_at_recent
    ON ops_check_runs (started_at DESC, id DESC);

-- Supports failed ops history list queries without scanning all successful rows.
CREATE INDEX IF NOT EXISTS idx_ops_check_runs_failed_recent
    ON ops_check_runs (started_at DESC, id DESC)
    WHERE overall_status <> 'ok';

-- Supports failed notification retry/list queries ordered by newest failure first.
CREATE INDEX IF NOT EXISTS idx_notification_outbox_failed_recent
    ON notification_outbox (updated_at DESC, created_at DESC, id DESC)
    WHERE status = 'failed';

-- Supports operator support-context lookups for one user's recent erasure jobs.
CREATE INDEX IF NOT EXISTS idx_account_erasure_jobs_user_recent
    ON account_erasure_jobs (user_id, requested_at DESC, id DESC);

COMMIT;
