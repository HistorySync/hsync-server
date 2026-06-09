-- migrations/019_account_erasure_jobs.down.sql

BEGIN;

DROP TRIGGER IF EXISTS trg_account_erasure_jobs_updated_at ON account_erasure_jobs;
DROP INDEX IF EXISTS idx_account_erasure_jobs_status_eligible;
DROP INDEX IF EXISTS idx_account_erasure_jobs_user_open;
DROP TABLE IF EXISTS account_erasure_jobs;

COMMIT;
