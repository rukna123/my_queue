// Service apigw is the API gateway – the public entry-point for external
// clients.  It exposes GPU telemetry queries and Swagger documentation.
//
//	@title			Prompted API
//	@version		1.0
//	@description	GPU telemetry API gateway.
//	@host			localhost:8080
//	@BasePath		/
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
	httpSwagger "github.com/swaggo/http-swagger/v2"

	"github.com/prompted/prompted/internal/apigw"
	"github.com/prompted/prompted/internal/config"
	"github.com/prompted/prompted/internal/db"
	"github.com/prompted/prompted/internal/models"

	_ "github.com/prompted/prompted/docs/swagger" // generated swagger docs
)

func main() {
	cfg := config.LoadAPIGW()

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

	store := apigw.NewStore(pool)
	handler := apigw.NewHandler(store)

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(30 * time.Second))

	// Health probes.
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, models.HealthResponse{Status: "ok", Service: "apigw"})
	})
	r.Get("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := db.Healthy(r.Context(), pool); err != nil {
			writeJSON(w, http.StatusServiceUnavailable,
				models.HealthResponse{Status: "unavailable", Service: "apigw"})
			return
		}
		writeJSON(w, http.StatusOK, models.HealthResponse{Status: "ready", Service: "apigw"})
	})

	// API routes.
	r.Get("/api/v1/gpus", handler.ListGPUs)
	r.Get("/api/v1/gpus/{id}/telemetry", handler.GetTelemetry)

	// Swagger UI.
	r.Get("/swagger/*", httpSwagger.Handler(
		httpSwagger.URL("/swagger/doc.json"),
	))

	serve(cfg.Base, r)
}

func serve(cfg config.Base, handler http.Handler) {
	srv := &http.Server{
		Addr:         cfg.Addr(),
		Handler:      handler,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("apigw listening", "addr", srv.Addr)
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

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		slog.Error("shutdown error", "error", err)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
