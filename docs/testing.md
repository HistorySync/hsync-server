# Testing

## Unit Tests

Linux and CI should run the race-enabled unit tier:

```powershell
go test -race -count=1 -timeout 60s ./...
```

or:

```powershell
make test
```

On Windows development machines without a working CGO/C toolchain, the Go race
detector can fail before tests run, including with exit code `0xc0000139`. Treat
that as a local toolchain limitation, not a product test failure. Use the
PowerShell helper's non-race tier for local feedback:

```powershell
.\scripts\dev.ps1 test-no-race
```

Before merging shared or concurrent changes, verify the race-enabled tier on a
Linux workstation or in CI.

## Smoke Tests

Production readiness smoke checks are gated behind the `smoke` build tag and
require Docker because they start a throwaway PostgreSQL 16 container and apply
the embedded migrations before wiring the HTTP app.

Run the CE smoke suite:

```powershell
go test -tags=smoke -count=1 -timeout 300s ./cmd/hsync-server
```

or:

```powershell
make test-smoke
```

On Windows:

```powershell
.\scripts\dev.ps1 test-smoke
```

The CE smoke suite verifies that all CE migrations apply, the startup wiring can
build a Fiber app with PostgreSQL and fake blob storage, and the public
production probes/routes respond: `/healthz`, `/readyz`, `/api/meta/overview`,
`/console`, and `/metrics`. It also re-reads migration status after applying the
embedded SQL and fails if `schema_migrations` is inconsistent, any CE migration
is still pending, or the required CE schema drift checks report missing tables,
columns, or indexes.

Smoke tests do not use real external providers. Redis is intentionally left nil
to exercise the supported degraded path, and blob storage uses an in-memory fake.
If Docker cannot provide Linux containers locally, run this suite in CI or on a
Linux workstation.

## Client Conformance Tests

The CE client conformance suite is an opt-in integration tier that exercises the
minimal protocol surface a desktop app or browser extension depends on:
password login, refresh, device token issuance, bundle upload/download,
snapshot upload/download, WebSocket push delivery, step-up verification, device
revocation, and selected negative error cases.

Run it with:

```powershell
go test -tags=conformance -count=1 -timeout 300s ./pkg/clientconformance
```

or:

```powershell
make test-conformance
```

The suite starts a real Fiber listener, a throwaway PostgreSQL 16 container, and
an in-memory blob store. It validates that selected error responses match both
the `pkg/apierrors` catalog and the statuses declared in `docs/api/openapi.ce.yaml`.

Local auth overrides:

- `HSYNC_CONFORMANCE_2FA_MODE=real` uses the real TOTP setup, login challenge,
  and `/api/v1/auth/verify` flow before sensitive device actions.
- `HSYNC_CONFORMANCE_2FA_MODE=skip` mints a short-lived step-up token directly
  in the harness so clients can keep testing revoke flows without a real TOTP UI.
- `HSYNC_CONFORMANCE_PASSKEY_MODE=disabled` asserts the CE default behavior that
  `/api/v1/auth/passkeys/login/begin` returns `PASSKEY_DISABLED`.
- `HSYNC_CONFORMANCE_PASSKEY_MODE=skip` skips that negative passkey assertion.

Turnstile is always faked inside this suite with a fixed accepted token so local
and CI runs do not depend on external verification. If Docker is unavailable,
the conformance suite skips instead of failing the default test tier.

## Release Candidate Gate

RC builds must pass the unified release gate before they are tagged or promoted.
The gate is intentionally strict: it does not update baselines, does not push
artifacts, and fails immediately if any check is not clean.

Run it from the repository root:

```powershell
make release-check
```

or:

```powershell
.\scripts\release-check.ps1 -ReportPath build/release-report-ce.json
```

Standalone supply-chain commands are also available:

```powershell
make vuln-check
make sbom
```

The CE release gate runs these checks in order against a temporary local stack:

- `go test -count=1 -timeout 60s ./...`
- `go test ./docs/api`
- `go run ./cmd/hsync-server doctor --format json`
- `go run ./cmd/hsync-server ops rehearsal --format json`
- `go test -tags=smoke -count=1 -timeout 300s ./cmd/hsync-server`
- `go run ./cmd/loadtest -json`
- `pwsh -ExecutionPolicy Bypass -File .\scripts\supply-chain.ps1`

Every step must pass. The gate does not skip failures, does not update
baselines, and does not mark the RC green if any step fails.

The script writes a machine-readable report to `build/release-report-ce.json`
with:

- `commit`
- `version`
- `edition`
- `build_info`
- `artifact_manifest_path`
- `artifacts.binary.sha256`
- `artifacts.image.digest`
- `artifacts.sbom`
- `vulnerability_reports`
- `passed_steps`
- `failed_steps`
- `duration_ms`

Per-step stdout/stderr logs are written under `build/release-check/`. CI keeps
those artifacts even when the gate fails so release blockers can be inspected
without rerunning locally.

The release artifact manifest itself is written to
`build/release-artifact-manifest-ce.json`. Use
`docs/release-verification.md` for binary/image-to-commit verification steps.

## Release Capacity Rehearsal

