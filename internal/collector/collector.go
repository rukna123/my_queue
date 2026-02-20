package collector

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/prompted/prompted/internal/config"
	"github.com/prompted/prompted/internal/httpx"
)

// ---------------------------------------------------------------------------
// MQ API types (local mirrors)
// ---------------------------------------------------------------------------

type leaseRequest struct {
	Topic        string `json:"topic"`
	ConsumerID   string `json:"consumer_id"`
	Max          int    `json:"max"`
	LeaseSeconds int    `json:"lease_seconds"`
}

// LeaseMessage is a single message in the lease response, carrying the
// mqwriter-generated msg_uuid alongside the raw payload.
type LeaseMessage struct {
	MsgUUID string          `json:"msg_uuid"`
	Payload json.RawMessage `json:"payload"`
}

// LeaseResponse is the result of a lease call to the MQ reader.
type LeaseResponse struct {
	LeaseID  string         `json:"lease_id"`
	Messages []LeaseMessage `json:"messages"`
}

type ackRequest struct {
	Topic      string `json:"topic"`
	ConsumerID string `json:"consumer_id"`
	LeaseID    string `json:"lease_id"`
}

// AckResponse is the result of an ack call to the MQ reader.
type AckResponse struct {
	Acked int64 `json:"acked"`
}

// MQClient abstracts the HTTP calls to the MQ reader so the collector loop
// can be tested with a mock.
type MQClient interface {
	Lease(ctx context.Context, topic, consumerID string, max, leaseSeconds int) (*LeaseResponse, error)
	Ack(ctx context.Context, topic, consumerID, leaseID string) (*AckResponse, error)
}

// ---------------------------------------------------------------------------
// httpMQClient – real implementation using httpx
// ---------------------------------------------------------------------------

type httpMQClient struct {
	client *httpx.Client
	base   string
}

// NewMQClient creates an MQClient that talks to the MQ reader over HTTP.
func NewMQClient(client *httpx.Client, baseURL string) MQClient {
	return &httpMQClient{client: client, base: baseURL}
}

func (c *httpMQClient) Lease(ctx context.Context, topic, consumerID string, max, leaseSeconds int) (*LeaseResponse, error) {
	body, err := json.Marshal(leaseRequest{
		Topic:        topic,
		ConsumerID:   consumerID,
		Max:          max,
		LeaseSeconds: leaseSeconds,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal lease request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/v1/lease", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("lease HTTP call: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("lease: status %d", resp.StatusCode)
	}

	var result LeaseResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode lease response: %w", err)
	}
	return &result, nil
}

func (c *httpMQClient) Ack(ctx context.Context, topic, consumerID, leaseID string) (*AckResponse, error) {
	body, err := json.Marshal(ackRequest{
		Topic:      topic,
		ConsumerID: consumerID,
		LeaseID:    leaseID,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal ack request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/v1/ack", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("ack HTTP call: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ack: status %d", resp.StatusCode)
	}

	var result AckResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode ack response: %w", err)
	}
	return &result, nil
}

// ---------------------------------------------------------------------------
// Run – owner goroutine for the collect loop
// ---------------------------------------------------------------------------

// Run starts the collector's main loop.  It leases messages from the MQ
// reader, persists them to the telemetry table, and acks the lease on
// success.  If persistence fails, the lease is NOT acked — messages will
// be redelivered after the lease expires.
//
// Run blocks until ctx is cancelled.
func Run(ctx context.Context, cfg config.Collector, store Persister, mq MQClient) {
	interval := time.Duration(cfg.PollIntervalMS) * time.Millisecond
	topic := "telemetry"

	slog.Info("collector started",
		"collector_id", cfg.CollectorID,
		"mq_url", cfg.MQBaseURL,
		"lease_max", cfg.LeaseMax,
		"lease_seconds", cfg.LeaseSeconds,
		"poll_interval_ms", cfg.PollIntervalMS,
	)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("collector loop stopped")
			return
		case <-ticker.C:
			processOnce(ctx, cfg, store, mq, topic)
		}
	}
}

// processOnce runs a single lease → persist → ack cycle.
func processOnce(ctx context.Context, cfg config.Collector, store Persister, mq MQClient, topic string) {
	// 1. Lease messages from the MQ reader.
	leaseResp, err := mq.Lease(ctx, topic, cfg.CollectorID, cfg.LeaseMax, cfg.LeaseSeconds)
	if err != nil {
		slog.Error("lease failed", "error", err)
		return
	}

	if len(leaseResp.Messages) == 0 {
		slog.Debug("no messages to collect")
		return
	}

	slog.Info("leased messages",
		"lease_id", leaseResp.LeaseID,
		"count", len(leaseResp.Messages),
	)

	// 2. Parse payloads into telemetry rows, using msg_uuid as the dedup key.
	rows := make([]TelemetryRow, 0, len(leaseResp.Messages))
	for i, msg := range leaseResp.Messages {
		var row TelemetryRow
		if err := json.Unmarshal(msg.Payload, &row); err != nil {
			slog.Error("parse message payload",
				"index", i,
				"msg_uuid", msg.MsgUUID,
				"lease_id", leaseResp.LeaseID,
				"error", err,
			)
			// Skip unparseable messages but continue with the rest.
			continue
		}
		// Set MsgUUID from the mqwriter-generated dedup key.
		// row.UUID retains the original GPU UUID from the CSV payload.
		row.MsgUUID = msg.MsgUUID
		rows = append(rows, row)
	}

	if len(rows) == 0 {
		slog.Warn("all messages in lease were unparseable, skipping ack",
			"lease_id", leaseResp.LeaseID,
		)
		return
	}

	// 3. Persist to the telemetry table.
	persisted, dupes, err := store.PersistBatch(ctx, rows)
	if err != nil {
		// DO NOT ack — let the lease expire so messages are redelivered.
		slog.Error("persist failed, NOT acking lease",
			"lease_id", leaseResp.LeaseID,
			"error", err,
		)
		return
	}

	slog.Info("persisted telemetry",
		"lease_id", leaseResp.LeaseID,
		"persisted", persisted,
		"duplicates", dupes,
	)

	// 4. Ack the lease — all messages are safely in the telemetry table.
	ackResp, err := mq.Ack(ctx, topic, cfg.CollectorID, leaseResp.LeaseID)
	if err != nil {
		slog.Error("ack failed (messages are persisted, will be deduped on retry)",
			"lease_id", leaseResp.LeaseID,
			"error", err,
		)
		return
	}

	slog.Info("acked lease",
		"lease_id", leaseResp.LeaseID,
		"acked", ackResp.Acked,
	)
}

// Healthy returns nil when the MQ reader is reachable.
func Healthy(ctx context.Context, client *httpx.Client, mqBaseURL string) error {
	resp, err := client.Get(ctx, mqBaseURL+"/healthz")
	if err != nil {
		return fmt.Errorf("mq healthz: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("mq healthz: status %d", resp.StatusCode)
	}
	return nil
}
