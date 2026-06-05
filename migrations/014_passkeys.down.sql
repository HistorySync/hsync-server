BEGIN;

DROP TABLE IF EXISTS passkey_challenges;
DROP TRIGGER IF EXISTS trg_passkey_credentials_updated_at ON passkey_credentials;
DROP TABLE IF EXISTS passkey_credentials;

COMMIT;
