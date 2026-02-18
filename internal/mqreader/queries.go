package mqreader

const (
	// queryFetchBatch loads the oldest N unprocessed messages for a
	// partition range.  A row existing in queue_messages means it has
	// not been processed; deletion is the only terminal operation.
	queryFetchBatch = `
SELECT id, topic, msg_uuid, payload
FROM queue_messages
WHERE partition_key BETWEEN $1 AND $2
ORDER BY id
LIMIT $3`
)
