// Package models contains shared domain structs used across services.
package models

import (
	"time"

	"github.com/google/uuid"
)

// HealthResponse is returned by /healthz and /readyz endpoints.
type HealthResponse struct {
	Status  string `json:"status"`
	Service string `json:"service"`
}

// ---------------------------------------------------------------------------
// Queue messages
// ---------------------------------------------------------------------------

// MessageState represents the lifecycle state of a queue message.
type MessageState string

const (
	MessageStateReady    MessageState = "ready"
	MessageStateInFlight MessageState = "in_flight"
	MessageStateAcked    MessageState = "acked"
)

// QueueMessage maps to the queue_messages table.
type QueueMessage struct {
	ID           int64        `json:"id"            db:"id"`
	Topic        string       `json:"topic"         db:"topic"`
	MsgUUID      uuid.UUID    `json:"msg_uuid"      db:"msg_uuid"`
	Payload      []byte       `json:"payload"       db:"payload"`
	State        MessageState `json:"state"         db:"state"`
	LeasedBy     *string      `json:"leased_by"     db:"leased_by"`
	LeaseID      *uuid.UUID   `json:"lease_id"      db:"lease_id"`
	LeaseUntil   *time.Time   `json:"lease_until"   db:"lease_until"`
	Attempts     int          `json:"attempts"      db:"attempts"`
	PartitionKey int          `json:"partition_key" db:"partition_key"`
	CreatedAt    time.Time    `json:"created_at"    db:"created_at"`
	UpdatedAt    time.Time    `json:"updated_at"    db:"updated_at"`
}

// ---------------------------------------------------------------------------
// Telemetry
// ---------------------------------------------------------------------------

// Telemetry maps to the telemetry table — a persisted GPU telemetry entry.
type Telemetry struct {
	ID         int64     `json:"id"          db:"id"`
	UUID       uuid.UUID `json:"uuid"        db:"uuid"`
	GPUID      string    `json:"gpu_id"      db:"gpu_id"`
	MetricName string    `json:"metric_name" db:"metric_name"`
	Timestamp  time.Time `json:"timestamp"   db:"timestamp"`
	ModelName  *string   `json:"model_name"  db:"model_name"`
	Container  *string   `json:"container"   db:"container"`
	Pod        *string   `json:"pod"         db:"pod"`
	Namespace  *string   `json:"namespace"   db:"namespace"`
	Value      *float64  `json:"value"       db:"value"`
	LabelsRaw  *string   `json:"labels_raw"  db:"labels_raw"`
	IngestedAt time.Time `json:"ingested_at" db:"ingested_at"`
}
