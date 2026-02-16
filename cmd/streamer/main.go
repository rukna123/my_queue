// Service streamer reads telemetry rows from a CSV file and publishes them
// to the MQ service in batches. It loops continuously, overwriting the
// timestamp with time.Now().UTC() on each publish.
package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/prompted/prompted/internal/config"
	"github.com/prompted/prompted/internal/httpx"
	"github.com/prompted/prompted/internal/models"
	"github.com/prompted/prompted/internal/streamer"
)

func main() {
	cfg := config.LoadStreamer()

	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: cfg.SlogLevel(),
	})))

	// Low retry count (1) so the sender goroutine doesn't stall the pipeline.
	// Backpressure is handled by the MQ's write buffer, not the streamer.
	client := httpx.NewClient(10*time.Second, 1)

	// Root context cancelled on SIGINT/SIGTERM.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start the streaming pipeline in a background goroutine.
	go streamer.Run(ctx, cfg, client)

	// HTTP server for health probes.
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(10 * time.Second))

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, models.HealthResponse{Status: "ok", Service: "streamer"})
	})

	r.Get("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := streamer.Healthy(r.Context(), client, cfg.MQBaseURL); err != nil {
			writeJSON(w, http.StatusServiceUnavailable,
				models.HealthResponse{Status: "unavailable", Service: "streamer"})
			return
		}
		writeJSON(w, http.StatusOK, models.HealthResponse{Status: "ready", Service: "streamer"})
	})

	srv := &http.Server{
		Addr:         cfg.Addr(),
		Handler:      r,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  30 * time.Second,
	}

	// Start HTTP server.
	errCh := make(chan error, 1)
	go func() {
		slog.Info("streamer listening", "addr", srv.Addr)
		errCh <- srv.ListenAndServe()
	}()

	// Wait for signal or server error.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-sigCh:
		slog.Info("shutting down", "signal", sig)
	case err := <-errCh:
		slog.Error("server error", "error", err)
	}

	// Cancel the streaming context and shut down HTTP server.
	cancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("http shutdown error", "error", err)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
