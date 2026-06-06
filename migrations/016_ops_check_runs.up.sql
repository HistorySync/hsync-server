BEGIN;

CREATE TABLE ops_check_runs (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    run_type            TEXT NOT NULL CHECK (run_type IN ('dependency','consistency')),
    overall_status      TEXT NOT NULL,
    started_at          TIMESTAMPTZ NOT NULL,
    finished_at         TIMESTAMPTZ NOT NULL,
    duration_millis     BIGINT NOT NULL DEFAULT 0,
    summarized_findings JSONB NOT NULL DEFAULT '{}'::jsonb,
    artifact_counts     JSONB NOT NULL DEFAULT '{}'::jsonb,
    report_json         JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_ops_check_runs_recent
    ON ops_check_runs (run_type, started_at DESC);

CREATE INDEX idx_ops_check_runs_failures
    ON ops_check_runs (started_at DESC)
    WHERE overall_status <> 'ok';

COMMIT;
