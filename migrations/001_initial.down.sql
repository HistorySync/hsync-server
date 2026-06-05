-- migrations/001_initial.down.sql

BEGIN;

DROP TRIGGER IF EXISTS trg_users_updated_at ON users;
DROP FUNCTION IF EXISTS update_updated_at_column();

DROP TABLE IF EXISTS quota_limits;
DROP TABLE IF EXISTS storage_usage;
DROP TABLE IF EXISTS device_revocations;
DROP TABLE IF EXISTS snapshots;
DROP TABLE IF EXISTS bundles;
DROP TABLE IF EXISTS devices;
DROP TABLE IF EXISTS refresh_tokens;
DROP TABLE IF EXISTS users;

COMMIT;
