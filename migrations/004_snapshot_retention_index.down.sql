-- migrations/004_snapshot_retention_index.down.sql

BEGIN;

DROP INDEX IF EXISTS idx_snapshots_deleted;

COMMIT;
