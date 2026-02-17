package apigw_test

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/prompted/prompted/internal/apigw"
)

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

// seedTelemetry inserts test rows and returns the UUIDs.
func seedTelemetry(t *testing.T, db *sql.DB, gpuID string, timestamps []time.Time) []string {
	t.Helper()
	ctx := context.Background()

	uuids := make([]string, len(timestamps))
	for i, ts := range timestamps {
		u := uuid.New().String()
		uuids[i] = u
		_, err := db.ExecContext(ctx,
			`INSERT INTO telemetry (uuid, gpu_id, metric_name, timestamp, model_name, value)
			 VALUES ($1, $2, 'gpu_utilization', $3, 'A100', 80.0)`,
			u, gpuID, ts,
		)
		if err != nil {
			t.Fatalf("seed row %d: %v", i, err)
		}
	}
	return uuids
}

func TestListGPUs(t *testing.T) {
	db := testDB(t)
	store := apigw.NewStore(db)
	ctx := context.Background()

	now := time.Now().UTC()
	seedTelemetry(t, db, "GPU-AAA", []time.Time{now})
	seedTelemetry(t, db, "GPU-BBB", []time.Time{now})
	seedTelemetry(t, db, "GPU-AAA", []time.Time{now.Add(time.Second)})

	gpus, err := store.ListGPUs(ctx)
	if err != nil {
		t.Fatalf("ListGPUs: %v", err)
	}
	if len(gpus) != 2 {
		t.Fatalf("expected 2 distinct GPUs, got %d: %v", len(gpus), gpus)
	}
	if gpus[0] != "GPU-AAA" || gpus[1] != "GPU-BBB" {
		t.Errorf("unexpected GPU order: %v", gpus)
	}
}

func TestGetTelemetry_TimeWindow(t *testing.T) {
	db := testDB(t)
	store := apigw.NewStore(db)
	ctx := context.Background()

	base := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	timestamps := []time.Time{
		base,                          // T+0
		base.Add(10 * time.Second),    // T+10s
		base.Add(20 * time.Second),    // T+20s
		base.Add(30 * time.Second),    // T+30s
		base.Add(60 * time.Second),    // T+60s
	}
	seedTelemetry(t, db, "GPU-TIME", timestamps)

	tests := []struct {
		name     string
		start    time.Time
		end      time.Time
		expected int
	}{
		{
			name:     "full range",
			start:    base.Add(-time.Hour),
			end:      base.Add(time.Hour),
			expected: 5,
		},
		{
			name:     "first 3 entries",
			start:    base,
			end:      base.Add(20 * time.Second),
			expected: 3,
		},
		{
			name:     "last entry only",
			start:    base.Add(60 * time.Second),
			end:      base.Add(2 * time.Hour),
			expected: 1,
		},
		{
			name:     "middle window",
			start:    base.Add(10 * time.Second),
			end:      base.Add(30 * time.Second),
			expected: 3,
		},
		{
			name:     "no results",
			start:    base.Add(2 * time.Hour),
			end:      base.Add(3 * time.Hour),
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entries, err := store.GetTelemetry(ctx, "GPU-TIME", tt.start, tt.end)
			if err != nil {
				t.Fatalf("GetTelemetry: %v", err)
			}
			if len(entries) != tt.expected {
				t.Errorf("expected %d entries, got %d", tt.expected, len(entries))
			}

			// Verify ascending order.
			for i := 1; i < len(entries); i++ {
				if entries[i].Timestamp.Before(entries[i-1].Timestamp) {
					t.Errorf("entries not in ascending order at index %d", i)
				}
			}
		})
	}
}

func TestGetTelemetry_AscendingOrder(t *testing.T) {
	db := testDB(t)
	store := apigw.NewStore(db)
	ctx := context.Background()

	base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	// Insert out of order.
	timestamps := []time.Time{
		base.Add(30 * time.Minute),
		base,
		base.Add(15 * time.Minute),
	}
	seedTelemetry(t, db, "GPU-ORDER", timestamps)

	entries, err := store.GetTelemetry(ctx, "GPU-ORDER",
		base.Add(-time.Hour), base.Add(time.Hour))
	if err != nil {
		t.Fatalf("GetTelemetry: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}

	// Should be sorted: base, base+15m, base+30m.
	if !entries[0].Timestamp.Equal(base) {
		t.Errorf("first entry should be %v, got %v", base, entries[0].Timestamp)
	}
	if !entries[1].Timestamp.Equal(base.Add(15 * time.Minute)) {
		t.Errorf("second entry should be %v, got %v", base.Add(15*time.Minute), entries[1].Timestamp)
	}
	if !entries[2].Timestamp.Equal(base.Add(30 * time.Minute)) {
		t.Errorf("third entry should be %v, got %v", base.Add(30*time.Minute), entries[2].Timestamp)
	}
}
