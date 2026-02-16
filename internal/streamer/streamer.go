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
// Types mirroring the MQ publish API (local to this package)
// ---------------------------------------------------------------------------

type telemetryPayload struct {
	Timestamp  time.Time `json:"timestamp"`
	MetricName string    `json:"metric_name"`
	GPUID      string    `json:"gpu_id"`
	UUID       string    `json:"uuid"`
	ModelName  string    `json:"model_name"`
	Container  string    `json:"container"`
	Pod        string    `json:"pod"`
	Namespace  string    `json:"namespace"`
	Value      float64   `json:"value"`
	LabelsRaw  string    `json:"labels_raw"`
}

type mqMessage struct {
	MsgUUID string          `json:"msg_uuid"`
	Payload json.RawMessage `json:"payload"`
}

type mqPublishRequest struct {
	Topic    string      `json:"topic"`
	Messages []mqMessage `json:"messages"`
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
		"batch_size", cfg.BatchSize,
		"interval_ms", cfg.IntervalMS,
		"mq_url", cfg.MQBaseURL,
	)

	// Buffered channel: the reader never blocks waiting for the sender to
	// finish an HTTP call. The MQ service handles backpressure internally
	// via its write buffer.
	batchCh := make(chan []TelemetryRow, cfg.ChannelBuffer)

	// Sender goroutine – owns the HTTP client.
	doneCh := make(chan struct{})
	go func() {
		defer close(doneCh)
		sender(ctx, client, cfg.MQBaseURL, batchCh)
	}()

	// Reader loop – owns the CSV data and timing, runs on caller goroutine.
	readLoop(ctx, rows, cfg.BatchSize, cfg.IntervalMS, cfg.CSVPath, batchCh)

	// readLoop returned (ctx cancelled) – close channel so sender drains.
	close(batchCh)
	<-doneCh
}

// ---------------------------------------------------------------------------
// Reader goroutine (runs on the calling goroutine of Run)
// ---------------------------------------------------------------------------

// readLoop iterates over rows continuously, slicing them into batches and
// pushing each batch onto out. It sleeps IntervalMS between batches.
//
// For every batch it:
//  1. Stamps time.Now().UTC() on the original rows slice.
//  2. Writes the full CSV back to disk so the file reflects the last-processed
//     timestamp for every row.
//  3. Copies the batch and sends it through the channel to the sender.
func readLoop(ctx context.Context, rows []TelemetryRow, batchSize, intervalMS int, csvPath string, out chan<- []TelemetryRow) {
	interval := time.Duration(intervalMS) * time.Millisecond

	for {
		for i := 0; i < len(rows); i += batchSize {
			end := i + batchSize
			if end > len(rows) {
				end = len(rows)
			}

			// Stamp the processing time on the original rows.
			now := time.Now().UTC()
			for j := i; j < end; j++ {
				rows[j].Timestamp = now
			}

			// Persist the updated timestamps to the CSV on disk.
			if err := WriteCSV(csvPath, rows); err != nil {
				slog.Error("failed to write CSV back to disk", "error", err)
			}

			// Copy the slice so the sender owns its data.
			batch := make([]TelemetryRow, end-i)
			copy(batch, rows[i:end])

			select {
			case out <- batch:
			case <-ctx.Done():
				return
			}

			// Delay between batches.
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

// sender receives batches from in and publishes them to the MQ.
func sender(ctx context.Context, client *httpx.Client, mqBaseURL string, in <-chan []TelemetryRow) {
	publishURL := mqBaseURL + "/v1/publish"

	for batch := range in {
		publishBatch(ctx, client, publishURL, batch)
	}
}

// publishBatch builds the MQ publish request and sends it via the httpx
// client (which handles retries/backoff internally).
func publishBatch(ctx context.Context, client *httpx.Client, url string, rows []TelemetryRow) {
	start := time.Now()

	msgs := make([]mqMessage, 0, len(rows))
	for _, row := range rows {
		payload := telemetryPayload{
			Timestamp:  row.Timestamp, // stamped in readLoop when the row was processed
			MetricName: row.MetricName,
			GPUID:      row.GPUID,
			UUID:       row.UUID,
			ModelName:  row.ModelName,
			Container:  row.Container,
			Pod:        row.Pod,
			Namespace:  row.Namespace,
			Value:      row.Value,
			LabelsRaw:  row.LabelsRaw,
		}

		payloadBytes, err := json.Marshal(payload)
		if err != nil {
			slog.Error("marshal payload", "uuid", row.UUID, "error", err)
			continue
		}

		msgs = append(msgs, mqMessage{
			MsgUUID: row.UUID,
			Payload: json.RawMessage(payloadBytes),
		})
	}

	if len(msgs) == 0 {
		return
	}

	reqBody := mqPublishRequest{
		Topic:    "telemetry",
		Messages: msgs,
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
			"batch_size", len(msgs),
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
			"batch_size", len(msgs),
		)
		return
	}

	var result mqPublishResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		slog.Error("decode MQ response", "error", err)
		return
	}

	slog.Info("batch published",
		"accepted", result.Accepted,
		"duplicates", result.Duplicates,
		"batch_size", len(msgs),
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
