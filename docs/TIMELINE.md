# Project Timeline & Narrative

How the **prompted** telemetry pipeline evolved from an empty directory to a production-ready, auto-scaling Kubernetes deployment — told as a narrative.

---

## Phase 1: Foundation (Scaffolding + Data Layer)

The project began with a single prompt that established the entire Go mono-repo structure. The goal was clear from the start: build a telemetry pipeline with four microservices (MQ, Streamer, Collector, API Gateway) using Go's standard library plus `chi` for routing and `slog` for structured logging. A hard constraint was set early: **no `sync.Mutex` or `sync.RWMutex` anywhere** — all concurrency must use owner goroutines communicating over channels.

The scaffolding prompt generated the full directory layout, four compilable service binaries, shared packages (`config`, `db`, `models`, `httpx`), a `Makefile`, `docker-compose.yaml`, and a `README.md`. Every service got `GET /healthz` and `GET /readyz` endpoints from day one.

Next came the data layer. A detailed prompt specified the exact PostgreSQL schemas for `queue_messages` (the custom message queue's backing store) and `telemetry` (the final destination for GPU metrics). Column types, indexes, and constraints were all defined upfront. The `golang-migrate` tool was wired in with `make migrate-up` and `make migrate-down`.

---

## Phase 2: Core Services (MQ + Streamer)

With the skeleton in place, the MQ service was the first to be fully implemented. The prompt specified three HTTP endpoints (`/v1/publish`, `/v1/lease`, `/v1/ack`) with precise semantics: idempotent publish via `ON CONFLICT DO NOTHING`, visibility-timeout-based leasing via `FOR UPDATE SKIP LOCKED`, and ack-by-deletion. SQL queries were placed in a dedicated file, and integration tests were written against a real Postgres instance.

The streamer came next. It reads a CSV file, overwrites each row's timestamp with `time.Now().UTC()`, and publishes to the MQ. The initial design used batching, but this quickly evolved through a series of design discussions:

1. **Timestamp persistence**: The user wanted processed timestamps written back to the CSV file on disk, not just updated in memory. This ensures each row has a unique timestamp on every loop iteration.

2. **Fire-and-forget**: The user decided that backpressure should be the MQ's responsibility, not the streamer's. The streamer was refactored to fire-and-forget — send and move on, let the MQ buffer absorb bursts.

3. **Single-message emission**: Batching was removed. Each loop iteration sends exactly one message, simulating a continuous stream of individual telemetry datapoints.

---

## Phase 3: Architecture Evolution (Writer/Reader Split)

A pivotal moment came when the user proposed splitting the monolithic MQ service into two microservices:

- **mqwriter**: Receives publishes, buffers in memory, flushes to PostgreSQL asynchronously. Horizontally scalable — buffer utilization drives pod scaling.
- **mqreader**: Each pod owns a deterministic partition of messages based on `hash(gpu_id) % 256`. Maintains messages in main memory and serves them to collectors.

This was a significant architectural shift. The partition model eliminated read-side contention entirely — no more `FOR UPDATE SKIP LOCKED` between competing readers. Each pod owns a disjoint slice of the data.

The split also surfaced a UUID problem. Originally, the streamer's CSV `uuid` field was used as `msg_uuid` in the queue. Since the same CSV is streamed in a loop, this caused `ON CONFLICT` to reject every message after the first pass. The fix: move `msg_uuid` generation to the mqwriter, so every emission gets a fresh UUID regardless of the CSV content.

---

## Phase 4: MQReader Algorithm Refinement

The mqreader went through a major simplification driven by the user's design intuition. The original design tracked message state (`ready`, `in_flight`, `acked`) in the database with lease metadata columns. The user pushed for a cleaner model:

> "don't keep idea of state in db now, state will be maintained in memory only... all you know is whatever is in db, is ready to go, and pick the batch size of it, and load in buffer to send"

The result was elegant:
- **DB is a dumb FIFO store**: Only INSERT (by mqwriter) and DELETE (by mqreader). No UPDATE ever.
- **State lives in memory**: Messages flow through `ready` → `leased` → `done` (or `expired`) entirely in the owner goroutine.
- **Refill on empty**: When the buffer has no ready messages and no outstanding leases, load another batch from DB.
- **Expired messages drop from memory**: They remain in DB and get re-fetched on the next refill.

This eliminated multiple columns from the schema (`state`, `leased_by`, `lease_id`, `lease_until`, `attempts`) and drastically reduced DB write pressure.

---

## Phase 5: Collector + End-to-End Pipeline

The collector service completed the pipeline. It runs an owner goroutine loop: poll `POST /v1/lease` on mqreader, parse each message's payload into a telemetry struct, persist to the `telemetry` table with `ON CONFLICT (uuid) DO NOTHING` for idempotency, then ack the lease. If any persist fails, the entire batch goes unacked — the lease expires in mqreader's memory, messages stay in DB, and the next refill cycle retries them.

A critical idempotency rework followed: the `msg_uuid` generated by mqwriter became the single authoritative dedup key across the entire pipeline. It flows from mqwriter → queue_messages → mqreader lease response → collector → telemetry table's `uuid` column. This guarantees that even with at-least-once delivery, the telemetry table never contains duplicates.

---

## Phase 6: API Gateway + Swagger

The API Gateway (`apigw`) exposed two query endpoints:
- `GET /api/v1/gpus` — distinct GPU IDs from the telemetry table
- `GET /api/v1/gpus/{id}/telemetry` — time-series data for a specific GPU with optional `start_time`/`end_time` filters

OpenAPI documentation was auto-generated from Go code using `swaggo/swag`, with a `make swagger` target and Swagger UI served at `/swagger/*`.

Shortly after, the data model was augmented with `device` and `hostname` columns to match the full CSV schema. This touched every layer: migration, streamer parser, streamer payload, collector INSERT, and apigw SELECT.

---

## Phase 7: Containerization & Helm Charts

With all services working locally via `docker-compose`, the project moved to Kubernetes deployment. A comprehensive set of Helm charts was created:

- **Per-service charts**: `mqwriter`, `mqreader`, `streamer`, `collector`, `apigw` — each with configurable images, environment variables (ConfigMap + Secret), resource limits, liveness/readiness probes, and optional HPAs.
- **Umbrella chart** (`telemetry-pipeline`): Installs all services plus Bitnami PostgreSQL as a dependency. A single `values.yaml` configures the entire stack.

The streamer chart included a ConfigMap-based CSV mount for demo deployments.

The first Kubernetes deployment hit real-world issues: disk pressure from the resource-heavy `postgresql-ha` chart. This led to replacing it with the simpler `bitnami/postgresql` single-instance chart, which was more appropriate for the project's scale.

Other issues encountered and solved:
- **Read-only ConfigMap volume**: The streamer couldn't write timestamps back to the CSV mounted from a ConfigMap. Solution: in-memory-only timestamp updates with best-effort disk writes (silently disabling disk writes when the filesystem is read-only).
- **Missing migrations on fresh clusters**: The `queue_messages` table didn't exist until migrations were run. This was solved with documentation and `make migrate-up` after port-forwarding.

---

## Phase 8: Advanced Scaling (The 6-Step Plan)

The most complex phase was a structured 6-step plan to add intelligent autoscaling and partition redistribution:

### Step 1: CPU-based HPA for mqwriter + 10 streamer replicas
Simple value changes — enabled the existing HPA template for mqwriter and set streamer to 10 pods. No code changes.

### Step 2: Buffer-based HPA for mqwriter
Added a Prometheus-format `/metrics` endpoint exposing `mqwriter_buffer_utilization` (a 0.0-1.0 gauge). The HPA was extended to dual metrics: CPU utilization *and* buffer utilization. If either crosses its threshold, scaling kicks in. This required Prometheus + Prometheus Adapter in the cluster.

### Step 3 (re-ordered to 5): Partition Controller + StatefulSet
The foundation for safe mqreader scaling. The mqreader Deployment was converted to a **StatefulSet** (for stable pod identity: `mqreader-0`, `mqreader-1`, ...) with a headless service.

A new **partition-controller** service was built — a Kubernetes controller using `client-go` that:
1. Watches the mqreader StatefulSet's replica count
2. When replicas change, recomputes partition ranges (256 partitions ÷ N pods)
3. Updates a `mqreader-partitions` ConfigMap
4. Triggers a rolling restart by patching an annotation on the StatefulSet

The controller uses leader election so only one instance acts at a time. Each mqreader pod reads its partition range from the ConfigMap file mounted at `/etc/mqreader/partitions.yaml`, using its hostname to look up its entry.

### Step 4 (re-ordered to 3): CPU-based HPA for mqreader
With the StatefulSet and partition controller in place, CPU-based HPA was added targeting the mqreader StatefulSet. When HPA changes the replica count, the partition controller automatically redistributes ranges.

### Step 5 (re-ordered to 4): Buffer-based HPA for mqreader
Mirrored the mqwriter approach: `BufferUtilization()` method on the `Partition` struct (using a channel query to the owner goroutine — still no mutexes), Prometheus endpoint, dual-metric HPA.

### Step 6: Graceful In-Flight Message Handling
The final piece: when partition ranges change and pods restart, in-flight messages need to be handled gracefully. A `Drain()` method was added to the `Partition` struct that:
1. Signals the owner goroutine to stop accepting new lease commands
2. Waits for all entries in the served map to resolve (acked → deleted, or expired → dropped)
3. Returns when fully drained or timeout is reached

The shutdown sequence became: HTTP server shutdown → partition drain → context cancel → exit. Unacked messages remain in DB for the new partition owner.

---

## Phase 9: CSV Resilience & Kubernetes Data Sourcing

The final phase addressed real-world data handling. When a new, larger CSV file (2471 data rows) with different header casing (`modelName` instead of `model_name`) was introduced, the hardcoded positional column parser broke.

The CSV reader was rewritten to use **header-based column lookup** with case-insensitive matching and alias support. A `requiredColumns` table maps logical field names to accepted header variants. The `normalizeHeader()` function strips underscores and lowercases, so `model_name`, `modelName`, and `ModelName` all resolve to the same column.

The last issue was Kubernetes data sourcing. The CSV data was hardcoded in `deploy/helm/streamer/values.yaml`, meaning updating the CSV required manual intervention. The fix used Helm's `.Files.Get` function: the CSV file was placed at `deploy/helm/streamer/files/telemetry.csv`, and the ConfigMap template reads it directly at render time. `docker-compose.yaml` was updated to mount from the same location. One file, one source of truth — `git clone` + `helm install` just works.

---

## Architecture Summary

```
                    ┌──────────────┐
                    │   Streamer   │ ×10 pods
                    │  (CSV → MQ)  │
                    └──────┬───────┘
                           │ POST /v1/publish
                           ▼
                    ┌──────────────┐
                    │   MQWriter   │ HPA: CPU + buffer
                    │ (buffer→DB)  │
                    └──────┬───────┘
                           │ INSERT → PostgreSQL
                           ▼
                    ┌──────────────┐
                    │  PostgreSQL  │ queue_messages + telemetry
                    └──────┬───────┘
                           │ SELECT (by partition range)
                           ▼
                    ┌──────────────┐
                    │   MQReader   │ StatefulSet, HPA: CPU + buffer
                    │ (DB→memory)  │ Partition controller redistributes
                    └──────┬───────┘
                           │ POST /v1/lease + /v1/ack
                           ▼
                    ┌──────────────┐
                    │  Collector   │ ×10 pods
                    │ (MQ→telemetry│
                    │    table)    │
                    └──────────────┘
                           │ INSERT → telemetry table
                           ▼
                    ┌──────────────┐
                    │    APIGW     │ Swagger UI
                    │ (REST API)   │
                    └──────────────┘
```

## Key Design Principles Maintained Throughout

1. **No mutexes** — Every concurrent component uses owner goroutines and channels
2. **Idempotency everywhere** — `ON CONFLICT DO NOTHING` at both queue and telemetry layers
3. **DB as durable store only** — MQReader state lives in memory; DB is just INSERT + DELETE
4. **Partition-based ownership** — Deterministic hash partitioning eliminates read contention
5. **Graceful degradation** — Lease expiry, drain on shutdown, collector-side dedup
6. **Single source of truth** — CSV, config, and Helm values each have one canonical location
