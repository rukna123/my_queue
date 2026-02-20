// Package apigw implements the HTTP handlers and data access for the
// API gateway service.
package apigw

// SQL queries for the apigw service.
const (
	// queryDistinctGPUs returns the unique GPU hardware UUIDs from the
	// telemetry table (the uuid column holds the CSV's GPU UUID).
	queryDistinctGPUs = `SELECT DISTINCT uuid FROM telemetry WHERE uuid IS NOT NULL ORDER BY uuid`

	// queryTelemetryByGPU returns telemetry entries for a specific GPU
	// identified by its hardware UUID, optionally bounded by a time window.
	// Parameters: $1 = uuid (GPU hardware UUID), $2 = start_time, $3 = end_time.
	queryTelemetryByGPU = `
SELECT uuid, gpu_id, metric_name, timestamp, device, model_name,
       hostname, container, pod, namespace, value, labels_raw, ingested_at
FROM telemetry
WHERE uuid = $1
  AND timestamp >= $2
  AND timestamp <= $3
ORDER BY timestamp ASC`
)
