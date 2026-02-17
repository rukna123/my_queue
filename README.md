# prompted

Go mono-repo with five micro-services: **apigw**, **mqwriter**, **mqreader**, **streamer**, and **collector**.

## Prerequisites

- Go 1.26+
- Docker & Docker Compose
- [golang-migrate CLI](https://github.com/golang-migrate/migrate/tree/master/cmd/migrate) (for host-mode migrations)
- [golangci-lint](https://golangci-lint.run/) (optional, for linting)

## Quick Start (Docker Compose — recommended)

One command brings up the entire stack: Postgres, migrations, and all five services.

```bash
make up
```

Wait ~15 seconds for data to flow through the pipeline, then verify:

```bash
# List GPUs
curl http://localhost:8080/api/v1/gpus

# Telemetry for a specific GPU
curl 'http://localhost:8080/api/v1/gpus/GPU-0a1b2c3d/telemetry'

# Telemetry with time window
curl 'http://localhost:8080/api/v1/gpus/GPU-0a1b2c3d/telemetry?start_time=2026-01-01T00:00:00Z&end_time=2027-01-01T00:00:00Z'

# Health check all services
make verify
```

To stop:

```bash
make down          # stop containers (preserves DB data)
make down-clean    # stop containers AND wipe DB volume
```

### What happens on `make up`

1. **Postgres** starts and becomes healthy.
2. **migrate** runs all SQL migrations, then exits.
3. **mqwriter** (:8084) and **mqreader** (:8085) start once migrations complete.
4. **streamer** (:8082) starts once mqwriter is healthy — reads `samples/telemetry.csv`, stamps `time.Now().UTC()` on each row, publishes to mqwriter every second.
5. **collector** (:8083) starts once mqreader is healthy — leases messages, persists to `telemetry` table.
6. **apigw** (:8080) starts once migrations complete — serves REST API and Swagger UI.

No manual seeding step is needed. The streamer continuously loops over the CSV, generating fresh telemetry data with unique message IDs.

### Useful commands

```bash
make logs            # tail all service logs
make logs-mqwriter   # tail a single service
make ps              # show running containers
make verify          # quick health + data check
```

## Quick Start (Host Mode — without Docker)

```bash
# 1. Copy env file and adjust if needed
cp .env.example .env

# 2. Start PostgreSQL (via compose or install locally)
docker compose up -d postgres

# 3. Run migrations
make migrate-up

# 4. Run services (each in its own terminal)
make run-mqwriter    # :8084
make run-mqreader    # :8085
make run-streamer    # :8082
make run-collector   # :8083
make run-apigw       # :8080
```

## Architecture

```
  Streamer ──POST /v1/publish──► MQWriter ──flush──► Postgres (queue_messages)
                                                          │
  Collector ◄──POST /v1/lease── MQReader ◄──poll──────────┘
      │                              │
      └──persist──► Postgres         └──POST /v1/ack──► MQReader ──delete──► Postgres
         (telemetry)
```

- **MQWriter** – receives raw telemetry JSON, generates `msg_uuid`, computes `partition_key = FNV-1a(gpu_id) % 256`, buffers in memory, flushes to Postgres asynchronously. Exposes `GET /metrics/buffer` for HPA autoscaling.
- **MQReader** – each pod owns a range of 256 virtual partitions, polls Postgres for its partition's rows into memory, serves `POST /v1/lease` and `POST /v1/ack` from the in-memory store. No contention between pods.
- **Streamer** – reads a CSV file, stamps `time.Now().UTC()` on each row, writes timestamps back to disk, publishes each row as a single message to mqwriter.
- **Collector** – polls mqreader for leased messages, parses payloads, inserts into the `telemetry` table with `ON CONFLICT (uuid) DO NOTHING` for idempotency, acks the lease only on full success. Uses the mqwriter-generated `msg_uuid` as the dedup key.
- **API Gateway** – public-facing HTTP API serving GPU telemetry queries. Exposes `GET /api/v1/gpus` and `GET /api/v1/gpus/{id}/telemetry` (with optional RFC3339 time filters). Auto-generated OpenAPI docs served at `/swagger/`.

## Project Structure

```
prompted/
├── cmd/
│   ├── apigw/          # API gateway service
│   ├── mqwriter/       # MQ write-side service
│   ├── mqreader/       # MQ read-side service
│   ├── streamer/       # CSV telemetry streamer
│   └── collector/      # Telemetry collector/persister
├── internal/
│   ├── config/         # Env-based configuration loader
│   ├── db/             # Database connection & migration helpers
│   ├── httpx/          # HTTP client wrapper with retries/timeouts
│   ├── models/         # Shared domain structs
│   ├── mqwriter/       # Writer store, buffer, handler, SQL
│   ├── mqreader/       # Reader store, partition, handler, SQL
│   ├── streamer/       # CSV reader + streaming pipeline
│   ├── collector/      # Collector store, loop, SQL, tests
│   └── apigw/          # API gateway store, handler, SQL, tests
├── docs/swagger/       # Generated OpenAPI spec + Swagger UI assets
├── migrations/         # SQL migration files
├── samples/            # Sample data files (telemetry CSV)
├── deploy/helm/        # Helm charts (per-service + umbrella)
├── docker-compose.yaml # Full local dev stack (all services)
├── Dockerfile          # Multi-stage build (all services)
├── Makefile            # Build, test, lint, run, migrate, up/down
└── .env.example        # Environment variable template
```

## Make Targets

| Target              | Description                                    |
|---------------------|------------------------------------------------|
| `make up`           | Build & start full stack (Postgres + services) |
| `make down`         | Stop all containers                            |
| `make down-clean`   | Stop containers + wipe DB volume               |
| `make logs`         | Tail logs from all services                    |
| `make logs-mqwriter`| Tail logs from a single service                |
| `make ps`           | Show running containers                        |
| `make verify`       | Health check + quick data verification         |
| `make seed`         | Show seeding instructions (auto via streamer)  |
| `make build`        | Build all binaries into `./bin/`               |
| `make build-apigw`  | Build a single service                         |
| `make run-apigw`    | Run a single service on the host               |
| `make test`         | Run all tests with race detector               |
| `make test-cover`   | Run tests with HTML coverage report            |
| `make lint`         | Run golangci-lint                              |
| `make fmt`          | Format code (gofmt + goimports)                |
| `make migrate-up`   | Apply pending migrations (host Postgres)       |
| `make migrate-down` | Roll back one migration                        |
| `make docker-build` | Build Docker images (no compose)               |
| `make swagger`      | Generate OpenAPI spec into `docs/swagger/`     |

## Verifying the Pipeline

After `make up`, wait ~15 seconds, then run:

```bash
# 1. Check all services are healthy
make verify

# 2. List GPUs (should show 3 GPU IDs from the sample CSV)
curl -s http://localhost:8080/api/v1/gpus | python3 -m json.tool
# {
#     "gpus": [
#         {"id": "GPU-0a1b2c3d"},
#         {"id": "GPU-5e6f7a8b"},
#         {"id": "GPU-c9d0e1f2"}
#     ]
# }

# 3. Get telemetry for a GPU
curl -s 'http://localhost:8080/api/v1/gpus/GPU-0a1b2c3d/telemetry' | python3 -m json.tool
# Returns entries with recent timestamps (streamer stamps time.Now())

# 4. Filter by time window (RFC3339)
curl -s 'http://localhost:8080/api/v1/gpus/GPU-0a1b2c3d/telemetry?start_time=2026-02-15T00:00:00Z&end_time=2026-02-16T00:00:00Z' | python3 -m json.tool

# 5. Check mqwriter buffer metrics
curl -s http://localhost:8084/metrics/buffer
# {"buffer_utilization": 0.0}

# 6. Swagger UI
open http://localhost:8080/swagger/index.html
```

## Configuration

All services read configuration from environment variables. See `.env.example` for the full list.

| Variable | Default | Service | Description |
|---|---|---|---|
| `PORT` | 8080/8082-8085 | all | Listen port |
| `LOG_LEVEL` | `info` | all | `debug/info/warn/error` |
| `DATABASE_URL` | `postgres://...` | all (except streamer) | PostgreSQL DSN |
| `MQ_PARTITION_COUNT` | `256` | mqwriter | Total virtual partitions |
| `MQ_BUFFER_SIZE` | `500` | mqwriter | Buffer flush threshold (messages) |
| `MQ_FLUSH_INTERVAL` | `500ms` | mqwriter | Buffer flush timer |
| `READER_PARTITION_START` | `0` | mqreader | First owned partition |
| `READER_PARTITION_END` | `255` | mqreader | Last owned partition |
| `READER_POLL_INTERVAL` | `500ms` | mqreader | DB poll frequency |
| `READER_POLL_BATCH` | `1000` | mqreader | Max rows per poll |
| `STREAMER_CSV_PATH` | `samples/telemetry.csv` | streamer | CSV file path |
| `STREAMER_INTERVAL_MS` | `1000` | streamer | Delay between messages |
| `MQ_BASE_URL` | `http://localhost:8084` | streamer | MQWriter URL |
| `STREAMER_CHANNEL_BUFFER` | `16` | streamer | Internal channel capacity |
| `COLLECTOR_ID` | hostname | collector | Consumer identity |
| `LEASE_SECONDS` | `30` | collector | Lease duration |
| `LEASE_MAX` | `100` | collector | Max messages per lease |
| `POLL_INTERVAL_MS` | `1000` | collector | Polling frequency |
| `MQ_BASE_URL` | `http://localhost:8085` | collector | MQReader URL |

## API Gateway

The API gateway exposes GPU telemetry data over REST. Start it after the full pipeline is running:

```bash
make run-apigw   # http://localhost:8080
```

### Endpoints

**`GET /api/v1/gpus`** — list distinct GPU IDs in the telemetry table.

```bash
curl http://localhost:8080/api/v1/gpus
# {"gpus":[{"id":"GPU-0a1b2c3d"},{"id":"GPU-5e6f7a8b"},{"id":"GPU-c9d0e1f2"}]}
```

**`GET /api/v1/gpus/{id}/telemetry`** — telemetry entries for a GPU, ordered by timestamp ascending.

Optional query params `start_time` and `end_time` (RFC3339, inclusive):

```bash
curl 'http://localhost:8080/api/v1/gpus/GPU-0a1b2c3d/telemetry?start_time=2026-01-01T00:00:00Z&end_time=2027-01-01T00:00:00Z'
```

### Swagger UI

Auto-generated OpenAPI docs are served at:

```
http://localhost:8080/swagger/index.html
```

To regenerate after changing handler annotations:

```bash
make swagger
```

## Database Schema

Migrations live in `migrations/` and are managed by [golang-migrate](https://github.com/golang-migrate/migrate).

```bash
make migrate-up      # apply all pending migrations
make migrate-down    # roll back the last migration
make migrate-create NAME=add_foo   # scaffold a new migration pair
```

### `queue_messages`

Backing table for the MQ broker. Each row is a message with a lifecycle state.

| Column | Type | Notes |
|---|---|---|
| `id` | `bigserial` | PK |
| `topic` | `text` | not null |
| `msg_uuid` | `uuid` | not null, unique per topic (generated by mqwriter) |
| `payload` | `jsonb` | not null (raw CSV row content) |
| `state` | `text` | `ready` / `in_flight` / `acked` |
| `leased_by` | `text` | nullable – consumer identifier |
| `lease_id` | `uuid` | nullable – lease correlation ID |
| `lease_until` | `timestamptz` | nullable – lease expiry |
| `attempts` | `int` | default 0 |
| `partition_key` | `int` | not null – `FNV-1a(gpu_id) % 256` |
| `created_at` | `timestamptz` | default `now()` |
| `updated_at` | `timestamptz` | default `now()` |

**Indexes**: `UNIQUE(topic, msg_uuid)`, `(topic, state, lease_until)`, `(lease_id)`, `(partition_key, state, id)`.

### `telemetry`

Persisted GPU telemetry entries written by the collector.

| Column | Type | Notes |
|---|---|---|
| `id` | `bigserial` | PK |
| `uuid` | `uuid` | not null, unique (dedup key = mqwriter's `msg_uuid`) |
| `gpu_id` | `text` | not null |
| `metric_name` | `text` | not null |
| `timestamp` | `timestamptz` | not null |
| `model_name` | `text` | nullable |
| `device` | `text` | nullable |
| `hostname` | `text` | nullable |
| `container` | `text` | nullable |
| `pod` | `text` | nullable |
| `namespace` | `text` | nullable |
| `value` | `double precision` | nullable |
| `labels_raw` | `text` | nullable |
| `ingested_at` | `timestamptz` | default `now()` |

**Indexes**: `UNIQUE(uuid)`, `(gpu_id, timestamp)`.

## Health Endpoints

Every service exposes:

- `GET /healthz` – liveness probe (always 200 if the process is up)
- `GET /readyz` – readiness probe (checks DB and/or MQ reachability)

## Logging

All services use Go's `log/slog` with JSON output. Set `LOG_LEVEL` to control verbosity.

## Concurrency Model

This codebase avoids `sync.Mutex` and `sync.RWMutex`. Where concurrency is needed, owner goroutines and channels are used instead.
