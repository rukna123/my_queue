# prompted – Makefile
# Usage: make <target>

SHELL := /bin/bash
.DEFAULT_GOAL := help

# ---- Variables ---------------------------------------------------------------
SERVICES     := apigw mq streamer collector
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

# ---- Run ---------------------------------------------------------------------
.PHONY: run-%
run-%: ## Run a single service, e.g. make run-apigw
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
migrate-up: ## Apply all pending migrations
	migrate -database "$(DATABASE_URL)" -path $(MIGRATIONS) up

.PHONY: migrate-down
migrate-down: ## Roll back the last migration
	migrate -database "$(DATABASE_URL)" -path $(MIGRATIONS) down 1

.PHONY: migrate-create
migrate-create: ## Create a new migration, e.g. make migrate-create NAME=add_users
	migrate create -ext sql -dir $(MIGRATIONS) -seq $(NAME)

# ---- Docker ------------------------------------------------------------------
.PHONY: up
up: ## Start local dev dependencies (postgres)
	docker-compose up -d

.PHONY: down
down: ## Stop local dev dependencies
	docker-compose down

.PHONY: docker-build
docker-build: ## Build Docker images for all services
	@for svc in $(SERVICES); do \
		echo "docker build $$svc ..."; \
		docker build --build-arg SERVICE=$$svc -t prompted-$$svc .; \
	done

# ---- Swagger -----------------------------------------------------------------
.PHONY: swagger
swagger: ## Generate swagger docs (requires swag CLI)
	swag init -g cmd/apigw/main.go -o docs/swagger

# ---- Help --------------------------------------------------------------------
.PHONY: help
help: ## Show this help
	@grep -E '^[a-zA-Z_%-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'
