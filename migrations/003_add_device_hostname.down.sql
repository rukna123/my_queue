-- 003_add_device_hostname.down.sql – Remove device and hostname columns.
ALTER TABLE telemetry DROP COLUMN IF EXISTS hostname;
ALTER TABLE telemetry DROP COLUMN IF EXISTS device;
