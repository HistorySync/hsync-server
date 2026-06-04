-- migrations/003_bundle_retention_index.up.sql
-- Partial index supporting retention cleanup over soft-deleted bundles.
--
-- The existing bundle indexes are partial on "deleted_at IS NULL" (live rows),
-- so the retention queries that scan soft-deleted rows had no supporting index
-- and fell back to a sequential scan over the whole table:
--   CountDeletedBefore / ListDeletedBefore:
--     WHERE deleted_at IS NOT NULL AND deleted_at < $1 ORDER BY deleted_at
-- This partial index covers exactly those rows; it serves the range filter and,
-- being ordered by deleted_at, the ORDER BY + LIMIT of the paging query without a
-- sort. Plain (non-CONCURRENT) build so it stays inside the migration's
-- transaction, matching the other migrations; CE-scale tables build quickly.
-- Target: PostgreSQL 16+

BEGIN;

CREATE INDEX IF NOT EXISTS idx_bundles_deleted ON bundles (deleted_at)
    WHERE deleted_at IS NOT NULL;

COMMIT;
