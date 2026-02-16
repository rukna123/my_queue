// Package mq implements the custom message-queue broker backed by PostgreSQL.
package mq

// All SQL queries are collected here so they are easy to audit and test.
const (
	// queryPublishOne inserts a single message. ON CONFLICT makes retries
	// idempotent — the same (topic, msg_uuid) pair is silently ignored.
	// RETURNING true lets us distinguish inserts from no-ops at the Go layer.
	queryPublishOne = `
INSERT INTO queue_messages (topic, msg_uuid, payload)
VALUES ($1, $2, $3::jsonb)
ON CONFLICT (topic, msg_uuid) DO NOTHING
RETURNING true`

	// queryLease atomically claims up to $2 messages that are either ready or
	// have an expired lease.  FOR UPDATE SKIP LOCKED ensures concurrent
	// consumers never receive the same message — no application-level locks
	// are needed.
	queryLease = `
WITH candidates AS (
    SELECT id
    FROM queue_messages
    WHERE topic = $1
      AND (state = 'ready' OR (state = 'in_flight' AND lease_until < now()))
    ORDER BY id
    LIMIT $2
    FOR UPDATE SKIP LOCKED
)
UPDATE queue_messages qm
SET state       = 'in_flight',
    leased_by   = $3,
    lease_id    = $4,
    lease_until = now() + $5::int * interval '1 second',
    attempts    = attempts + 1,
    updated_at  = now()
FROM candidates c
WHERE qm.id = c.id
RETURNING qm.msg_uuid, qm.payload`

	// queryAck deletes all messages belonging to a lease batch.
	// Only the original consumer (leased_by match) can ack.
	queryAck = `
DELETE FROM queue_messages
WHERE topic     = $1
  AND leased_by = $2
  AND lease_id  = $3`
)
