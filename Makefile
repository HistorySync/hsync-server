# HistorySync Cloud Server - Makefile
# Common development and deployment commands.

APP_NAME    := hsync-server
BUILD_DIR   := ./build
CMD_DIR     := ./cmd/hsync-server
DOCKER_IMAGE:= historysync/server

# ── Go build flags ───────────────────────────────────────────
LDFLAGS := -s -w \
	-X main.version=$(shell git describe --tags --always --dirty 2>/dev/null || echo "dev") \
	-X main.commit=$(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown") \
	-X main.buildDate=$(shell date -u +%Y-%m-%dT%H:%M:%SZ)

# ── Targets ──────────────────────────────────────────────────

.PHONY: all build run test lint clean docker-build docker-up docker-down migrate-up migrate-down

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

## lint: Run linters (requires golangci-lint)
lint:
	golangci-lint run ./...

## clean: Remove build artifacts
clean:
	rm -rf $(BUILD_DIR)

## dev: Start development environment with hot reload (requires air)
dev:
	air -c .air.toml

# ── Docker ───────────────────────────────────────────────────

## docker-build: Build Docker image
docker-build:
	docker build -t $(DOCKER_IMAGE):latest -f Dockerfile ..

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

# ── Database ─────────────────────────────────────────────────

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

# ── Utilities ────────────────────────────────────────────────

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
