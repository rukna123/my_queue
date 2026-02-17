package apigw

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// TelemetryEntry represents a single telemetry row returned by the API.
type TelemetryEntry struct {
	UUID       string    `json:"uuid"`
	GPUID      string    `json:"gpu_id"`
	MetricName string    `json:"metric_name"`
	Timestamp  time.Time `json:"timestamp"`
	ModelName  *string   `json:"model_name"`
	Container  *string   `json:"container"`
	Pod        *string   `json:"pod"`
	Namespace  *string   `json:"namespace"`
	Value      *float64  `json:"value"`
	LabelsRaw  *string   `json:"labels_raw"`
	IngestedAt time.Time `json:"ingested_at"`
}

// Store provides read-only database access for the API gateway.
type Store struct {
	db *sql.DB
}

// NewStore creates a Store.
func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// ListGPUs returns distinct gpu_id values from the telemetry table.
func (s *Store) ListGPUs(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, queryDistinctGPUs)
	if err != nil {
		return nil, fmt.Errorf("list gpus: %w", err)
	}
	defer rows.Close()

	var gpus []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan gpu_id: %w", err)
		}
		gpus = append(gpus, id)
	}
	return gpus, rows.Err()
}

// GetTelemetry returns telemetry entries for the given GPU within the
// specified time window, ordered by timestamp ascending.
func (s *Store) GetTelemetry(ctx context.Context, gpuID string, start, end time.Time) ([]TelemetryEntry, error) {
	rows, err := s.db.QueryContext(ctx, queryTelemetryByGPU, gpuID, start, end)
	if err != nil {
		return nil, fmt.Errorf("get telemetry: %w", err)
	}
	defer rows.Close()

	var entries []TelemetryEntry
	for rows.Next() {
		var e TelemetryEntry
		if err := rows.Scan(
			&e.UUID,
			&e.GPUID,
			&e.MetricName,
			&e.Timestamp,
			&e.ModelName,
			&e.Container,
			&e.Pod,
			&e.Namespace,
			&e.Value,
			&e.LabelsRaw,
			&e.IngestedAt,
		); err != nil {
			return nil, fmt.Errorf("scan telemetry: %w", err)
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}
