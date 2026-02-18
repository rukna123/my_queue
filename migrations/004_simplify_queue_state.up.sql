-- 004_simplify_queue_state.up.sql
-- mqreader now manages message state entirely in memory.
-- DB rows simply exist (unprocessed) or are deleted (done).
-- Drop all lease/state tracking columns and their indexes.

DROP INDEX IF EXISTS idx_qm_topic_state_lease;
DROP INDEX IF EXISTS idx_qm_lease_id;
DROP INDEX IF EXISTS idx_qm_partition_state;

ALTER TABLE queue_messages DROP CONSTRAINT IF EXISTS chk_queue_messages_state;

ALTER TABLE queue_messages DROP COLUMN IF EXISTS state;
ALTER TABLE queue_messages DROP COLUMN IF EXISTS leased_by;
ALTER TABLE queue_messages DROP COLUMN IF EXISTS lease_id;
ALTER TABLE queue_messages DROP COLUMN IF EXISTS lease_until;
ALTER TABLE queue_messages DROP COLUMN IF EXISTS attempts;
ALTER TABLE queue_messages DROP COLUMN IF EXISTS updated_at;

-- Reader pods now only need: partition_key range + ordered by id.
CREATE INDEX IF NOT EXISTS idx_qm_partition_id
    ON queue_messages (partition_key, id);
