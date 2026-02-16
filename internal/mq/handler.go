package mq

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
)

// Handler exposes the MQ HTTP endpoints.
type Handler struct {
	store  *Store
	buffer *WriteBuffer
}

// NewHandler creates a Handler backed by the given Store and WriteBuffer.
// Publish requests flow through the WriteBuffer (batched async writes);
// Lease and Ack go directly to the Store for strong consistency.
func NewHandler(store *Store, buffer *WriteBuffer) *Handler {
	return &Handler{store: store, buffer: buffer}
}

// ---------------------------------------------------------------------------
// Request / Response types
// ---------------------------------------------------------------------------

// PublishRequest is the body of POST /v1/publish.
type PublishRequest struct {
	Topic    string       `json:"topic"`
	Messages []PublishMsg `json:"messages"`
}

// PublishResponse is returned by POST /v1/publish.
type PublishResponse struct {
	Accepted   int `json:"accepted"`
	Duplicates int `json:"duplicates"`
}

// LeaseRequest is the body of POST /v1/lease.
type LeaseRequest struct {
	Topic        string `json:"topic"`
	ConsumerID   string `json:"consumer_id"`
	Max          int    `json:"max"`
	LeaseSeconds int    `json:"lease_seconds"`
}

// LeaseResponse is returned by POST /v1/lease.
type LeaseResponse struct {
	LeaseID  uuid.UUID   `json:"lease_id"`
	Messages []LeasedMsg `json:"messages"`
}

// AckRequest is the body of POST /v1/ack.
type AckRequest struct {
	Topic      string    `json:"topic"`
	ConsumerID string    `json:"consumer_id"`
	LeaseID    uuid.UUID `json:"lease_id"`
}

// AckResponse is returned by POST /v1/ack.
type AckResponse struct {
	Acked int64 `json:"acked"`
}

type errorResponse struct {
	Error string `json:"error"`
}

// ---------------------------------------------------------------------------
// POST /v1/publish
// ---------------------------------------------------------------------------

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
	for i, m := range req.Messages {
		if m.MsgUUID == uuid.Nil {
			writeErr(w, http.StatusBadRequest, fmt.Sprintf("messages[%d].msg_uuid is required", i))
			return
		}
		if len(m.Payload) == 0 || string(m.Payload) == "null" {
			writeErr(w, http.StatusBadRequest, fmt.Sprintf("messages[%d].payload is required", i))
			return
		}
	}

	accepted, dupes, err := h.buffer.Submit(r.Context(), req.Topic, req.Messages)
	if err != nil {
		slog.Error("publish failed", "topic", req.Topic, "error", err)
		writeErr(w, http.StatusInternalServerError, "publish failed")
		return
	}

	slog.Info("published",
		"topic", req.Topic,
		"accepted", accepted,
		"duplicates", dupes,
		"batch_size", len(req.Messages),
		"latency_ms", time.Since(start).Milliseconds(),
	)

	writeJSON(w, http.StatusOK, PublishResponse{
		Accepted:   accepted,
		Duplicates: dupes,
	})
}

// ---------------------------------------------------------------------------
// POST /v1/lease
// ---------------------------------------------------------------------------

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

	leaseID, msgs, err := h.store.Lease(r.Context(), req.Topic, req.ConsumerID, req.Max, req.LeaseSeconds)
	if err != nil {
		slog.Error("lease failed",
			"topic", req.Topic,
			"consumer_id", req.ConsumerID,
			"error", err,
		)
		writeErr(w, http.StatusInternalServerError, "lease failed")
		return
	}

	if msgs == nil {
		msgs = []LeasedMsg{}
	}

	slog.Info("leased",
		"topic", req.Topic,
		"consumer_id", req.ConsumerID,
		"leased", len(msgs),
		"requested_max", req.Max,
		"latency_ms", time.Since(start).Milliseconds(),
	)

	writeJSON(w, http.StatusOK, LeaseResponse{
		LeaseID:  leaseID,
		Messages: msgs,
	})
}

// ---------------------------------------------------------------------------
// POST /v1/ack
// ---------------------------------------------------------------------------

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
	if req.LeaseID == uuid.Nil {
		writeErr(w, http.StatusBadRequest, "lease_id is required")
		return
	}

	acked, err := h.store.Ack(r.Context(), req.Topic, req.ConsumerID, req.LeaseID)
	if err != nil {
		slog.Error("ack failed",
			"topic", req.Topic,
			"consumer_id", req.ConsumerID,
			"lease_id", req.LeaseID,
			"error", err,
		)
		writeErr(w, http.StatusInternalServerError, "ack failed")
		return
	}

	slog.Info("acked",
		"topic", req.Topic,
		"consumer_id", req.ConsumerID,
		"lease_id", req.LeaseID,
		"acked", acked,
		"latency_ms", time.Since(start).Milliseconds(),
	)

	writeJSON(w, http.StatusOK, AckResponse{Acked: acked})
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
