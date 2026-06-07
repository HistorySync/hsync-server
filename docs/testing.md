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

## Integration Tests

DB-backed integration tests require Docker and do not use `-race`:

```powershell
go test -tags=integration -count=1 -timeout 300s ./pkg/repository/...
```

or:

```powershell
make test-integration
```
