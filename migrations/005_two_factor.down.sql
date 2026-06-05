-- migrations/005_two_factor.down.sql
-- Remove two-factor authentication state and backup codes.

BEGIN;

DROP TABLE IF EXISTS user_two_factor_backup_codes;
DROP TRIGGER IF EXISTS trg_user_two_factor_updated_at ON user_two_factor;
DROP TABLE IF EXISTS user_two_factor;

COMMIT;
