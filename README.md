# prompted

Go mono-repo with four micro-services: **apigw**, **mq**, **streamer**, and **collector**.

## Prerequisites

- Go 1.26+
- Docker & Docker Compose
- [golang-migrate CLI](https://github.com/golang-migrate/migrate/tree/master/cmd/migrate) (for migrations)
- [golangci-lint](https://golangci-lint.run/) (optional, for linting)

## Quick Start

```bash
# 1. Copy env file and adjust if needed
cp .env.example .env

# 2. Start PostgreSQL
make up

# 3. Run migrations
make migrate-up

# 4. Run a service (in separate terminals)
make run-apigw      # :8080
make run-mq         # :8081
make run-streamer   # :8082
make run-collector  # :8083
```

Verify a service is running:

```bash
curl http://localhost:8080/healthz
# {"status":"ok","service":"apigw"}
```

## Project Structure

```
prompted/
├── cmd/
│   ├── apigw/          # API gateway service
│   ├── mq/             # Message queue service
│   ├── streamer/       # Streamer service
│   └── collector/      # Collector service
├── internal/
│   ├── config/         # Env-based configuration loader
│   ├── db/             # Database connection & migration helpers
│   ├── httpx/          # HTTP client wrapper with retries/timeouts
│   └── models/         # Shared domain structs
├── migrations/         # SQL migration files
├── deploy/helm/        # Helm charts (per-service + umbrella)
├── docker-compose.yaml # Local dev (PostgreSQL)
├── Dockerfile          # Multi-stage build (all services)
├── Makefile            # Build, test, lint, run, migrate, swagger
└── .env.example        # Environment variable template
```

## Make Targets

| Target              | Description                              |
|---------------------|------------------------------------------|
| `make build`        | Build all binaries into `./bin/`         |
| `make build-apigw`  | Build a single service                   |
| `make run-apigw`    | Run a single service via `go run`        |
| `make test`         | Run all tests with race detector         |
| `make test-cover`   | Run tests with HTML coverage report      |
| `make lint`         | Run golangci-lint                        |
| `make fmt`          | Format code (gofmt + goimports)          |
| `make migrate-up`   | Apply pending migrations                 |
| `make migrate-down` | Roll back one migration                  |
| `make up`           | Start docker-compose (PostgreSQL)        |
| `make down`         | Stop docker-compose                      |
| `make docker-build` | Build Docker images for all services     |
| `make swagger`      | Generate Swagger docs                    |

## Configuration

All services read configuration from environment variables. See `.env.example` for the full list.

| Variable            | Default                                                              | Description          |
|---------------------|----------------------------------------------------------------------|----------------------|
| `PORT`              | 8080 (apigw), 8081 (mq), 8082 (streamer), 8083 (collector)         | Listen port          |
| `LOG_LEVEL`         | `info`                                                               | `debug/info/warn/error` |
| `DATABASE_URL`      | `postgres://prompted:prompted@localhost:5432/prompted?sslmode=disable` | PostgreSQL DSN       |
| `UPSTREAM_TIMEOUT`  | `10s`                                                                | apigw upstream timeout |
| `COLLECT_INTERVAL`  | `30s`                                                                | collector tick rate  |

## Database Schema

Migrations live in `migrations/` and are managed by [golang-migrate](https://github.com/golang-migrate/migrate).

```bash
make migrate-up      # apply all pending migrations
make migrate-down    # roll back the last migration
make migrate-create NAME=add_foo   # scaffold a new migration pair
```

### `queue_messages`

Backing table for the custom MQ broker. Each row is a message with a lifecycle state.

| Column       | Type          | Notes                                      |
|--------------|---------------|--------------------------------------------|
| `id`         | `bigserial`   | PK                                         |
| `topic`      | `text`        | not null (e.g. `telemetry`)                |
| `msg_uuid`   | `uuid`        | not null, unique per topic (dedup key)     |
| `payload`    | `jsonb`       | not null                                   |
| `state`      | `text`        | `ready` / `in_flight` / `acked`            |
| `leased_by`  | `text`        | nullable – consumer identifier             |
| `lease_id`   | `uuid`        | nullable – lease correlation ID            |
| `lease_until`| `timestamptz` | nullable – lease expiry                    |
| `attempts`   | `int`         | default 0                                  |
| `created_at` | `timestamptz` | default `now()`                            |
| `updated_at` | `timestamptz` | default `now()`                            |

**Indexes**: `UNIQUE(topic, msg_uuid)`, `(topic, state, lease_until)`, `(lease_id)`.

### `telemetry`

Persisted GPU telemetry entries written by the streamer.

| Column        | Type               | Notes                                 |
|---------------|--------------------|---------------------------------------|
| `id`          | `bigserial`        | PK                                    |
| `uuid`        | `uuid`             | not null, unique (dedup key)          |
| `gpu_id`      | `text`             | not null                              |
| `metric_name` | `text`             | not null                              |
| `timestamp`   | `timestamptz`      | not null – `processed_at` from streamer |
| `model_name`  | `text`             | nullable                              |
| `container`   | `text`             | nullable                              |
| `pod`         | `text`             | nullable                              |
| `namespace`   | `text`             | nullable                              |
| `value`       | `double precision` | nullable                              |
| `labels_raw`  | `text`             | nullable                              |
| `ingested_at` | `timestamptz`      | default `now()`                       |

**Indexes**: `UNIQUE(uuid)`, `(gpu_id, timestamp)`.

## Health Endpoints

Every service exposes:

- `GET /healthz` – liveness probe (always returns 200 if the process is up)
- `GET /readyz` – readiness probe (returns 200 only when the database is reachable)

## Logging

All services use Go's `log/slog` with JSON output. Set `LOG_LEVEL` to control verbosity.

## Concurrency Model

This codebase avoids `sync.Mutex` and `sync.RWMutex`. Where concurrency is needed, owner goroutines and channels are used instead.
