package mqwriter

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
)

// Handler exposes the writer HTTP endpoints (publish only).
type Handler struct {
	buffer          *WriteBuffer
	totalPartitions int
}

// NewHandler creates a Handler backed by the given WriteBuffer.
func NewHandler(buffer *WriteBuffer, totalPartitions int) *Handler {
	return &Handler{buffer: buffer, totalPartitions: totalPartitions}
}

// ---------------------------------------------------------------------------
// Request / Response types
// ---------------------------------------------------------------------------

// PublishRequest is the body of POST /v1/publish.
// Messages are raw JSON objects — the original CSV row content.
type PublishRequest struct {
	Topic    string            `json:"topic"`
	Messages []json.RawMessage `json:"messages"`
}

// PublishResponse is returned by POST /v1/publish.
type PublishResponse struct {
	Accepted   int `json:"accepted"`
	Duplicates int `json:"duplicates"`
}

type errorResponse struct {
	Error string `json:"error"`
}

// msgFields is used to extract gpu_id from a raw message for partitioning.
type msgFields struct {
	GPUID string `json:"gpu_id"`
}

// ---------------------------------------------------------------------------
// POST /v1/publish
// ---------------------------------------------------------------------------

// Publish accepts raw CSV row objects.  The writer generates a unique msg_uuid
// for each message (the producer doesn't need to provide one) and extracts
// gpu_id from the payload to compute the partition key.
func (h *Handler) Publish(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	var req PublishRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if req.Topic == "" {
		writeErr(w, http.StatusBadRequest, "topic is required")
		return
	}
	if len(req.Messages) == 0 {
		writeErr(w, http.StatusBadRequest, "messages must not be empty")
		return
	}

	msgs := make([]IngestMsg, 0, len(req.Messages))
	for i, raw := range req.Messages {
		var fields msgFields
		if err := json.Unmarshal(raw, &fields); err != nil {
			writeErr(w, http.StatusBadRequest, "messages["+itoa(i)+"]: "+err.Error())
			return
		}
		if fields.GPUID == "" {
			writeErr(w, http.StatusBadRequest, "messages["+itoa(i)+"]: gpu_id is required")
			return
		}

		msgs = append(msgs, IngestMsg{
			UUID:      uuid.New(),
			GPUID:     fields.GPUID,
			Payload:   raw,
			Partition: PartitionFor(fields.GPUID, h.totalPartitions),
		})
	}

	accepted, dupes, err := h.buffer.Submit(r.Context(), req.Topic, msgs)
	if err != nil {
		slog.Error("publish failed", "topic", req.Topic, "error", err)
		writeErr(w, http.StatusInternalServerError, "publish failed")
		return
	}

	slog.Info("published",
		"topic", req.Topic,
		"accepted", accepted,
		"duplicates", dupes,
		"count", len(msgs),
		"latency_ms", time.Since(start).Milliseconds(),
	)

	writeJSON(w, http.StatusOK, PublishResponse{Accepted: accepted, Duplicates: dupes})
}

// MetricsHandler returns buffer utilization as a simple JSON payload.
func (h *Handler) MetricsHandler(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]float64{
		"buffer_utilization": h.buffer.BufferUtilization(),
	})
}

// PrometheusMetrics returns buffer utilization in Prometheus text exposition format.
func (h *Handler) PrometheusMetrics(w http.ResponseWriter, _ *http.Request) {
	util := h.buffer.BufferUtilization()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, "# HELP mqwriter_buffer_utilization Ratio of pending items in write buffer (0.0-1.0)\n")
	fmt.Fprintf(w, "# TYPE mqwriter_buffer_utilization gauge\n")
	fmt.Fprintf(w, "mqwriter_buffer_utilization %f\n", util)
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

func itoa(i int) string {
	return fmt.Sprintf("%d", i)
}
