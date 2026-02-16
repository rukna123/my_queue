-- 002_add_partition_key.up.sql – Add virtual partition column for reader sharding.

ALTER TABLE queue_messages ADD COLUMN partition_key INT NOT NULL DEFAULT 0;

-- Reader pods query their partition range: WHERE partition_key BETWEEN $start AND $end.
CREATE INDEX idx_qm_partition_state ON queue_messages (partition_key, state, id);
