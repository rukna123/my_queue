// Package apigw implements the HTTP handlers and data access for the
// API gateway service.
package apigw

// SQL queries for the apigw service.  Both leverage the existing
// idx_telemetry_gpu_ts index on (gpu_id, timestamp).
const (
	// queryDistinctGPUs returns the unique gpu_id values in the telemetry table.
	queryDistinctGPUs = `SELECT DISTINCT gpu_id FROM telemetry ORDER BY gpu_id`

	// queryTelemetryByGPU returns telemetry entries for a specific GPU,
	// optionally bounded by a time window.
	// Parameters: $1 = gpu_id, $2 = start_time, $3 = end_time.
	queryTelemetryByGPU = `
SELECT uuid, gpu_id, metric_name, timestamp, device, model_name,
       hostname, container, pod, namespace, value, labels_raw, ingested_at
FROM telemetry
WHERE gpu_id = $1
  AND timestamp >= $2
  AND timestamp <= $3
ORDER BY timestamp ASC`
)
