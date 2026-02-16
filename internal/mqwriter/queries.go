// Package mqwriter implements the write-side of the message queue broker.
package mqwriter

// All SQL queries for the writer service are collected here.
const (
	// queryPublishOne inserts a single message with its computed partition key.
	// ON CONFLICT makes retries idempotent — the same (topic, msg_uuid)
	// pair is silently ignored.  RETURNING true lets us distinguish inserts
	// from no-ops at the Go layer.
	queryPublishOne = `
INSERT INTO queue_messages (topic, msg_uuid, payload, partition_key)
VALUES ($1, $2, $3::jsonb, $4)
ON CONFLICT (topic, msg_uuid) DO NOTHING
RETURNING true`
)
