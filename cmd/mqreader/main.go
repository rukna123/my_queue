// Service mqreader is the read-side of the message queue.  Each pod owns a
// range of virtual partitions, loads messages into memory, and serves
// lease/ack requests from its in-memory partition store.
package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"gopkg.in/yaml.v2"

	"github.com/prompted/prompted/internal/config"
	"github.com/prompted/prompted/internal/db"
	"github.com/prompted/prompted/internal/models"
	"github.com/prompted/prompted/internal/mqreader"
)

const partitionFile = "/etc/mqreader/partitions.yaml"

func main() {
	cfg := config.LoadMQReader()

	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: cfg.SlogLevel(),
	})))

	// Try loading partition range from the ConfigMap-mounted file.
	// Falls back to env-based READER_PARTITION_START / READER_PARTITION_END.
	partStart, partEnd := loadPartitionRange(cfg.PartitionStart, cfg.PartitionEnd)
	cfg.PartitionStart = partStart
	cfg.PartitionEnd = partEnd

	connCtx, connCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer connCancel()

	pool, err := db.Connect(connCtx, cfg.DatabaseURL)
	if err != nil {
		slog.Error("failed to connect to database", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	store := mqreader.NewStore(pool)
	part := mqreader.NewPartition(store, cfg.PartitionStart, cfg.PartitionEnd, cfg.PollInterval, cfg.PollBatch)

	// Root context for the partition goroutine.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go part.Run(ctx)

	h := mqreader.NewHandler(part)

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(30 * time.Second))

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, models.HealthResponse{Status: "ok", Service: "mqreader"})
	})
	r.Get("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := db.Healthy(r.Context(), pool); err != nil {
			writeJSON(w, http.StatusServiceUnavailable,
				models.HealthResponse{Status: "unavailable", Service: "mqreader"})
			return
		}
		writeJSON(w, http.StatusOK, models.HealthResponse{Status: "ready", Service: "mqreader"})
	})

	r.Route("/v1", func(r chi.Router) {
		r.Post("/lease", h.Lease)
		r.Post("/ack", h.Ack)
	})

	srv := &http.Server{
		Addr:         cfg.Addr(),
		Handler:      r,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("mqreader listening",
			"addr", srv.Addr,
			"partition_start", cfg.PartitionStart,
			"partition_end", cfg.PartitionEnd,
		)
		errCh <- srv.ListenAndServe()
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-sigCh:
		slog.Info("shutting down", "signal", sig)
	case err := <-errCh:
		slog.Error("server error", "error", err)
	}

	// Stop accepting requests.
	shutCtx, shutCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutCancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		slog.Error("shutdown error", "error", err)
	}

	// Stop the partition goroutine.
	cancel()
	slog.Info("mqreader stopped")
}

// loadPartitionRange reads the partition assignment from the ConfigMap-mounted
// file at /etc/mqreader/partitions.yaml.  If the file doesn't exist (e.g.
// running locally or in docker-compose), it falls back to the env-derived
// defaults.
func loadPartitionRange(envStart, envEnd int) (int, int) {
	data, err := os.ReadFile(partitionFile)
	if err != nil {
		slog.Info("partition file not found, using env vars",
			"path", partitionFile,
			"start", envStart,
			"end", envEnd,
		)
		return envStart, envEnd
	}

	podName := os.Getenv("POD_NAME")
	if podName == "" {
		podName, _ = os.Hostname()
	}

	var partitions map[string]string
	if err := yaml.Unmarshal(data, &partitions); err != nil {
		slog.Error("failed to parse partition file, falling back to env vars",
			"path", partitionFile,
			"error", err,
		)
		return envStart, envEnd
	}

	rangeStr, ok := partitions[podName]
	if !ok {
		slog.Error("pod not found in partition map, falling back to env vars",
			"pod_name", podName,
			"available", partitions,
		)
		return envStart, envEnd
	}

	parts := strings.SplitN(rangeStr, "-", 2)
	if len(parts) != 2 {
		slog.Error("invalid partition range format", "pod_name", podName, "range", rangeStr)
		return envStart, envEnd
	}

	start, err1 := strconv.Atoi(parts[0])
	end, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		slog.Error("invalid partition range values", "pod_name", podName, "range", rangeStr)
		return envStart, envEnd
	}

	slog.Info("loaded partition range from file",
		"path", partitionFile,
		"pod_name", podName,
		"start", start,
		"end", end,
	)
	return start, end
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
