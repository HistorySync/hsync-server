# HistorySync Cloud Server - Makefile
# Common development and deployment commands.

APP_NAME    := hsync-server
BUILD_DIR   := ./build
CMD_DIR     := ./cmd/hsync-server
DOCKER_IMAGE:= historysync/server
BUILD_PKG   := github.com/historysync/hsync-server/pkg/buildinfo
VERSION     := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT      := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_TIME  := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

# Go build flags
LDFLAGS := -s -w \
	-X $(BUILD_PKG).version=$(VERSION) \
	-X $(BUILD_PKG).commit=$(COMMIT) \
	-X $(BUILD_PKG).buildTime=$(BUILD_TIME) \
	-X $(BUILD_PKG).edition=community

# Targets

.PHONY: all build run test test-smoke test-integration loadtest release-check lint clean docker-build docker-up docker-down migrate-up migrate-down

all: lint test build

## build: Compile the server binary
build:
	@echo "Building $(APP_NAME)..."
	@mkdir -p $(BUILD_DIR)
	go build -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(APP_NAME) $(CMD_DIR)

## run: Build and run the server locally
run: build
	$(BUILD_DIR)/$(APP_NAME)

## test: Run all tests with race detection
test:
	go test -race -count=1 -timeout 60s ./...

## test-smoke: Run production readiness smoke checks (requires Docker)
test-smoke:
	go test -tags=smoke -count=1 -timeout 300s ./cmd/hsync-server

## test-integration: Run DB-backed integration tests (requires a running Docker daemon)
test-integration:
	go test -tags=integration -count=1 -timeout 300s ./pkg/repository/...

## loadtest: Run local CE smoke+load rehearsal against a running server
loadtest:
	go run ./cmd/loadtest

## release-check: Run the release candidate gate and emit a JSON report
release-check:
	pwsh -ExecutionPolicy Bypass -File .\scripts\release-check.ps1

## lint: Run linters (requires golangci-lint)
lint:
	golangci-lint run ./...

## clean: Remove build artifacts
clean:
	rm -rf $(BUILD_DIR)

## dev: Start development environment with hot reload (requires air)
dev:
	air -c .air.toml

# Docker

## docker-build: Build Docker image
docker-build:
	docker build \
		--build-arg VERSION=$(VERSION) \
		--build-arg COMMIT=$(COMMIT) \
		--build-arg BUILD_TIME=$(BUILD_TIME) \
		-t $(DOCKER_IMAGE):latest -f Dockerfile .

## docker-up-simple: Start self-hosted stack (Postgres + Redis + MinIO)
docker-up-simple:
	docker compose -f deployments/docker-compose.simple.yml up -d

## docker-down-simple: Stop self-hosted stack
docker-down-simple:
	docker compose -f deployments/docker-compose.simple.yml down

## docker-up: Start full production stack (with monitoring)
docker-up:
	docker compose -f deployments/docker-compose.full.yml up -d

## docker-down: Stop full production stack
docker-down:
	docker compose -f deployments/docker-compose.full.yml down

# Database

## migrate-up: Run all pending migrations
migrate-up:
	@echo "Running migrations..."
	go run $(CMD_DIR) migrate up

## migrate-down: Rollback last migration
migrate-down:
	go run $(CMD_DIR) migrate down 1

## migrate-create name=xxx: Create a new migration pair
migrate-create:
	go run $(CMD_DIR) migrate create $(name)

# Utilities

## gen-key: Generate a new Ed25519 JWT signing key
gen-key:
	@openssl rand -base64 32 | tr -d '\n' && echo ""

## deps: Download and tidy Go module dependencies
deps:
	go mod download
	go mod tidy

## help: Show this help
help:
	@grep -E '^##' Makefile | sed 's/## //'
