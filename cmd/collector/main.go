// Service collector leases messages from the MQ reader, persists telemetry
// data to PostgreSQL, and acknowledges the lease on success.
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

	"github.com/prompted/prompted/internal/collector"
	"github.com/prompted/prompted/internal/config"
	"github.com/prompted/prompted/internal/db"
	"github.com/prompted/prompted/internal/httpx"
	"github.com/prompted/prompted/internal/models"
)

func main() {
	cfg := config.LoadCollector()

	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: cfg.SlogLevel(),
	})))

	connCtx, connCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer connCancel()

	pool, err := db.Connect(connCtx, cfg.DatabaseURL)
	if err != nil {
		slog.Error("failed to connect to database", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	store := collector.NewStore(pool)
	client := httpx.NewClient(10*time.Second, 2)
	mqClient := collector.NewMQClient(client, cfg.MQBaseURL)

	// Start the collector loop in an owner goroutine.
	collectCtx, collectCancel := context.WithCancel(context.Background())
	defer collectCancel()
	go collector.Run(collectCtx, cfg, store, mqClient)

	// HTTP server for health probes.
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(30 * time.Second))

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, models.HealthResponse{Status: "ok", Service: "collector"})
	})

	r.Get("/readyz", func(w http.ResponseWriter, r *http.Request) {
		// Check DB.
		if err := db.Healthy(r.Context(), pool); err != nil {
			writeJSON(w, http.StatusServiceUnavailable,
				models.HealthResponse{Status: "unavailable", Service: "collector"})
			return
		}
		// Check MQ reader reachability.
		if err := collector.Healthy(r.Context(), client, cfg.MQBaseURL); err != nil {
			writeJSON(w, http.StatusServiceUnavailable,
				models.HealthResponse{Status: "unavailable", Service: "collector"})
			return
		}
		writeJSON(w, http.StatusOK, models.HealthResponse{Status: "ready", Service: "collector"})
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
		slog.Info("collector listening", "addr", srv.Addr)
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

	collectCancel()

	shutCtx, shutCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutCancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		slog.Error("shutdown error", "error", err)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
