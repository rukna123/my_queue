package collector

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// TelemetryRow is parsed from a leased message payload.
type TelemetryRow struct {
	UUID       string    `json:"uuid"`
	GPUID      string    `json:"gpu_id"`
	MetricName string    `json:"metric_name"`
	Timestamp  time.Time `json:"timestamp"`
	ModelName  string    `json:"model_name"`
	Container  string    `json:"container"`
	Pod        string    `json:"pod"`
	Namespace  string    `json:"namespace"`
	Value      float64   `json:"value"`
	LabelsRaw  string    `json:"labels_raw"`
}

// Persister abstracts telemetry persistence so the collector loop can be
// tested with a mock.
type Persister interface {
	PersistBatch(ctx context.Context, rows []TelemetryRow) (persisted, duplicates int, err error)
}

// Store persists telemetry rows to PostgreSQL.
type Store struct {
	db *sql.DB
}

// NewStore creates a Store.
func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// PersistBatch inserts telemetry rows in a single transaction.
// Duplicates (same uuid) are silently skipped via ON CONFLICT DO NOTHING.
// Returns the count of newly persisted rows and duplicates.
func (s *Store) PersistBatch(ctx context.Context, rows []TelemetryRow) (persisted, duplicates int, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, queryInsertTelemetry)
	if err != nil {
		return 0, 0, fmt.Errorf("prepare: %w", err)
	}
	defer stmt.Close()

	for _, row := range rows {
		parsed, err := uuid.Parse(row.UUID)
		if err != nil {
			return 0, 0, fmt.Errorf("invalid uuid %q: %w", row.UUID, err)
		}

		var ok bool
		err = stmt.QueryRowContext(ctx,
			parsed,
			row.GPUID,
			row.MetricName,
			row.Timestamp,
			nullStr(row.ModelName),
			nullStr(row.Container),
			nullStr(row.Pod),
			nullStr(row.Namespace),
			nullFloat(row.Value),
			nullStr(row.LabelsRaw),
		).Scan(&ok)

		switch {
		case err == sql.ErrNoRows:
			duplicates++
		case err != nil:
			return 0, 0, fmt.Errorf("insert uuid %s: %w", row.UUID, err)
		default:
			persisted++
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, 0, fmt.Errorf("commit: %w", err)
	}
	return persisted, duplicates, nil
}

func nullStr(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

func nullFloat(f float64) sql.NullFloat64 {
	return sql.NullFloat64{Float64: f, Valid: true}
}
