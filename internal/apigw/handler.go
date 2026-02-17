package apigw

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
)

// Handler exposes the API gateway HTTP endpoints.
type Handler struct {
	store *Store
}

// NewHandler creates a Handler backed by the given Store.
func NewHandler(store *Store) *Handler {
	return &Handler{store: store}
}

// ---------------------------------------------------------------------------
// Response types
// ---------------------------------------------------------------------------

// GPUItem is a single GPU in the list response.
type GPUItem struct {
	ID string `json:"id" example:"GPU-0a1b2c3d"`
}

// GPUsResponse is the response for GET /api/v1/gpus.
type GPUsResponse struct {
	GPUs []GPUItem `json:"gpus"`
}

// TelemetryResponse is the response for GET /api/v1/gpus/{id}/telemetry.
type TelemetryResponse struct {
	GPUID   string           `json:"gpu_id" example:"GPU-0a1b2c3d"`
	Entries []TelemetryEntry `json:"entries"`
}

type errorResponse struct {
	Error string `json:"error" example:"invalid start_time"`
}

// ---------------------------------------------------------------------------
// GET /api/v1/gpus
// ---------------------------------------------------------------------------

// ListGPUs godoc
//
//	@Summary		List distinct GPUs
//	@Description	Returns the list of distinct gpu_id values present in the telemetry table.
//	@Tags			gpus
//	@Produce		json
//	@Success		200	{object}	GPUsResponse
//	@Failure		500	{object}	errorResponse
//	@Router			/api/v1/gpus [get]
func (h *Handler) ListGPUs(w http.ResponseWriter, r *http.Request) {
	gpus, err := h.store.ListGPUs(r.Context())
	if err != nil {
		slog.Error("list gpus", "error", err)
		writeErr(w, http.StatusInternalServerError, "failed to list GPUs")
		return
	}

	items := make([]GPUItem, len(gpus))
	for i, id := range gpus {
		items[i] = GPUItem{ID: id}
	}

	writeJSON(w, http.StatusOK, GPUsResponse{GPUs: items})
}

// ---------------------------------------------------------------------------
// GET /api/v1/gpus/{id}/telemetry
// ---------------------------------------------------------------------------

// GetTelemetry godoc
//
//	@Summary		Get GPU telemetry
//	@Description	Returns telemetry entries for the specified GPU ordered by timestamp ascending.
//	@Description	Optional query params start_time and end_time filter the time window (RFC3339).
//	@Tags			gpus
//	@Produce		json
//	@Param			id			path		string	true	"GPU ID"				example(GPU-0a1b2c3d)
//	@Param			start_time	query		string	false	"Start time (RFC3339)"	example(2024-01-01T00:00:00Z)
//	@Param			end_time	query		string	false	"End time (RFC3339)"	example(2030-12-31T23:59:59Z)
//	@Success		200			{object}	TelemetryResponse
//	@Failure		400			{object}	errorResponse
//	@Failure		500			{object}	errorResponse
//	@Router			/api/v1/gpus/{id}/telemetry [get]
func (h *Handler) GetTelemetry(w http.ResponseWriter, r *http.Request) {
	gpuID := chi.URLParam(r, "id")
	if gpuID == "" {
		writeErr(w, http.StatusBadRequest, "gpu id is required")
		return
	}

	start, end, err := parseTimeWindow(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	entries, err := h.store.GetTelemetry(r.Context(), gpuID, start, end)
	if err != nil {
		slog.Error("get telemetry", "gpu_id", gpuID, "error", err)
		writeErr(w, http.StatusInternalServerError, "failed to fetch telemetry")
		return
	}
	if entries == nil {
		entries = []TelemetryEntry{}
	}

	writeJSON(w, http.StatusOK, TelemetryResponse{
		GPUID:   gpuID,
		Entries: entries,
	})
}

// parseTimeWindow reads optional start_time / end_time query params.
// Both must be valid RFC3339 if present.  Defaults to a wide window.
func parseTimeWindow(r *http.Request) (start, end time.Time, err error) {
	start = time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)
	end = time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)

	if s := r.URL.Query().Get("start_time"); s != "" {
		start, err = time.Parse(time.RFC3339, s)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid start_time: %w", err)
		}
	}

	if e := r.URL.Query().Get("end_time"); e != "" {
		end, err = time.Parse(time.RFC3339, e)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid end_time: %w", err)
		}
	}

	if !start.Before(end) && !start.Equal(end) {
		return time.Time{}, time.Time{}, fmt.Errorf("start_time must be before or equal to end_time")
	}

	return start, end, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errorResponse{Error: msg})
}
