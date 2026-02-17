// Package collector implements the consumer that leases messages from the MQ
// reader, persists them to the telemetry table, and acknowledges the lease.
package collector

// SQL queries for the collector service.
const (
	// queryInsertTelemetry inserts a telemetry row using the payload's uuid
	// as the dedup key.  ON CONFLICT DO NOTHING makes it idempotent — the
	// same datapoint is silently skipped if already persisted.
	// RETURNING true lets us distinguish inserts from no-ops.
	queryInsertTelemetry = `
INSERT INTO telemetry (uuid, gpu_id, metric_name, timestamp, model_name, container, pod, namespace, value, labels_raw)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
ON CONFLICT (uuid) DO NOTHING
RETURNING true`
)
