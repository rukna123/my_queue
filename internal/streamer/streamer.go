package streamer

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
// Types mirroring the MQ writer publish API (local to this package).
// Messages are raw CSV row objects — no msg_uuid wrapper.
// ---------------------------------------------------------------------------

type telemetryPayload struct {
	Timestamp  time.Time `json:"timestamp"`
	MetricName string    `json:"metric_name"`
	GPUID      string    `json:"gpu_id"`
	Device     string    `json:"device"`
	UUID       string    `json:"uuid"`
	ModelName  string    `json:"model_name"`
	Hostname   string    `json:"hostname"`
	Container  string    `json:"container"`
	Pod        string    `json:"pod"`
	Namespace  string    `json:"namespace"`
	Value      float64   `json:"value"`
	LabelsRaw  string    `json:"labels_raw"`
}

type mqPublishRequest struct {
	Topic    string            `json:"topic"`
	Messages []json.RawMessage `json:"messages"`
}

type mqPublishResponse struct {
	Accepted   int `json:"accepted"`
	Duplicates int `json:"duplicates"`
}

// ---------------------------------------------------------------------------
// Run – main entry point for the streaming loop
// ---------------------------------------------------------------------------

// Run loads the CSV and starts the reader → channel → sender pipeline.
// It blocks until ctx is cancelled.
func Run(ctx context.Context, cfg config.Streamer, client *httpx.Client) {
	rows, err := ReadCSV(cfg.CSVPath)
	if err != nil {
		slog.Error("failed to load CSV", "error", err, "path", cfg.CSVPath)
		return
	}
	slog.Info("csv loaded",
		"rows", len(rows),
		"path", cfg.CSVPath,
		"interval_ms", cfg.IntervalMS,
		"mq_url", cfg.MQBaseURL,
	)

	// Buffered channel: the reader never blocks waiting for the sender to
	// finish an HTTP call. The MQ service handles backpressure internally
	// via its write buffer.
	msgCh := make(chan TelemetryRow, cfg.ChannelBuffer)

	// Sender goroutine – owns the HTTP client.
	doneCh := make(chan struct{})
	go func() {
		defer close(doneCh)
		sender(ctx, client, cfg.MQBaseURL, msgCh)
	}()

	// Reader loop – owns the CSV data and timing, runs on caller goroutine.
	readLoop(ctx, rows, cfg.IntervalMS, cfg.CSVPath, msgCh)

	// readLoop returned (ctx cancelled) – close channel so sender drains.
	close(msgCh)
	<-doneCh
}

// ---------------------------------------------------------------------------
// Reader goroutine (runs on the calling goroutine of Run)
// ---------------------------------------------------------------------------

// readLoop iterates over rows one at a time, sending each as a single message
// on out. It sleeps IntervalMS between messages.
//
// For every row it:
//  1. Stamps time.Now().UTC() on the in-memory rows slice entry.
//  2. Attempts to write the CSV back to disk (best-effort; skipped silently
//     when the filesystem is read-only, e.g. ConfigMap mount in Kubernetes).
//  3. Copies the row and sends it through the channel to the sender.
func readLoop(ctx context.Context, rows []TelemetryRow, intervalMS int, csvPath string, out chan<- TelemetryRow) {
	interval := time.Duration(intervalMS) * time.Millisecond
	diskWritable := true

	for {
		for i := range rows {
			rows[i].Timestamp = time.Now().UTC()

			if diskWritable {
				if err := WriteCSV(csvPath, rows); err != nil {
					slog.Warn("csv disk write failed, continuing in-memory only", "error", err)
					diskWritable = false
				}
			}

			// Copy the row so the sender owns its data.
			row := rows[i]

			select {
			case out <- row:
			case <-ctx.Done():
				return
			}

			select {
			case <-time.After(interval):
			case <-ctx.Done():
				return
			}
		}
		slog.Info("csv loop completed, restarting from beginning")
	}
}

// ---------------------------------------------------------------------------
// Sender goroutine
// ---------------------------------------------------------------------------

// sender receives individual rows from in and publishes each as a single
// message to the MQ.
func sender(ctx context.Context, client *httpx.Client, mqBaseURL string, in <-chan TelemetryRow) {
	publishURL := mqBaseURL + "/v1/publish"

	for row := range in {
		publishMessage(ctx, client, publishURL, row)
	}
}

// publishMessage builds an MQ publish request containing a single raw CSV row
// and sends it via the httpx client (which handles retries/backoff internally).
// The payload's uuid field is the original CSV value; the mqwriter generates
// the authoritative msg_uuid for queue-level and telemetry-level dedup.
func publishMessage(ctx context.Context, client *httpx.Client, url string, row TelemetryRow) {
	start := time.Now()

	rowJSON, err := json.Marshal(telemetryPayload{
		Timestamp:  row.Timestamp,
		MetricName: row.MetricName,
		GPUID:      row.GPUID,
		Device:     row.Device,
		UUID:       row.UUID,
		ModelName:  row.ModelName,
		Hostname:   row.Hostname,
		Container:  row.Container,
		Pod:        row.Pod,
		Namespace:  row.Namespace,
		Value:      row.Value,
		LabelsRaw:  row.LabelsRaw,
	})
	if err != nil {
		slog.Error("marshal row", "uuid", row.UUID, "error", err)
		return
	}

	reqBody := mqPublishRequest{
		Topic:    "telemetry",
		Messages: []json.RawMessage{rowJSON},
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		slog.Error("marshal publish request", "error", err)
		return
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		slog.Error("create HTTP request", "error", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(ctx, req)
	if err != nil {
		slog.Error("publish to MQ failed",
			"error", err,
			"uuid", row.UUID,
			"latency_ms", time.Since(start).Milliseconds(),
		)
		return
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		slog.Error("MQ returned non-200",
			"status", resp.StatusCode,
			"uuid", row.UUID,
		)
		return
	}

	var result mqPublishResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		slog.Error("decode MQ response", "error", err)
		return
	}

	slog.Info("message published",
		"uuid", row.UUID,
		"accepted", result.Accepted,
		"duplicates", result.Duplicates,
		"latency_ms", time.Since(start).Milliseconds(),
	)
}

// Healthy returns nil when the MQ service is reachable.
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
