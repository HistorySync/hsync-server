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
