-- 004_simplify_queue_state.down.sql – Restore state/lease columns.

DROP INDEX IF EXISTS idx_qm_partition_id;

ALTER TABLE queue_messages ADD COLUMN IF NOT EXISTS state TEXT NOT NULL DEFAULT 'ready';
ALTER TABLE queue_messages ADD COLUMN IF NOT EXISTS leased_by TEXT;
ALTER TABLE queue_messages ADD COLUMN IF NOT EXISTS lease_id UUID;
ALTER TABLE queue_messages ADD COLUMN IF NOT EXISTS lease_until TIMESTAMPTZ;
ALTER TABLE queue_messages ADD COLUMN IF NOT EXISTS attempts INT NOT NULL DEFAULT 0;
ALTER TABLE queue_messages ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT now();

ALTER TABLE queue_messages ADD CONSTRAINT chk_queue_messages_state
    CHECK (state IN ('ready', 'in_flight', 'acked'));

CREATE INDEX IF NOT EXISTS idx_qm_topic_state_lease
    ON queue_messages (topic, state, lease_until);
CREATE INDEX IF NOT EXISTS idx_qm_lease_id
    ON queue_messages (lease_id);
CREATE INDEX IF NOT EXISTS idx_qm_partition_state
    ON queue_messages (partition_key, state, id);
