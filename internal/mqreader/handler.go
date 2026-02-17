package mqreader

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"
)

// Handler exposes the reader HTTP endpoints (lease and ack).
type Handler struct {
	partition *Partition
}

// NewHandler creates a Handler backed by the given Partition.
func NewHandler(partition *Partition) *Handler {
	return &Handler{partition: partition}
}

// ---------------------------------------------------------------------------
// Request / Response types
// ---------------------------------------------------------------------------

// LeaseRequest is the body of POST /v1/lease.
type LeaseRequest struct {
	Topic        string `json:"topic"`
	ConsumerID   string `json:"consumer_id"`
	Max          int    `json:"max"`
	LeaseSeconds int    `json:"lease_seconds"`
}

// LeaseMessageResponse is a single message in the lease response.
type LeaseMessageResponse struct {
	MsgUUID string          `json:"msg_uuid"`
	Payload json.RawMessage `json:"payload"`
}

// LeaseResponse is returned by POST /v1/lease.
type LeaseResponse struct {
	LeaseID  string                 `json:"lease_id"`
	Messages []LeaseMessageResponse `json:"messages"`
}

// AckRequest is the body of POST /v1/ack.
type AckRequest struct {
	Topic      string `json:"topic"`
	ConsumerID string `json:"consumer_id"`
	LeaseID    string `json:"lease_id"`
}

// AckResponse is returned by POST /v1/ack.
type AckResponse struct {
	Acked int64 `json:"acked"`
}

type errorResponse struct {
	Error string `json:"error"`
}

// ---------------------------------------------------------------------------
// POST /v1/lease
// ---------------------------------------------------------------------------

// Lease returns messages from the in-memory partition.
// Response messages are raw JSON objects — the original CSV row content.
func (h *Handler) Lease(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	var req LeaseRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if req.Topic == "" {
		writeErr(w, http.StatusBadRequest, "topic is required")
		return
	}
	if req.ConsumerID == "" {
		writeErr(w, http.StatusBadRequest, "consumer_id is required")
		return
	}
	if req.Max <= 0 {
		writeErr(w, http.StatusBadRequest, "max must be > 0")
		return
	}
	if req.LeaseSeconds <= 0 {
		writeErr(w, http.StatusBadRequest, "lease_seconds must be > 0")
		return
	}

	result := h.partition.Lease(r.Context(), req.Topic, req.ConsumerID, req.Max, req.LeaseSeconds)
	if result.Err != nil {
		slog.Error("lease failed",
			"topic", req.Topic,
			"consumer_id", req.ConsumerID,
			"error", result.Err,
		)
		writeErr(w, http.StatusInternalServerError, "lease failed")
		return
	}

	// Build response with msg_uuid + payload for each message.
	msgs := make([]LeaseMessageResponse, len(result.Messages))
	for i, m := range result.Messages {
		msgs[i] = LeaseMessageResponse{
			MsgUUID: m.MsgUUID,
			Payload: m.Payload,
		}
	}

	slog.Info("leased",
		"topic", req.Topic,
		"consumer_id", req.ConsumerID,
		"leased", len(msgs),
		"requested_max", req.Max,
		"latency_ms", time.Since(start).Milliseconds(),
	)

	writeJSON(w, http.StatusOK, LeaseResponse{
		LeaseID:  result.LeaseID,
		Messages: msgs,
	})
}

// ---------------------------------------------------------------------------
// POST /v1/ack
// ---------------------------------------------------------------------------

// Ack marks a lease batch as acknowledged and removes messages.
func (h *Handler) Ack(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	var req AckRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if req.Topic == "" {
		writeErr(w, http.StatusBadRequest, "topic is required")
		return
	}
	if req.ConsumerID == "" {
		writeErr(w, http.StatusBadRequest, "consumer_id is required")
		return
	}
	if req.LeaseID == "" {
		writeErr(w, http.StatusBadRequest, "lease_id is required")
		return
	}

	result := h.partition.Ack(r.Context(), req.Topic, req.ConsumerID, req.LeaseID)
	if result.Err != nil {
		slog.Error("ack failed",
			"topic", req.Topic,
			"consumer_id", req.ConsumerID,
			"lease_id", req.LeaseID,
			"error", result.Err,
		)
		writeErr(w, http.StatusInternalServerError, "ack failed")
		return
	}

	slog.Info("acked",
		"topic", req.Topic,
		"consumer_id", req.ConsumerID,
		"lease_id", req.LeaseID,
		"acked", result.Acked,
		"latency_ms", time.Since(start).Milliseconds(),
	)

	writeJSON(w, http.StatusOK, AckResponse{Acked: result.Acked})
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
