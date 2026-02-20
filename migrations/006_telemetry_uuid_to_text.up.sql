-- 006_telemetry_uuid_to_text.up.sql
-- The uuid column now holds CSV GPU identifiers (e.g. "GPU-...") which are
-- not valid PostgreSQL UUIDs. Change to TEXT.
ALTER TABLE telemetry ALTER COLUMN uuid TYPE TEXT USING uuid::TEXT;
