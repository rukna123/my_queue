# Prompts Catalog

A structured record of every major prompt used to generate the **prompted** telemetry pipeline project, in chronological order. Each entry contains the prompt (verbatim or lightly edited for formatting) and a brief description of what it produced.

---

## 1. Project Scaffolding

**Prompt:**

> Create a new Go mono-repo with the following structure and files. Use Go 1.22+.
>
> Repo layout:
> - cmd/mq/main.go
> - cmd/streamer/main.go
> - cmd/collector/main.go
> - cmd/apigw/main.go
> - internal/config/ (shared config loader)
> - internal/db/ (db connect + migrations helper)
> - internal/models/ (shared structs)
> - internal/httpx/ (small HTTP client wrapper with retries/timeouts; no locks)
> - migrations/ (SQL migrations)
> - deploy/helm/ (Helm charts, one per component + umbrella)
> - docker-compose.yaml (local dev, single postgres)
> - Makefile (build, test, lint, run, migrate, swagger)
> - go.work workspace that includes all modules OR a single module at repo root (choose simplest and consistent).
>
> Requirements:
> - Use standard net/http server + router chi.
> - All services read config from env vars (provide .env.example).
> - Add README.md with how to run locally via docker-compose and make targets.
> - Add a consistent logging approach (slog).
> - Add health endpoints for each service: GET /healthz and GET /readyz.
>
> Do NOT add any sync.Mutex or sync.RWMutex. If you need concurrency, use owner goroutines and channels.
>
> Generate the initial code so that all four services compile (even if handlers are placeholders for now).

**What it produced:** The entire project skeleton — `go.mod`, four service binaries under `cmd/`, shared packages under `internal/` (config, db, models, httpx), stub Helm charts, `docker-compose.yaml`, `Makefile`, `.env.example`, and `README.md`. All services compiled with placeholder handlers and health endpoints.

---

## 2. Go Version Update

**Prompt:**

> please update the go version to 1.26

**What it produced:** Updated `go.mod` to specify Go 1.26.

---

## 3. PostgreSQL Schema & Migrations

**Prompt:**

