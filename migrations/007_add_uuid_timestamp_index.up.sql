-- 007_add_uuid_timestamp_index.up.sql
-- Supports the API query: WHERE uuid = $1 AND timestamp >= $2 AND timestamp <= $3
CREATE INDEX idx_telemetry_uuid_ts ON telemetry (uuid, timestamp);
