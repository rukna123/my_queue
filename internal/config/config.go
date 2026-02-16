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

// MQWriter holds configuration for the writer microservice.
type MQWriter struct {
	Base
	PartitionCount int
	BufferSize     int
	FlushInterval  time.Duration
}

// MQReader holds configuration for the reader microservice.
type MQReader struct {
	Base
	PartitionStart int
	PartitionEnd   int
	PollInterval   time.Duration
	PollBatch      int
}

// Streamer holds configuration for the streamer service.
type Streamer struct {
	Base
	CSVPath       string
	IntervalMS    int
	BatchSize     int
	MQBaseURL     string
	ChannelBuffer int
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

// LoadMQWriter returns the MQ Writer service configuration.
func LoadMQWriter() MQWriter {
	return MQWriter{
		Base:           LoadBase(8084),
		PartitionCount: GetEnvInt("MQ_PARTITION_COUNT", 256),
		BufferSize:     GetEnvInt("MQ_BUFFER_SIZE", 500),
		FlushInterval:  GetEnvDuration("MQ_FLUSH_INTERVAL", 500*time.Millisecond),
	}
}

// LoadMQReader returns the MQ Reader service configuration.
func LoadMQReader() MQReader {
	return MQReader{
		Base:           LoadBase(8085),
		PartitionStart: GetEnvInt("READER_PARTITION_START", 0),
		PartitionEnd:   GetEnvInt("READER_PARTITION_END", 255),
		PollInterval:   GetEnvDuration("READER_POLL_INTERVAL", 500*time.Millisecond),
		PollBatch:      GetEnvInt("READER_POLL_BATCH", 1000),
	}
}

// LoadStreamer returns the Streamer service configuration.
func LoadStreamer() Streamer {
	return Streamer{
		Base:       LoadBase(8082),
		CSVPath:    GetEnv("STREAMER_CSV_PATH", "samples/telemetry.csv"),
		IntervalMS: GetEnvInt("STREAMER_INTERVAL_MS", 1000),
		BatchSize:  GetEnvInt("STREAMER_BATCH_SIZE", 100),
		MQBaseURL:     GetEnv("MQ_BASE_URL", "http://localhost:8084"),
		ChannelBuffer: GetEnvInt("STREAMER_CHANNEL_BUFFER", 16),
	}
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
