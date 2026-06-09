# API Contracts

This directory publishes the maintained OpenAPI contract for the Community
Edition API surface.

## Files

- `openapi.ce.yaml`: CE-owned routes and shared contract primitives that
  Enterprise builds on.
- `openapi.ce.baseline.yaml`: Compatibility baseline for the last consciously
  accepted CE contract.

## Compatibility Guard

`go test ./docs/api` compares `openapi.ce.yaml` with
`openapi.ce.baseline.yaml`. The check fails for breaking changes that can
disrupt clients or automation:

- deleting an existing path or operation
- deleting a response field from an existing response schema
- removing a value from an existing enum
- changing an operation's effective `security` requirement

Additive changes such as new endpoints, new response fields, and broader enums
are allowed without changing the baseline.

To intentionally accept a breaking change, update `openapi.ce.yaml`, copy the
new accepted contract to `openapi.ce.baseline.yaml`, and call out the breaking
change plus the migration path in the pull request description. The baseline
update should be reviewed as an explicit API compatibility decision.

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
