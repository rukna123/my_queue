package apigw_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/prompted/prompted/internal/apigw"
)

func TestGetTelemetryHandler_TimeFilters(t *testing.T) {
	db := testDB(t)
	store := apigw.NewStore(db)
	handler := apigw.NewHandler(store)

	base := time.Date(2025, 7, 1, 0, 0, 0, 0, time.UTC)
	seedTelemetry(t, db, "GPU-HTTP", []time.Time{
		base,
		base.Add(10 * time.Second),
		base.Add(20 * time.Second),
	})

	r := chi.NewRouter()
	r.Get("/api/v1/gpus/{id}/telemetry", handler.GetTelemetry)

	tests := []struct {
		name     string
		query    string
		status   int
		expected int
	}{
		{
			name:     "no filters returns all",
			query:    "",
			status:   http.StatusOK,
			expected: 3,
		},
		{
			name:     "start filter only",
			query:    "?start_time=" + base.Add(10*time.Second).Format(time.RFC3339),
			status:   http.StatusOK,
			expected: 2,
		},
		{
			name:     "end filter only",
			query:    "?end_time=" + base.Add(10*time.Second).Format(time.RFC3339),
			status:   http.StatusOK,
			expected: 2,
		},
		{
			name:     "both filters",
			query:    "?start_time=" + base.Format(time.RFC3339) + "&end_time=" + base.Add(10*time.Second).Format(time.RFC3339),
			status:   http.StatusOK,
			expected: 2,
		},
		{
			name:   "invalid start_time",
			query:  "?start_time=not-a-time",
			status: http.StatusBadRequest,
		},
		{
			name:   "invalid end_time",
			query:  "?end_time=nope",
			status: http.StatusBadRequest,
		},
		{
			name:   "start after end",
			query:  "?start_time=2030-01-01T00:00:00Z&end_time=2020-01-01T00:00:00Z",
			status: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/gpus/GPU-HTTP/telemetry"+tt.query, nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != tt.status {
				t.Fatalf("expected status %d, got %d: %s", tt.status, w.Code, w.Body.String())
			}

			if tt.status == http.StatusOK {
				var resp apigw.TelemetryResponse
				if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
					t.Fatalf("decode: %v", err)
				}
				if len(resp.Entries) != tt.expected {
					t.Errorf("expected %d entries, got %d", tt.expected, len(resp.Entries))
				}
				if resp.GPUID != "GPU-HTTP" {
					t.Errorf("expected gpu_id GPU-HTTP, got %s", resp.GPUID)
				}
			}
		})
	}
}

func TestListGPUsHandler(t *testing.T) {
	db := testDB(t)
	store := apigw.NewStore(db)
	handler := apigw.NewHandler(store)

	now := time.Now().UTC()
	seedTelemetry(t, db, "GPU-H1", []time.Time{now})
	seedTelemetry(t, db, "GPU-H2", []time.Time{now})

	r := chi.NewRouter()
	r.Get("/api/v1/gpus", handler.ListGPUs)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/gpus", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp apigw.GPUsResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.GPUs) != 2 {
		t.Errorf("expected 2 GPUs, got %d", len(resp.GPUs))
	}
}
