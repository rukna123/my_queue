-- 006_telemetry_uuid_to_text.down.sql
ALTER TABLE telemetry ALTER COLUMN uuid TYPE UUID USING uuid::UUID;
