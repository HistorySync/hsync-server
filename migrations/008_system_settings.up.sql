-- migrations/008_system_settings.up.sql
-- Add database-driven dynamic system settings for runtime feature switches.
--
-- A row is an override for a code-declared, whitelisted key. A missing row means
-- "use the code default", so this table is never required for the server to boot;
-- startup-critical configuration still loads from pkg/config. value_type and
-- description are persisted copies of the code definition so the table is
-- self-describing on direct inspection, while the code registry stays authoritative.

BEGIN;

CREATE TABLE system_settings (
    key         TEXT PRIMARY KEY,
    value       TEXT NOT NULL DEFAULT '',
    value_type  TEXT NOT NULL DEFAULT 'string',
    description TEXT NOT NULL DEFAULT '',
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TRIGGER trg_system_settings_updated_at
    BEFORE UPDATE ON system_settings
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

COMMIT;
