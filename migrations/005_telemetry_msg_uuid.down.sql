-- 005_telemetry_msg_uuid.down.sql
DROP INDEX IF EXISTS idx_telemetry_msg_uuid;
ALTER TABLE telemetry DROP COLUMN IF EXISTS msg_uuid;
ALTER TABLE telemetry ADD CONSTRAINT telemetry_uuid_key UNIQUE (uuid);
