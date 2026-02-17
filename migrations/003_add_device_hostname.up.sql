-- 003_add_device_hostname.up.sql – Add device and hostname columns to telemetry.
ALTER TABLE telemetry ADD COLUMN device TEXT;
ALTER TABLE telemetry ADD COLUMN hostname TEXT;
