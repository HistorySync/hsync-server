-- migrations/006_audit_logs.up.sql
-- Add structured audit events for security-sensitive and admin operations.

BEGIN;

CREATE TABLE audit_logs (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    actor_user_id UUID REFERENCES users(id) ON DELETE SET NULL,
    event_type    TEXT NOT NULL,
    target_type   TEXT NOT NULL DEFAULT '',
    target_id     TEXT NOT NULL DEFAULT '',
    ip            TEXT NOT NULL DEFAULT '',
    user_agent    TEXT NOT NULL DEFAULT '',
    metadata      JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_audit_logs_created_at ON audit_logs(created_at DESC);
CREATE INDEX idx_audit_logs_actor_created_at ON audit_logs(actor_user_id, created_at DESC)
    WHERE actor_user_id IS NOT NULL;
CREATE INDEX idx_audit_logs_event_created_at ON audit_logs(event_type, created_at DESC);
CREATE INDEX idx_audit_logs_target_created_at ON audit_logs(target_type, target_id, created_at DESC);

COMMIT;
