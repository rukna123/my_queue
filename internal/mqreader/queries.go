// Package mqreader implements the read-side of the message queue broker.
// Each reader pod owns a range of virtual partitions, loads them into memory,
// and serves lease/ack requests from its in-memory store.
package mqreader

// All SQL queries for the reader service.
const (
	// queryPollReady fetches new ready rows (or expired in-flight) for
	// the reader's partition range, starting after the last seen ID.
	queryPollReady = `
SELECT id, topic, msg_uuid, payload, state, leased_by, lease_id,
       lease_until, attempts, created_at
FROM queue_messages
WHERE partition_key BETWEEN $1 AND $2
  AND (state = 'ready' OR (state = 'in_flight' AND lease_until < now()))
  AND id > $3
ORDER BY id
LIMIT $4`

	// queryLeaseByIDs atomically marks specific rows as in-flight.
	// Used after selecting candidates from the in-memory partition.
	queryLeaseByIDs = `
UPDATE queue_messages
SET state      = 'in_flight',
    leased_by  = $1,
    lease_id   = $2,
    lease_until = now() + $3::int * interval '1 second',
    attempts   = attempts + 1,
    updated_at = now()
WHERE id = ANY($4)`

	// queryAck deletes messages belonging to a lease batch within the
	// reader's partition.
	queryAck = `
DELETE FROM queue_messages
WHERE topic     = $1
  AND leased_by = $2
  AND lease_id  = $3
  AND partition_key BETWEEN $4 AND $5`
)
