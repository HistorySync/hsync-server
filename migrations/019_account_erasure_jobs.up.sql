-- migrations/019_account_erasure_jobs.up.sql
-- Retention-gated account erasure jobs and final certificate JSON.

BEGIN;

CREATE TABLE account_erasure_jobs (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      UUID NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    requested_at TIMESTAMPTZ NOT NULL,
    eligible_at  TIMESTAMPTZ NOT NULL,
    status       TEXT NOT NULL DEFAULT 'pending'
                 CHECK (status IN ('pending','running','completed','failed')),
    summary      JSONB NOT NULL DEFAULT '{}'::jsonb,
    last_error   TEXT NOT NULL DEFAULT '',
    started_at   TIMESTAMPTZ,
    finished_at  TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX idx_account_erasure_jobs_user_open
    ON account_erasure_jobs(user_id)
    WHERE status IN ('pending','running','failed');

CREATE INDEX idx_account_erasure_jobs_status_eligible
    ON account_erasure_jobs(status, eligible_at, requested_at);

CREATE TRIGGER trg_account_erasure_jobs_updated_at
    BEFORE UPDATE ON account_erasure_jobs
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

COMMIT;
