// Package models contains shared domain structs used across services.
package models

import "time"

// HealthResponse is returned by /healthz and /readyz endpoints.
type HealthResponse struct {
	Status  string `json:"status"`
	Service string `json:"service"`
}

// Message represents a message in the queue.
type Message struct {
	ID        string    `json:"id"`
	Topic     string    `json:"topic"`
	Payload   []byte    `json:"payload"`
	CreatedAt time.Time `json:"created_at"`
}

// Event represents a collected event.
type Event struct {
	ID        string    `json:"id"`
	Source    string    `json:"source"`
	Type      string    `json:"type"`
	Data      []byte    `json:"data"`
	Timestamp time.Time `json:"timestamp"`
}

// Stream represents a data stream.
type Stream struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}
