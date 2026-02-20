// Package collector implements the consumer that leases messages from the MQ
// reader, persists them to the telemetry table, and acknowledges the lease.
package collector

// SQL queries for the collector service.
const (
	// queryInsertTelemetry inserts a telemetry row using msg_uuid (the
	// mqwriter-generated key) for dedup.  The uuid column holds the
	// original GPU UUID from the CSV.
	// RETURNING true lets us distinguish inserts from no-ops.
	queryInsertTelemetry = `
INSERT INTO telemetry (uuid, msg_uuid, gpu_id, metric_name, timestamp, device, model_name, hostname, container, pod, namespace, value, labels_raw)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
ON CONFLICT (msg_uuid) DO NOTHING
RETURNING true`
)
