# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working in this repository.

## Project Overview

HistorySync Cloud Server is the Community Edition backend for HistorySync. It is a Go 1.23+ service built on Fiber v3 that stores encrypted bundle blobs, maintains metadata indexes, and exposes HTTP + WebSocket APIs for sync, snapshot, auth, quota, and billing flows.

This repository is the CE base that the Enterprise server builds on top of.

## Development Commands

Run commands from the repository root.

```bash
# Install / sync dependencies
go mod download
go mod tidy

# Run the server
make run
# or
go run ./cmd/hsync-server

# Hot reload (requires air)
make dev

# Build
make build

# Run tests
make test
# or
go test -race -count=1 -timeout 60s ./...

# Lint (requires golangci-lint)
make lint

# Database migrations
go run ./cmd/hsync-server migrate up
go run ./cmd/hsync-server migrate down 1
go run ./cmd/hsync-server migrate create <name>

# Docker stacks
make docker-up-simple
make docker-down-simple
make docker-up
make docker-down
```

## Project Structure

```text
cmd/hsync-server/          # Server entrypoint
pkg/auth/                  # Auth middleware and token manager
pkg/config/                # Config loading and config types
pkg/handler/               # Fiber handlers and route registration
pkg/model/                 # API and persistence models
pkg/provider/              # Provider interfaces and default CE implementations
pkg/repository/            # PostgreSQL and Redis access layer
pkg/service/               # Business logic layer
pkg/storage/               # Blob storage interface and S3 implementation
pkg/ws/                    # WebSocket hub and client management
migrations/                # SQL migrations
deployments/               # Docker compose stacks
docs/server-design.md      # Product and architecture design notes
configs/                   # Example and environment-specific configs
```

## Architecture

- `cmd/hsync-server/main.go` wires config, PostgreSQL, Redis, blob storage, services, WebSocket hub, and Fiber app startup.
- `pkg/handler/` is the HTTP boundary. Keep request parsing, auth gating, and response shaping here.
- `pkg/service/` owns business rules such as bundle lifecycle, snapshot logic, auth flows, quota checks, and billing orchestration.
- `pkg/repository/` owns database and Redis persistence details. Keep SQL and storage-specific logic here rather than in handlers.
- `pkg/storage/` abstracts blob storage. Treat bundle payloads as opaque encrypted blobs.
- `pkg/ws/` manages push notifications to connected devices.
- `pkg/provider/` defines extension seams used by different deployments. In CE, default providers should stay simple and self-contained.

## Product Constraints

- The server is a metadata index and encrypted blob store, not a bundle parser. Do not introduce code that inspects or transforms bundle contents unless the user explicitly requests a product change.
- Preserve the zero-knowledge assumption described in `docs/server-design.md`: the server should not require plaintext access to synced history data.
- Prefer keeping the CE server usable as a standalone self-hosted deployment.
- When a change may belong only in the commercial product, keep the CE side generic and extension-friendly rather than embedding enterprise-only behavior directly.

## Routing And API Notes

- Public health routes live at `/healthz` and `/readyz`.
- Main API routes are mounted under `/api/v1` in `pkg/handler/handler.go`.
- JWT-protected groups should continue to use `auth.AuthMiddleware(...)` rather than ad hoc token parsing.
- Keep error response shapes consistent with the existing `ErrorHandler` in `pkg/handler/handler.go`.
- For new endpoints, follow the existing separation: handler for HTTP concerns, service for logic, repository for persistence.
- Avoid broad route reorganizations unless the user explicitly asks for API restructuring.

## Storage And Data Rules

- Bundle and snapshot payloads are immutable uploads from the server's perspective. Do not add in-place mutation flows unless explicitly required.
- Prefer metadata-driven queries and indexing over blob inspection.
- Keep PostgreSQL as the source of truth for metadata.
- Redis is optional. If behavior must degrade when Redis is unavailable, preserve the existing graceful-degradation approach.
- S3-compatible storage is a core dependency. Fail clearly when storage is unavailable rather than silently skipping writes.

## Coding Standards

- Follow the existing style in each file. Do not reformat unrelated code.
- Use ASCII in code and comments.
- Write code comments in English.
- Prefer small, local changes over broad refactors.
- Keep dependency injection explicit through existing `Deps` structs and constructors.
- Avoid introducing new abstractions unless they remove real duplication in this codebase.
- Keep CE code free of enterprise-specific product policy unless the repository already exposes it through provider interfaces.
- When asked to provide a commit message, output a single English line in the format `<type>(<scope>): <subject>` directly in chat. Do not run git commit commands unless explicitly requested.
- Preferred commit types are `feat`, `fix`, `perf`, `refactor`, `docs`, `chore`, `build`, `ci`, `test`, and `style`.
- Keep commit scopes lowercase and aligned with the touched area, such as `auth`, `handler`, `service`, `repository`, `storage`, `ws`, `config`, `docker`, `migrations`, or `tests`.
- Write commit subjects in imperative mood, with a lowercase first letter and no trailing period.

## Go And Fiber Notes

- Target Go 1.23+.
- Keep Fiber configuration changes minimal unless there is a clear runtime, API, or deployment need.
- Reuse existing request/response patterns in `pkg/handler/` before introducing helper frameworks.
- Prefer standard library types and existing project dependencies over adding new packages.
- Keep context propagation intact for repository and storage calls.
- When adding concurrent or background behavior, make shutdown and cancellation behavior explicit.

## Validation Expectations

- After code changes, run targeted tests first when possible, then broader `go test` coverage if the affected area justifies it.
- For cross-cutting backend changes, prefer `make test`.
- If you change build wiring, startup flow, Docker files, or configuration loading, prefer a build check with `make build`.
- If validation fails for unrelated reasons, report that clearly instead of masking it.

## Agent Usage

- Do not use the Agent tool or spawn subagents for any task in this project. Use direct tools instead.

## Working Norms

- Assume the user usually wants direct implementation, not just discussion, unless they ask for options first.
- Do not overwrite or revert user changes outside the requested scope.
- Avoid destructive git commands unless the user explicitly asks for them.
- If `docs/server-design.md` and current code disagree, treat the code as the current implementation and the doc as design intent unless the user says the doc is authoritative.
- Keep recommendations tightly scoped to this server and its CE role in the larger HistorySync stack.
