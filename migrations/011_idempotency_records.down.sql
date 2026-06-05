-- Remove reusable request idempotency records.

BEGIN;

DROP TABLE IF EXISTS idempotency_records;

COMMIT;
