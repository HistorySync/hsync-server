# API Contracts

This directory publishes the maintained OpenAPI contract for the Community
Edition API surface.

## Files

- `openapi.ce.yaml`: CE-owned routes and shared contract primitives that
  Enterprise builds on.

## Route Ownership

- CE owns the public self-hosted baseline routes exposed by
  `pkg/handler/handler.go`.
- EE may overlap selected CE paths by excluding CE registrations through
  `RouteExclusions` and mounting Enterprise handlers first.
- When the same path exists in both editions, the CE contract documents the CE
  fallback behavior only. The Enterprise-specific behavior is documented in the
  Enterprise repository's `docs/api/openapi.ee.yaml`.

## Overlap Notes

- `/api/v1/auth/*`, `/api/v1/devices/*`, `/api/v1/quota`, `/api/v1/billing/*`,
  and admin routes under `/admin` or `/api/v1/admin` may be replaced or
  extended by Enterprise.
- CE remains the owner of the shared error envelope and the baseline bundle,
  snapshot, passkey, notification preference, and self-hosted admin-ops
  semantics.
