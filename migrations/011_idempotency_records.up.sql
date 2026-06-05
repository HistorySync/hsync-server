-- Add reusable request idempotency records for dangerous mutations.

BEGIN;

CREATE TABLE idempotency_records (
    id                       UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    scope                    TEXT NOT NULL,
    idempotency_key_hash      TEXT NOT NULL,
    request_fingerprint      TEXT NOT NULL,
    status                   TEXT NOT NULL
                             CHECK (status IN ('processing','succeeded','failed')),
    locked_until             TIMESTAMPTZ,
    response_status          INTEGER,
    response_body            JSONB NOT NULL DEFAULT '{}'::jsonb,
    error_reason             TEXT NOT NULL DEFAULT '',
    expires_at               TIMESTAMPTZ NOT NULL,
    created_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (scope, idempotency_key_hash)
);

CREATE INDEX idx_idempotency_records_expires
    ON idempotency_records(expires_at);

CREATE INDEX idx_idempotency_records_status_lock
    ON idempotency_records(status, locked_until);

CREATE TRIGGER trg_idempotency_records_updated_at
    BEFORE UPDATE ON idempotency_records
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

COMMIT;
