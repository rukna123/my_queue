// Service mqwriter is the write-side of the message queue.  It receives
// published messages via HTTP, buffers them in memory, and flushes to
// PostgreSQL asynchronously.
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
	"github.com/prompted/prompted/internal/mqwriter"
)

func main() {
	cfg := config.LoadMQWriter()

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

	store := mqwriter.NewStore(pool, cfg.PartitionCount)
	buf := mqwriter.NewWriteBuffer(store, cfg.BufferSize, cfg.FlushInterval)

	bufDone := make(chan struct{})
	go func() {
		defer close(bufDone)
		buf.Run()
	}()

	h := mqwriter.NewHandler(buf, cfg.PartitionCount)

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(30 * time.Second))

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, models.HealthResponse{Status: "ok", Service: "mqwriter"})
	})
	r.Get("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := db.Healthy(r.Context(), pool); err != nil {
			writeJSON(w, http.StatusServiceUnavailable,
				models.HealthResponse{Status: "unavailable", Service: "mqwriter"})
			return
		}
		writeJSON(w, http.StatusOK, models.HealthResponse{Status: "ready", Service: "mqwriter"})
	})

	r.Post("/v1/publish", h.Publish)
	r.Get("/metrics/buffer", h.MetricsHandler)

	srv := &http.Server{
		Addr:         cfg.Addr(),
		Handler:      r,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("mqwriter listening", "addr", srv.Addr, "partitions", cfg.PartitionCount)
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

	shutCtx, shutCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutCancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		slog.Error("shutdown error", "error", err)
	}

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
