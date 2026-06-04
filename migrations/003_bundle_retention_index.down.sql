-- migrations/003_bundle_retention_index.down.sql

BEGIN;

DROP INDEX IF EXISTS idx_bundles_deleted;

COMMIT;
