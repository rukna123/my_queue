package collector_test

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/prompted/prompted/internal/collector"
)

// testDB opens a connection to the test Postgres and truncates the telemetry
// table.  Set TEST_DATABASE_URL to point at a test database.
func testDB(t *testing.T) *sql.DB {
	t.Helper()

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://prompted:prompted@localhost:5432/prompted?sslmode=disable"
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		t.Skipf("test database not reachable: %v", err)
	}

	if _, err := db.ExecContext(ctx, "TRUNCATE telemetry"); err != nil {
		t.Fatalf("truncate telemetry: %v", err)
	}

	t.Cleanup(func() { db.Close() })
	return db
}

func TestPersistBatch_Basic(t *testing.T) {
	db := testDB(t)
	store := collector.NewStore(db)
	ctx := context.Background()

	rows := []collector.TelemetryRow{
		{
			UUID:       "550e8400-e29b-41d4-a716-446655440001",
			GPUID:      "GPU-aaa",
			MetricName: "gpu_utilization",
			Timestamp:  time.Now().UTC(),
			ModelName:  "A100",
			Container:  "train",
			Pod:        "pod-0",
			Namespace:  "ml",
			Value:      87.3,
			LabelsRaw:  "team=ml",
		},
		{
			UUID:       "550e8400-e29b-41d4-a716-446655440002",
			GPUID:      "GPU-bbb",
			MetricName: "gpu_memory_used_bytes",
			Timestamp:  time.Now().UTC(),
			ModelName:  "H100",
			Container:  "infer",
			Pod:        "pod-1",
			Namespace:  "serving",
			Value:      42949672960,
			LabelsRaw:  "team=infra",
		},
	}

	persisted, dupes, err := store.PersistBatch(ctx, rows)
	if err != nil {
		t.Fatalf("PersistBatch: %v", err)
	}
	if persisted != 2 {
		t.Errorf("expected 2 persisted, got %d", persisted)
	}
	if dupes != 0 {
		t.Errorf("expected 0 duplicates, got %d", dupes)
	}
}

func TestPersistBatch_Idempotent(t *testing.T) {
	db := testDB(t)
	store := collector.NewStore(db)
	ctx := context.Background()

	row := collector.TelemetryRow{
		UUID:       "550e8400-e29b-41d4-a716-446655440099",
		GPUID:      "GPU-xxx",
		MetricName: "gpu_utilization",
		Timestamp:  time.Now().UTC(),
		ModelName:  "A100",
		Value:      50.0,
	}

	// First insert — should succeed.
	persisted, dupes, err := store.PersistBatch(ctx, []collector.TelemetryRow{row})
	if err != nil {
		t.Fatalf("first PersistBatch: %v", err)
	}
	if persisted != 1 || dupes != 0 {
		t.Fatalf("first: expected 1 persisted / 0 dupes, got %d / %d", persisted, dupes)
	}

	// Second insert with same UUID — should be silently skipped.
	persisted, dupes, err = store.PersistBatch(ctx, []collector.TelemetryRow{row})
	if err != nil {
		t.Fatalf("second PersistBatch: %v", err)
	}
	if persisted != 0 {
		t.Errorf("second: expected 0 persisted, got %d", persisted)
	}
	if dupes != 1 {
		t.Errorf("second: expected 1 duplicate, got %d", dupes)
	}

	// Verify only one row exists.
	var count int
	err = db.QueryRowContext(ctx, "SELECT count(*) FROM telemetry WHERE uuid = $1", row.UUID).Scan(&count)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 row in DB, got %d", count)
	}
}

func TestPersistBatch_MixedDuplicates(t *testing.T) {
	db := testDB(t)
	store := collector.NewStore(db)
	ctx := context.Background()

	existing := collector.TelemetryRow{
		UUID:       "550e8400-e29b-41d4-a716-446655440050",
		GPUID:      "GPU-existing",
		MetricName: "gpu_temp",
		Timestamp:  time.Now().UTC(),
		Value:      72.0,
	}

	// Pre-insert one row.
	if _, _, err := store.PersistBatch(ctx, []collector.TelemetryRow{existing}); err != nil {
		t.Fatalf("pre-insert: %v", err)
	}

	// Now insert a batch with the existing row + a new row.
	newRow := collector.TelemetryRow{
		UUID:       "550e8400-e29b-41d4-a716-446655440051",
		GPUID:      "GPU-new",
		MetricName: "gpu_power",
		Timestamp:  time.Now().UTC(),
		Value:      200.0,
	}

	persisted, dupes, err := store.PersistBatch(ctx, []collector.TelemetryRow{existing, newRow})
	if err != nil {
		t.Fatalf("mixed batch: %v", err)
	}
	if persisted != 1 {
		t.Errorf("expected 1 persisted, got %d", persisted)
	}
	if dupes != 1 {
		t.Errorf("expected 1 duplicate, got %d", dupes)
	}
}
