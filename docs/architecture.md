# Architecture Overview

HistorySync Cloud Server is the Community Edition backend for HistorySync.

The CE server stores encrypted bundle and snapshot blobs, maintains metadata
indexes, and exposes HTTP and WebSocket APIs for sync, auth, snapshots, quota,
and self-hosted admin operations.

## Design Principles

- Zero-knowledge storage: the server stores encrypted payloads but does not
  parse bundle contents.
- Self-host friendly: CE should remain usable as a standalone deployment.
- Clear boundaries: HTTP concerns live in handlers, business logic in services,
  and persistence in repositories.
- Extension-friendly core: CE keeps shared seams generic so other editions can
  extend behavior without forking core packages.

## Main Components

- `cmd/hsync-server/`: application entrypoint and startup wiring
- `pkg/handler/`: Fiber handlers and route registration
- `pkg/service/`: business logic
- `pkg/repository/`: PostgreSQL and Redis persistence
- `pkg/storage/`: blob storage abstraction and S3-compatible implementation
- `pkg/provider/`: CE default providers and extension seams
- `pkg/ws/`: WebSocket hub and client management
- `migrations/`: database schema migrations

## Runtime Dependencies

The CE server is designed around these core dependencies:

- PostgreSQL for metadata and source-of-truth state
- S3-compatible object storage for encrypted bundle and snapshot payloads
- Redis as an optional cache and rate-limit backend

## API Surface

- Public health routes: `/healthz`, `/readyz`
- Main API namespace: `/api/v1`
- WebSocket push: `/api/v1/ws`
- Self-hosted admin surfaces: `/admin/*` and `/api/v1/admin/*`

See the maintained API contract in [docs/api/README.md](./api/README.md).

## Related Guides

- [client-integration.md](./client-integration.md)
- [production-deployment.md](./production-deployment.md)
- [testing.md](./testing.md)
- [ce-operator-playbook.md](./ce-operator-playbook.md)