> Implement Postgres schema and SQL migrations in migrations/ for:
>
> queue_messages table for the custom MQ (active/unacked messages). Columns:
> - id bigserial PK
> - topic text not null (we'll use "telemetry")
> - msg_uuid uuid not null (unique per message)
> - payload jsonb not null
> - state text not null (enum-like: ready, in_flight, acked)
> - leased_by text null
> - lease_id uuid null
> - lease_until timestamptz null
> - attempts int not null default 0
> - created_at timestamptz not null default now()
> - updated_at timestamptz not null default now()
>
> Indexes: unique(topic, msg_uuid), (topic, state, lease_until) for leasing queries, (lease_id)
>
> telemetry table for persisted telemetry entries. Columns:
> - id bigserial PK
> - uuid uuid not null unique (dedup key)
> - gpu_id text not null
> - metric_name text not null
> - timestamp timestamptz not null
> - model_name text null
> - container text null
> - pod text null
> - namespace text null
> - value double precision null
> - labels_raw text null
> - ingested_at timestamptz not null default now()
>
> Indexes: (gpu_id, timestamp) for time-ordered queries
>
> Add migration tooling: choose golang-migrate/migrate or a tiny built-in migration runner. Provide make migrate-up and make migrate-down for local dev. Update README accordingly.

**What it produced:** `migrations/001_init.up.sql` and `001_init.down.sql` with full DDL for both tables and indexes. Updated `internal/models/models.go` with Go structs. Added `make migrate-up` / `make migrate-down` targets. Updated README with migration instructions.

---

## 4. MQ Service (Monolith)

**Prompt:**

> Implement the mq service as an HTTP server with these endpoints:
>
> POST /v1/publish — Request JSON: { "topic": "telemetry", "messages": [ { "msg_uuid": "...", "payload": { ... } } ] }. Response: { "accepted": N, "duplicates": M }. Semantics: Insert into queue_messages with state='ready'. Use ON CONFLICT (topic, msg_uuid) DO NOTHING so retries are idempotent. Support batching.
>
> POST /v1/lease — Request JSON: { "topic":"telemetry", "consumer_id":"...", "max": 100, "lease_seconds": 30 }. Lease semantics (visibility timeout): Claim up to max messages that are state='ready' OR expired. Use FOR UPDATE SKIP LOCKED. Set in_flight state. Return claimed messages.
>
> POST /v1/ack — Request JSON: { "topic":"telemetry", "consumer_id":"...", "lease_id":"..." }. Delete all messages with that lease_id.
>
> Additional requirements: No sync.Mutex / RWMutex. Add request validation and clear HTTP status codes. Add metrics-friendly logging. Add /healthz and /readyz based on DB connectivity. Provide SQL queries in a dedicated file and add unit tests for leasing logic.

**What it produced:** `internal/mq/` package with `queries.go`, `store.go`, `handler.go`, and `store_test.go`. Full HTTP API with batch publish, lease with visibility timeout (`FOR UPDATE SKIP LOCKED`), and ack with delete. Updated `cmd/mq/main.go`.

---

## 5. Streamer Service

**Prompt:**

> Implement the streamer service. Requirements:
> - Reads a CSV file path from env STREAMER_CSV_PATH.
> - Each CSV row has: timestamp, metric_name, gpu_id, uuid, model_name, container, pod, namespace, value, labels_raw.
> - Each row is independent telemetry datapoint. Stream continuously in a loop.
> - IMPORTANT: when a row is processed for streaming, overwrite/set timestamp = time.Now().UTC().
> - Build a JSON payload for the MQ that includes all fields.
> - Use uuid as msg_uuid in MQ.
> - Stream periodically: env STREAMER_INTERVAL_MS controls delay between sending batches.
> - Send to MQ endpoint MQ_BASE_URL via POST /v1/publish.
> - Retry on transient errors with exponential backoff (in internal/httpx).
> - No locks. Use an owner goroutine that reads CSV rows and sends over a channel to a single HTTP sender goroutine.
> - Add /healthz and /readyz.
> - Provide a small sample CSV in samples/telemetry.csv.

**What it produced:** `internal/streamer/` package with `csv.go` (CSV parser), `streamer.go` (run loop with owner goroutine + channel + sender goroutine). Updated `cmd/streamer/main.go`. Created `samples/telemetry.csv` with 12 sample rows. Updated config, README, and `.env.example`.

---

## 6. Fire-and-Forget Streaming

**Prompt:**

> I want message queue to control the flow and take responsibility, of scaling itself if producer stream messages increase, backpressure should be handled by message queue service. Let streamer be continuous stream without waiting for. This data can be streamed in a loop to simulate continuous stream of telemetry.

**What it produced:** Refactored the streamer to fire-and-forget mode. Removed backpressure from the streamer, reduced httpx retries to 1, made the send channel buffered so the reader never blocks. The MQ service absorbs bursts via its in-memory WriteBuffer.

---

## 7. Single Message Emission

**Prompt:**

> avoid batching, and send single message in each iteration from streamer.

**What it produced:** Removed batch size logic. Each loop iteration reads one CSV row, stamps the timestamp, and sends a single message to `POST /v1/publish`.

---

## 8. MQ Writer/Reader Split

**Prompt:**

> okay, what if i divide the service into writer microservice and reader microservice. writer's job is to dump things in postgres, and spawn more writer pod if burst increases and the pods currently available hit a certain threshold of buffer.
>
> Now each reader pod, access the rows in main memory by hash modular formula, such as gpu-uuid%n = x, where x is the pod number where the row will be read in main memory, and sent only from there, thus maintaining a partition based on gpu uuid.

**What it produced:** Split the monolithic MQ into two services:
- **mqwriter** (`cmd/mqwriter/`, `internal/mqwriter/`): Receives `POST /v1/publish`, buffers in-memory, flushes to PostgreSQL asynchronously. Exposes buffer utilization metric at `GET /metrics/buffer`.
- **mqreader** (`cmd/mqreader/`, `internal/mqreader/`): Each pod owns a deterministic partition range based on `hash(gpu_id) % 256`. Loads messages into memory from DB, serves `POST /v1/lease` and `POST /v1/ack`. Added `partition_key` column via `migrations/002_add_partition_key`.

Removed the old monolithic `cmd/mq/` and `internal/mq/` packages.

---

## 9. msg_uuid Generation at MQ Writer

**Prompt:**

> tell me if it is possible that msg_uuid is generated at message queue instead of streamer, it made more sense.

**What it produced:** Moved UUID generation from the streamer to `mqwriter`. The `mqwriter` handler now generates a fresh `uuid.New()` for every incoming message, ensuring every emission is unique. Streamer no longer needs to provide `msg_uuid`.

---

## 10. MQReader Simplified In-Memory Algorithm

**Prompt:**

> don't keep idea of state in db now, state will be maintained in memory only, no need for lastId etc. all you know is whatever is in db, is ready to go, and pick the batch size of it, and load in buffer to send, if marked done, delete these from db. in next iteration of refill, just load the batch size again, irrespective of their id, and set them ready in queue, set each to lease once a request hit and message is in front of queue, if acked set it to done, if expired, then delete from main memory bearing the risk of re-delievery.

**What it produced:** Completely rewrote `internal/mqreader/partition.go` and `store.go`. The DB schema was simplified to only `id, topic, msg_uuid, payload, partition_key, created_at` (dropped `state`, `leased_by`, `lease_id`, `lease_until`, `attempts`). All state transitions (`ready` → `leased` → `done`/`expired`) happen exclusively in memory. DB is just a durable FIFO store with INSERT (by mqwriter) and DELETE (by mqreader after ack).

---

## 11. Collector Service

**Prompt:**

> Implement the collector service. Requirements:
> - Env: MQ_BASE_URL, COLLECTOR_ID (default hostname), LEASE_SECONDS, LEASE_MAX, POLL_INTERVAL_MS.
> - Loop: call POST /v1/lease to fetch messages; if none, sleep.
> - For each leased message: parse payload into telemetry struct and persist to telemetry table.
> - Use uuid unique constraint for idempotency. Do upsert or "insert on conflict do nothing".
> - After successfully persisting all messages in the lease batch, call POST /v1/ack with lease_id.
> - If persistence partially fails, DO NOT ack; let lease expire and messages retry.
> - No locks. Use an owner goroutine for the main loop.
> - Add /healthz and /readyz (DB + MQ reachability).
> - Add logs for leased count, persisted count, duplicates skipped, acked count.
> - Add tests for: idempotent insert behavior, collector doesn't ack on DB failure (simulate).
> - Update README with local run instructions.

**What it produced:** `internal/collector/` package with `queries.go`, `store.go`, `collector.go`, and `collector_test.go`. Owner goroutine loop that leases from mqreader, persists to telemetry table with `ON CONFLICT (uuid) DO NOTHING`, and acks only on full success. Updated `cmd/collector/main.go`, config, and README.

---

## 12. UUID Idempotency Rework

**Prompt:**

> Can we make sure that every message is stored by msg_id uniqueness, which i think mqwriter is generating and storing in queue_message.

*(Followed by: "please implement what we discussed in ask mode.")*

**What it produced:** Unified the dedup key across the entire pipeline. The `msg_uuid` generated by `mqwriter` is now passed through `LeasedMsg` to the collector, which uses it as the `uuid` for the `telemetry` table's `ON CONFLICT (uuid) DO NOTHING`. This ensures end-to-end exactly-once semantics (at-least-once delivery + idempotent insert).

---

## 13. APIGW Service + Swagger

**Prompt:**

> Implement apigw service exposing:
>
> GET /api/v1/gpus — Returns list of distinct gpu_id values present in telemetry. Response JSON: { "gpus": [ {"id":"..."}, ... ] }
>
> GET /api/v1/gpus/{id}/telemetry — Returns telemetry entries for GPU ordered by timestamp ascending. Optional query params: start_time and end_time inclusive (RFC3339). Response JSON: { "gpu_id":"...", "entries":[ ... ] }
>
> OpenAPI/Swagger requirements: Auto-generate OpenAPI from Go code using swaggo/swag. Add make swagger that generates swagger JSON/YAML into ./docs/ and serve Swagger UI at /swagger/*. Ensure the generated spec includes models, query params, and examples.
>
> Implementation requirements: Use chi router. Validate time filters. Use DB indexes efficiently. No locks; handlers are stateless. Add /healthz and /readyz. Add integration test for the telemetry query endpoint with time windows.

**What it produced:** `internal/apigw/` package with `queries.go`, `store.go`, `handler.go` (with swaggo annotations), and `store_test.go`. Swagger UI served at `/swagger/*`. Added `make swagger` target. Updated `cmd/apigw/main.go`, config, and README.

---

## 14. CSV Column Augmentation (device + hostname)

**Prompt:**

> I'm going to iterate columns of csv read at streamer in sequence they exist, so if you think something is missed, please augment the code to accommodate the same, at all relevant places:
> 1. timestamp 2. metric_name 3. gpu_id 4. device 5. uuid 6. modelName 7. Hostname 8. container 9. pod 10. namespace 11. value 12. labels_raw

**What it produced:** Added `device` and `hostname` columns across the entire pipeline:
- New migration `003_add_device_hostname.up.sql` / `.down.sql`
- Updated `internal/streamer/csv.go` (TelemetryRow struct + parser)
- Updated `internal/streamer/streamer.go` (payload builder)
- Updated `internal/collector/store.go` (INSERT query + struct)
- Updated `internal/apigw/handler.go` + `store.go` (SELECT query + response struct)
- Updated `samples/telemetry.csv` and Swagger docs

---

## 15. Deploy Directory Cleanup (mq → mqwriter/mqreader)

**Prompt:**

> please amend deploy directory and others if necessary to accommodate mqreader and mqwriter services, and since mq is not there anymore, clean anything corresponding to that.

**What it produced:** Removed all Helm chart files for the old `mq` service. Created/updated charts for `mqwriter` and `mqreader`. Updated the umbrella chart dependencies. Cleaned references from `docker-compose.yaml`, `Dockerfile`, `Makefile`, and `.env.example`.

---

## 16. Docker Compose Full Stack

**Prompt:**

> Update docker-compose.yaml to run: postgres, mqwriter, mqreader, streamer, collector, apigw.
> Ensure environment variables are correctly wired. Add make up and make down. Ensure make up brings the system up and after 10-30 seconds, API shows GPUs and telemetry. Update README with the exact curl commands to verify.

**What it produced:** Full `docker-compose.yaml` with all six services + postgres, correct environment variable wiring, health check dependencies, and volume mounts. Added `make up` / `make down` targets. Updated README with verification `curl` commands.

---

## 17. Comprehensive Helm Charts

**Prompt:**

> Create Helm charts under deploy/helm/ for: mqreader, mqwriter, streamer, collector, apigw, telemetry-pipeline umbrella chart that installs all components.
>
> Requirements:
> - Each chart has configurable image repo/tag, env vars, resources, probes, and replicas.
> - Use ConfigMaps/Secrets appropriately for env vars.
> - Streamer chart must support mounting a CSV via ConfigMap or a volume.
> - Add HPAs templates (CPU-based) for mq, collector, streamer.
> - Add Postgres HA as a dependency in the umbrella chart (e.g., Bitnami postgresql-ha).
> - Document how to install into a cluster: helm install ... and how to port-forward apigw.
>
> Keep the Helm design simple and correct. Do not over-engineer.

**What it produced:** Complete Helm charts for all five services plus a `telemetry-pipeline` umbrella chart. Each chart includes `Chart.yaml`, `values.yaml`, `_helpers.tpl`, `deployment.yaml`, `service.yaml`, `configmap.yaml`, `secret.yaml`, and `hpa.yaml`. The umbrella chart includes Bitnami PostgreSQL as a dependency. Streamer chart embeds CSV data via ConfigMap.

---

## 18. Scaling Plan (6 Steps)

**Prompt:**

> Here is what i plan now one by one:
> 1. change mqwriter hpa to be based on CPU, and set streamer to 10 pods.
> 2. change mqwriter hpa to watch for buffer average going beyond 85%, and scale up and if it goes below 20%, scale down. Keep CPU based scaling even now.
> 3. change mqreader hpa to be based on CPU.
> 4. change mqreader code so that it watch for it's buffer average to be around 85%, and scale down if below 20%. keep previous cpu based scaling as well.
> 5. implement Custom Controller / Operator to watch configmap, and redistribute ranges across all pods.
> 6. implement code to manage in flight message, when re-assignment of ranges happen.

*(Re-ordered to: 1 → 2 → 5 → 3 → 4 → 6)*

**What it produced:** The plan was acknowledged and re-ordered. Each step was then executed via individual prompts (Steps 19-24 below).

---

## 19. Step 1 — mqwriter CPU HPA + Streamer 10 Replicas

**Prompt:**

> In the prompted mono-repo, make these changes:
> 1. In deploy/helm/telemetry-pipeline/values.yaml, set mqwriter.autoscaling.enabled = true (CPU-based HPA, already wired)
> 2. streamer.replicaCount = 10
>
> No Go code changes. No new templates.

**What it produced:** Two value changes in the umbrella `values.yaml`. mqwriter HPA enabled (1-5 pods, 70% CPU target). Streamer set to 10 replicas.

---

## 20. Step 2 — mqwriter Buffer-Based HPA

**Prompt:**

> In the prompted mono-repo, add buffer-utilization-based autoscaling to mqwriter alongside the existing CPU-based HPA. The mqwriter already exposes buffer utilization at GET /metrics/buffer as JSON.
>
> Changes needed:
> 1. internal/mqwriter/handler.go — Add PrometheusMetrics handler returning mqwriter_buffer_utilization in Prometheus text format.
> 2. deploy/helm/mqwriter/templates/deployment.yaml — Add Prometheus scrape annotations.
> 3. deploy/helm/mqwriter/templates/hpa.yaml — Add second metric: type Pods, mqwriter_buffer_utilization, target AverageValue from values.
> 4. deploy/helm/mqwriter/values.yaml — Add targetBufferUtilization: "0.85".
> 5. Verify: go build, go vet, helm template renders both metrics.

**What it produced:** Prometheus-format `/metrics` endpoint in Go code. HPA with dual metrics (CPU + buffer utilization). Prometheus scrape annotations on pod template. New `targetBufferUtilization` value in chart.

---

## 21. Step 5 (re-ordered 3) — Partition Controller + StatefulSet

**Prompt:**

> In the prompted mono-repo, implement partition redistribution for mqreader using a StatefulSet and a custom controller.
>
> A. Switch mqreader from Deployment to StatefulSet:
> 1. Replace deployment.yaml with StatefulSet manifest + headless service
> 2. Create configmap-partitions.yaml with initial ranges
> 3. Update cmd/mqreader/main.go to read partitions from file using pod hostname
>
> B. Implement the partition controller:
> 4. cmd/partition-controller/main.go — Watches StatefulSet replicas, recomputes 256-partition ranges, updates ConfigMap, triggers rolling restart. Uses leader election.
> 5. deploy/helm/partition-controller/ — New chart with RBAC, ServiceAccount
> 6. Add to umbrella chart
> 7. Update Makefile SERVICES

**What it produced:** Converted mqreader from Deployment to StatefulSet with headless service. Created `configmap-partitions.yaml` with computed ranges. New `cmd/partition-controller/main.go` using `client-go` with leader election, watching StatefulSet replicas, updating ConfigMap, and triggering rolling restarts via annotation patching. Full Helm chart for the controller with RBAC. Updated umbrella chart and Makefile.

---

## 22. Step 3 (re-ordered 4) — mqreader CPU HPA

**Prompt:**

> In the prompted mono-repo, add CPU-based HPA for mqreader. The StatefulSet and partition controller from Step 3 must already be in place.
>
> 1. deploy/helm/mqreader/templates/hpa.yaml — targeting StatefulSet
> 2. Make replicas conditional on autoscaling.enabled
> 3. Add autoscaling section to values.yaml
> 4. Enable in umbrella values.yaml

**What it produced:** CPU-based HPA targeting the mqreader StatefulSet. Conditional replica count in the StatefulSet template. Autoscaling configuration in values.

---

## 23. Step 4 (re-ordered 5) — mqreader Buffer-Based HPA

**Prompt:**

> In the prompted mono-repo, add buffer-utilization-based autoscaling to mqreader alongside the existing CPU-based HPA.
>
> 1. internal/mqreader/partition.go — BufferUtilization() via channel query to owner goroutine
> 2. internal/mqreader/handler.go — PrometheusMetrics handler
> 3. cmd/mqreader/main.go — Register GET /metrics
> 4. StatefulSet Prometheus scrape annotations
> 5. HPA dual metrics (CPU + buffer)
> 6. targetBufferUtilization in values

**What it produced:** `BufferUtilization()` method using a channel-based query to the owner goroutine (no mutex). Prometheus `/metrics` endpoint. Dual-metric HPA for mqreader. Scrape annotations.

---

## 24. Step 6 — Graceful In-Flight Message Handling

**Prompt:**

> In the prompted mono-repo, implement graceful handling of in-flight messages when mqreader partition ranges change during redistribution.
>
> 1. cmd/mqreader/main.go — On SIGTERM: stop accepting new leases, wait for outstanding served batches to be acked or expired, batch-delete doneIDs, leave unacked messages in DB for new owner, exit cleanly.
> 2. internal/mqreader/partition.go — Add Drain() method: signals owner goroutine to stop, waits for served map to clear, uses channel signaling (no mutexes).
> 3. Update shutdown sequence: srv.Shutdown → part.Drain → cancel.
> 4. Add test verifying Drain() waits for outstanding batches.
> 5. Document in README.

**What it produced:** `Drain()` method on `Partition` using a `drainCh` channel to signal the owner goroutine. Graceful shutdown sequence in `main.go`. During redistribution, pods drain in-flight messages before shutting down. Unacked messages stay in DB for the new partition owner.

---

## 25. Header-Based CSV Parsing

**Prompt:**

> make the changes for csv read

*(Context: new CSV file had different header casing — `modelName` instead of `model_name`, `Hostname` instead of `hostname` — and the code was using positional column indexes.)*

**What it produced:** Rewrote `internal/streamer/csv.go` to use header-based column lookup with case-insensitive matching and alias support (e.g., `model_name` and `modelname` both work). Defined `requiredColumns` with accepted variants. The parser reads the header row first, builds an index map, then uses the map for field extraction. Column order and casing no longer matter.

---

## 26. Auto-Source CSV in Kubernetes

**Prompt:**

> i want to have changes so that, anywhere the code is pulled, no extra steps are needed

*(Context: CSV data was hardcoded in `deploy/helm/streamer/values.yaml`, requiring manual `--set-file` to update.)*

**What it produced:** Moved the canonical CSV file to `deploy/helm/streamer/files/telemetry.csv`. Updated `configmap-csv.yaml` to use Helm's `.Files.Get "files/telemetry.csv"` instead of reading from `values.yaml`. Removed hardcoded `csv.data` from `values.yaml`. Updated `docker-compose.yaml` to mount from the same location. Now `git clone` + `helm install` uses the latest CSV automatically.
