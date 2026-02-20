-- 005_telemetry_msg_uuid.up.sql
-- Add msg_uuid column as the dedup key; uuid reverts to the original CSV GPU UUID.

ALTER TABLE telemetry ADD COLUMN msg_uuid UUID;

-- Backfill: existing rows used msg_uuid in the uuid column, copy it over.
UPDATE telemetry SET msg_uuid = uuid WHERE msg_uuid IS NULL;

ALTER TABLE telemetry ALTER COLUMN msg_uuid SET NOT NULL;

-- Drop the old unique constraint on uuid (it was the dedup key).
ALTER TABLE telemetry DROP CONSTRAINT telemetry_uuid_key;

-- New dedup constraint on msg_uuid.
CREATE UNIQUE INDEX idx_telemetry_msg_uuid ON telemetry (msg_uuid);
