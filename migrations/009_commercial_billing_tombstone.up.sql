-- Compatibility tombstone for the removed CE commercial billing migration.
--
-- Older CE databases may already have version 009 recorded in
-- schema_migrations. Keep this definition so migrate down can plan across that
-- version. Fresh CE installs intentionally do not create commercial tables.

BEGIN;

COMMIT;
