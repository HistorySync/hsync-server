-- migrations/002_auth_tokens.down.sql

BEGIN;

DROP TABLE IF EXISTS password_resets;
DROP TABLE IF EXISTS email_verifications;

COMMIT;
