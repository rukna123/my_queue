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

// msgFields is used to extract the uuid and gpu_id from a raw message.
type msgFields struct {
	UUID  string `json:"uuid"`
	GPUID string `json:"gpu_id"`
}

// ---------------------------------------------------------------------------
// POST /v1/publish
// ---------------------------------------------------------------------------

// Publish accepts raw CSV row objects, extracts uuid and gpu_id from each,
// computes the partition key, and pushes them into the write buffer.
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
		if fields.UUID == "" {
			writeErr(w, http.StatusBadRequest, "messages["+itoa(i)+"]: uuid is required")
			return
		}
		if fields.GPUID == "" {
			writeErr(w, http.StatusBadRequest, "messages["+itoa(i)+"]: gpu_id is required")
			return
		}

		parsed, err := uuid.Parse(fields.UUID)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "messages["+itoa(i)+"]: invalid uuid: "+err.Error())
			return
		}

		msgs = append(msgs, IngestMsg{
			UUID:      parsed,
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

// MetricsHandler returns buffer utilization as a simple JSON payload for HPA.
func (h *Handler) MetricsHandler(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]float64{
		"buffer_utilization": h.buffer.BufferUtilization(),
	})
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
