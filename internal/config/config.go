// Package config provides environment-based configuration loading
// for all services in the monorepo.
package config

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"
)

// Base holds configuration common to every service.
type Base struct {
	Port        int
	LogLevel    string
	DatabaseURL string
}

// MQ holds configuration for the message-queue service.
type MQ struct {
	Base
}

// Streamer holds configuration for the streamer service.
type Streamer struct {
	Base
}

// Collector holds configuration for the collector service.
type Collector struct {
	Base
	CollectInterval time.Duration
}

// APIGW holds configuration for the API gateway service.
type APIGW struct {
	Base
	UpstreamTimeout time.Duration
}

// LoadBase reads the common configuration from environment variables.
func LoadBase(defaultPort int) Base {
	return Base{
		Port:        GetEnvInt("PORT", defaultPort),
		LogLevel:    GetEnv("LOG_LEVEL", "info"),
		DatabaseURL: GetEnv("DATABASE_URL", "postgres://prompted:prompted@localhost:5432/prompted?sslmode=disable"),
	}
}

// LoadMQ returns the MQ service configuration.
func LoadMQ() MQ {
	return MQ{Base: LoadBase(8081)}
}

// LoadStreamer returns the Streamer service configuration.
func LoadStreamer() Streamer {
	return Streamer{Base: LoadBase(8082)}
}

// LoadCollector returns the Collector service configuration.
func LoadCollector() Collector {
	return Collector{
		Base:            LoadBase(8083),
		CollectInterval: GetEnvDuration("COLLECT_INTERVAL", 30*time.Second),
	}
}

// LoadAPIGW returns the API Gateway service configuration.
func LoadAPIGW() APIGW {
	return APIGW{
		Base:            LoadBase(8080),
		UpstreamTimeout: GetEnvDuration("UPSTREAM_TIMEOUT", 10*time.Second),
	}
}

// LogLevel parses the configured log level string into an slog.Level.
func (b Base) SlogLevel() slog.Level {
	switch b.LogLevel {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// Addr returns the listen address as ":PORT".
func (b Base) Addr() string {
	return fmt.Sprintf(":%d", b.Port)
}

// ---------------------------------------------------------------------------
// Env helpers
// ---------------------------------------------------------------------------

// GetEnv returns the value of the environment variable or fallback.
func GetEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// GetEnvInt returns the integer value of the environment variable or fallback.
func GetEnvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return fallback
}

// GetEnvDuration returns the duration value of the environment variable or fallback.
// The env value is parsed via time.ParseDuration (e.g. "30s", "5m").
func GetEnvDuration(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}
