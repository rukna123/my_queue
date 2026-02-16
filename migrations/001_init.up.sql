-- 001_init.up.sql – Core schema: queue_messages + telemetry

-- ---------------------------------------------------------------------------
-- queue_messages – backing table for the custom MQ broker
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS queue_messages (
    id          BIGSERIAL       PRIMARY KEY,
    topic       TEXT            NOT NULL,
    msg_uuid    UUID            NOT NULL,
    payload     JSONB           NOT NULL,
    state       TEXT            NOT NULL DEFAULT 'ready',    -- ready | in_flight | acked
    leased_by   TEXT,
    lease_id    UUID,
    lease_until TIMESTAMPTZ,
    attempts    INT             NOT NULL DEFAULT 0,
    created_at  TIMESTAMPTZ     NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ     NOT NULL DEFAULT now(),

    CONSTRAINT chk_queue_messages_state CHECK (state IN ('ready', 'in_flight', 'acked'))
);

-- Dedup: one msg_uuid per topic.
CREATE UNIQUE INDEX idx_qm_topic_msg_uuid
    ON queue_messages (topic, msg_uuid);

-- Lease query: find ready messages or expired leases.
CREATE INDEX idx_qm_topic_state_lease
    ON queue_messages (topic, state, lease_until);

-- Fast lookup by lease_id (ack / nack path).
CREATE INDEX idx_qm_lease_id
    ON queue_messages (lease_id);

-- ---------------------------------------------------------------------------
-- telemetry – persisted GPU telemetry entries
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS telemetry (
    id           BIGSERIAL        PRIMARY KEY,
    uuid         UUID             NOT NULL UNIQUE,     -- dedup key
    gpu_id       TEXT             NOT NULL,
    metric_name  TEXT             NOT NULL,
    timestamp    TIMESTAMPTZ      NOT NULL,             -- processed_at time from streamer
    model_name   TEXT,
    container    TEXT,
    pod          TEXT,
    namespace    TEXT,
    value        DOUBLE PRECISION,
    labels_raw   TEXT,
    ingested_at  TIMESTAMPTZ      NOT NULL DEFAULT now()
);

-- Time-ordered queries per GPU.
CREATE INDEX idx_telemetry_gpu_ts
    ON telemetry (gpu_id, timestamp);
