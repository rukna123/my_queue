-- 002_add_partition_key.down.sql

DROP INDEX IF EXISTS idx_qm_partition_state;
ALTER TABLE queue_messages DROP COLUMN IF EXISTS partition_key;
