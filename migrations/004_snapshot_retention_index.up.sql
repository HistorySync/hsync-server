-- migrations/004_snapshot_retention_index.up.sql
-- Partial index supporting retention cleanup over soft-deleted snapshots.
--
-- Mirror of 003_bundle_retention_index: the existing snapshot indexes are on live
-- rows (deleted_at IS NULL), so the retention queries
--   CountDeletedBefore / ListDeletedBefore:
--     WHERE deleted_at IS NOT NULL AND deleted_at < $1 ORDER BY deleted_at
-- had no supporting index. This partial index covers those rows; ordered by
-- deleted_at it serves the paging query's ORDER BY + LIMIT without a sort.
-- Target: PostgreSQL 16+

BEGIN;

CREATE INDEX IF NOT EXISTS idx_snapshots_deleted ON snapshots (deleted_at)
    WHERE deleted_at IS NOT NULL;

COMMIT;
