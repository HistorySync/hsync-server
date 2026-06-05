-- migrations/006_audit_logs.down.sql
-- Remove structured audit events.

BEGIN;

DROP TABLE IF EXISTS audit_logs;

COMMIT;