The CE release gate also includes a repeatable local smoke+load rehearsal. It
targets a running local server, uses normal auth flows, and verifies the
specific pre-release behaviors that are easy to regress under small bursts:
register/login, bundle upload/download/list, snapshot upload, WebSocket
connection caps, rate-limit fallback visibility, notification outbox drain, and
quota rollback accounting.

Start the local stack with the load overlay merged on top of the usual config:

```powershell
$env:HSYNC_CONFIG_EXTRA_FILES="config.load"
go run ./cmd/hsync-server
```

The `configs/config.load.yaml` overlay intentionally keeps this environment
local-only: metrics are enabled, Redis is disabled to expose fallback mode,
WebSocket caps are low enough to trigger deterministic rejections, background
tasks stay on, and notification draining is shortened to one second polling.

Run the rehearsal against that server:

```powershell
go run ./cmd/loadtest -json
```

or:

```powershell
make loadtest
```

The report includes per-scenario `p50`, `p95`, `error_rate`, `rejections`, a
roll-up `rejection_reasons` map, explicit `status_classes` counts for
`http_403`, `http_429`, and `http_5xx`, `quota_rollback_count`, and the
`hsync_rate_limit_redis_fallback_active` state captured from `/metrics`.

Recommended release thresholds for local rehearsal:

- `error_rate` should stay at `0%` for `ce_register_login`,
  `ce_bundle_snapshot_sync`, and `ce_notification_outbox_drain`.
- `ce_ws_connect_cap` should show at least one rejection when `HSYNC_LOAD_WS_ATTEMPTS`
  is greater than `websocket_max_connections_per_user`; that confirms the cap is
  enforced instead of silently over-admitting sockets.
- `ce_rate_limit_fallback` may show rejections, but they should be explicit
  `HTTP_429` or `RATE_LIMITED` outcomes rather than transport errors or `5xx`
  responses.
- `status_classes.http_5xx` should remain `0`. Non-zero `http_403` or
  `http_429` counts are acceptable only when they match the scenario you
  intentionally triggered.
- `quota_rollback_count` should remain `0` for the default rehearsal. A non-zero
  value means quota reservations were created and then rolled back, which is a
  release blocker unless the run intentionally exercised a failing storage path.
- `rate_limit_fallback` should match expectations for the local topology. With
  `config.load.yaml`, `memory=true` is expected because Redis is disabled.
- `notification_outbox failed` should remain `0`, and `sent` should increase
  after the quota-warning trigger upload.

Interpret the output as a release confidence check, not as a production
saturation benchmark. A rising `p95` with zero errors usually means the machine
is noisy or under-provisioned locally; `5xx`, missing notification drains,
unexpected rejection reasons, or non-zero rollback counts indicate a behavioral
regression worth fixing before release.

If the rehearsal fails, start here:

- `status_classes.http_429` or `RATE_LIMITED` spikes: inspect `/metrics` for
  `hsync_rate_limit_redis_fallback_active` and confirm the local fail mode in
  `configs/config.load.yaml` matches the run you intended.
- `status_classes.http_403` on WebSocket/device flows: confirm the device token
  was freshly minted and inspect `hsync_websocket_upgrade_rejections_total` plus
  the WebSocket limits in `configs/config.load.yaml`.
- `status_classes.http_5xx` or non-zero `quota_rollback_count`: inspect the
  server log around bundle/snapshot writes and the
  `hsync_quota_reservations_total{result="rollback"}` metric to see whether
  storage writes or metadata commits failed mid-flight.
- `ce_notification_outbox_drain` stalls: query
  `/admin/exports/operational-records?record_type=notification_outbox`, confirm
  `background_tasks_enabled=true`, and cross-check the notification guidance in
  `docs/ce-operator-playbook.md#4-notification-outbox`.

## Upgrade Validation Flow

Before applying a release to a persistent database, run the read-only migration
plan and doctor checks against the target environment:

```powershell
go run ./cmd/hsync-server migrate status --json
go run ./cmd/hsync-server migrate plan
go run ./cmd/hsync-server doctor --format human
```

`pending` is what `migrate up` would apply, `applied` is what the database has
already recorded, and `rollback_available` is the newest-first list that
`migrate down` can use. Treat inconsistent tracking, unknown applied versions,
or schema drift `error` findings as blockers until the database and binary match.

Apply migrations during the upgrade window:

```powershell
go run ./cmd/hsync-server migrate up
```

Backups and production rollback are intentionally out of band. If an upgrade
fails before traffic resumes, restore the operator-managed backup when that is
the safest recovery path. Use `migrate down [n]` only for a deliberate rollback
after reviewing `rollback_available` and the down SQL for data-loss effects.

After the upgrade, rerun doctor and the CE smoke suite:

```powershell
go run ./cmd/hsync-server doctor --format human
go test -tags=smoke -count=1 -timeout 300s ./cmd/hsync-server
```

## Integration Tests

DB-backed integration tests require Docker and do not use `-race`:

```powershell
go test -tags=integration -count=1 -timeout 300s ./pkg/repository/...
```

or:

```powershell
make test-integration
```
