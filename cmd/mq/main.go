// Service mq is a lightweight message-queue broker backed by PostgreSQL.
// It exposes HTTP endpoints for publishing, leasing, and acknowledging
// messages and provides health/readiness probes.
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
	"github.com/prompted/prompted/internal/db"
	"github.com/prompted/prompted/internal/models"
	"github.com/prompted/prompted/internal/mq"
)

func main() {
	cfg := config.LoadMQ()

	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: cfg.SlogLevel(),
	})))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		slog.Error("failed to connect to database", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	store := mq.NewStore(pool)
	buf := mq.NewWriteBuffer(store, cfg.BufferSize, cfg.FlushInterval)

	// Start the write buffer's flusher goroutine.
	bufDone := make(chan struct{})
	go func() {
		defer close(bufDone)
		buf.Run()
	}()

	h := mq.NewHandler(store, buf)

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(30 * time.Second))

	// Health probes
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, models.HealthResponse{Status: "ok", Service: "mq"})
	})
	r.Get("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := db.Healthy(r.Context(), pool); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, models.HealthResponse{Status: "unavailable", Service: "mq"})
			return
		}
		writeJSON(w, http.StatusOK, models.HealthResponse{Status: "ready", Service: "mq"})
	})

	// MQ API
	r.Route("/v1", func(r chi.Router) {
		r.Post("/publish", h.Publish)
		r.Post("/lease", h.Lease)
		r.Post("/ack", h.Ack)
	})

	serve(cfg.Base, r, buf, bufDone)
}

func serve(cfg config.Base, handler http.Handler, buf *mq.WriteBuffer, bufDone <-chan struct{}) {
	srv := &http.Server{
		Addr:         cfg.Addr(),
		Handler:      handler,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("mq listening", "addr", srv.Addr)
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

	// Gracefully shut down the HTTP server first (stop accepting new requests).
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		slog.Error("shutdown error", "error", err)
	}

	// Close the write buffer and wait for it to flush remaining messages.
	slog.Info("draining write buffer")
	buf.Close()
	<-bufDone
	slog.Info("write buffer drained")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
