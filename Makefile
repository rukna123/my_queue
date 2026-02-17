# prompted – Makefile
# Usage: make <target>

SHELL := /bin/bash
.DEFAULT_GOAL := help

# ---- Variables ---------------------------------------------------------------
SERVICES     := apigw mqwriter mqreader streamer collector
DATABASE_URL ?= postgres://prompted:prompted@localhost:5432/prompted?sslmode=disable
MIGRATIONS   := ./migrations

# ---- Build -------------------------------------------------------------------
.PHONY: build
build: ## Build all service binaries into ./bin/
	@mkdir -p bin
	@for svc in $(SERVICES); do \
		echo "building $$svc ..."; \
		CGO_ENABLED=0 go build -ldflags="-s -w" -o bin/$$svc ./cmd/$$svc; \
	done

.PHONY: build-%
build-%: ## Build a single service, e.g. make build-apigw
	@mkdir -p bin
	CGO_ENABLED=0 go build -ldflags="-s -w" -o bin/$* ./cmd/$*

# ---- Run (host, without Docker) ---------------------------------------------
.PHONY: run-%
run-%: ## Run a single service on the host, e.g. make run-apigw
	go run ./cmd/$*

# ---- Test --------------------------------------------------------------------
.PHONY: test
test: ## Run all tests
	go test -race -count=1 ./...

.PHONY: test-cover
test-cover: ## Run tests with coverage
	go test -race -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html

# ---- Lint --------------------------------------------------------------------
.PHONY: lint
lint: ## Run golangci-lint
	golangci-lint run ./...

.PHONY: fmt
fmt: ## Format code
	gofmt -s -w .
	goimports -w .

# ---- Migrate -----------------------------------------------------------------
.PHONY: migrate-up
migrate-up: ## Apply all pending migrations (host Postgres)
	migrate -database "$(DATABASE_URL)" -path $(MIGRATIONS) up

.PHONY: migrate-down
migrate-down: ## Roll back the last migration (host Postgres)
	migrate -database "$(DATABASE_URL)" -path $(MIGRATIONS) down 1

.PHONY: migrate-create
migrate-create: ## Create a new migration, e.g. make migrate-create NAME=add_users
	migrate create -ext sql -dir $(MIGRATIONS) -seq $(NAME)

# ---- Docker Compose (full stack) ---------------------------------------------
.PHONY: up
up: ## Build images and start the full stack (postgres + all services)
	docker compose up -d --build
	@echo ""
	@echo "  Stack is starting.  Services:"
	@echo "    mqwriter   http://localhost:8084"
	@echo "    mqreader   http://localhost:8085"
	@echo "    streamer   http://localhost:8082"
	@echo "    collector  http://localhost:8083"
	@echo "    apigw      http://localhost:8080"
	@echo "    swagger    http://localhost:8080/swagger/index.html"
	@echo ""
	@echo "  Wait ~15 seconds, then verify:"
	@echo "    curl http://localhost:8080/api/v1/gpus"
	@echo ""

.PHONY: down
down: ## Stop and remove all containers
	docker compose down

.PHONY: down-clean
down-clean: ## Stop containers and remove volumes (wipes DB)
	docker compose down -v

.PHONY: logs
logs: ## Tail logs from all services
	docker compose logs -f

.PHONY: logs-%
logs-%: ## Tail logs from a single service, e.g. make logs-mqwriter
	docker compose logs -f $*

.PHONY: ps
ps: ## Show running containers
	docker compose ps

# ---- Docker Build (images only) ---------------------------------------------
.PHONY: docker-build
docker-build: ## Build Docker images for all services (no compose)
	@for svc in $(SERVICES); do \
		echo "docker build $$svc ..."; \
		docker build --build-arg SERVICE=$$svc -t prompted-$$svc .; \
	done

# ---- Seed / Verify ----------------------------------------------------------
.PHONY: seed
seed: ## Show how data flows (streamer auto-seeds on 'make up')
	@echo "The streamer service auto-streams CSV data once 'make up' is running."
	@echo "No manual seeding is needed. Wait ~15s after 'make up', then:"
	@echo ""
	@echo "  curl http://localhost:8080/api/v1/gpus"
	@echo "  curl 'http://localhost:8080/api/v1/gpus/GPU-0a1b2c3d/telemetry'"
	@echo ""

.PHONY: verify
verify: ## Quick health check on all services
	@echo "--- healthz ---"
	@curl -sf http://localhost:8084/healthz && echo " mqwriter OK"  || echo " mqwriter FAIL"
	@curl -sf http://localhost:8085/healthz && echo " mqreader OK"  || echo " mqreader FAIL"
	@curl -sf http://localhost:8082/healthz && echo " streamer OK"  || echo " streamer FAIL"
	@curl -sf http://localhost:8083/healthz && echo " collector OK" || echo " collector FAIL"
	@curl -sf http://localhost:8080/healthz && echo " apigw OK"     || echo " apigw FAIL"
	@echo ""
	@echo "--- GPU list ---"
	@curl -sf http://localhost:8080/api/v1/gpus | python3 -m json.tool 2>/dev/null || echo "(no data yet — wait a few seconds)"

# ---- Swagger -----------------------------------------------------------------
.PHONY: swagger
swagger: ## Generate OpenAPI spec into docs/swagger/
	go run github.com/swaggo/swag/cmd/swag@latest init -g cmd/apigw/main.go -o docs/swagger --parseDependency --parseInternal

# ---- Help --------------------------------------------------------------------
.PHONY: help
help: ## Show this help
	@grep -E '^[a-zA-Z_%-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'
