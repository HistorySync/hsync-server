# Production Deployment

This guide covers the copyable production deployment template in
`deployments/docker-compose.production.yml`. It is cloud-neutral: PostgreSQL and
Redis run in compose, while object storage is an external S3-compatible service
or an operator-managed MinIO deployment.

The template does not include TLS certificates and does not store production
secrets in YAML. Put real values in an uncommitted `.env.production` file or an
equivalent secret manager workflow.

## Exposure Boundaries

Recommended public routes:

- `/healthz`
- `/readyz`
- `/api/v1/*` user and sync APIs
- `/api/v1/ws` WebSocket sync push

Recommended private routes:

- `/admin/*`
- `/api/v1/admin/*`
- `/metrics`

Keep the private routes on localhost, private networks, or VPN-only reverse
proxy rules. CE admin routes still require `X-Admin-Key`, and Enterprise admin
routes use operator/session controls, but those controls are not a substitute
for keeping the operator surface off the public internet. Metrics may include
operational posture and should be scraped only from trusted networks.

The compose template binds the backend to `127.0.0.1:8080` by default so a local
Nginx or Caddy process can terminate TLS and enforce path/IP boundaries.

## Files

- `deployments/docker-compose.production.yml`: CE application, PostgreSQL,
  Redis, one-shot doctor/migrate helpers, and a manual PostgreSQL backup job.
- `deployments/.env.production.example`: placeholder-only CE environment file.
- `deployments/reverse-proxy/nginx.conf`: TLS reverse proxy sample.
- `deployments/reverse-proxy/Caddyfile`: TLS reverse proxy sample.

## First CE Deploy

RC prerequisite:

```bash
make release-check
```

Do not tag or promote a CE release candidate unless the release gate passes for
the exact commit being shipped. RC readiness requires every release-check step
to pass: tests, OpenAPI compatibility, doctor JSON, ops rehearsal JSON, smoke,
load, and artifact verification.

Create and fill the environment file:

```bash
cp deployments/.env.production.example deployments/.env.production
openssl rand -base64 32 # HSYNC_JWT_PRIVATE_KEY
openssl rand -base64 32 # HSYNC_SECURITY_SECRET
openssl rand -base64 32 # HSYNC_ADMIN_KEY or use a longer secret
```

Render the compose file before touching infrastructure:

```bash
docker compose --env-file deployments/.env.production \
  -f deployments/docker-compose.production.yml config
```

Run the first deployment flow in this order:

```bash
# 1. doctor: preflight config, database, Redis, S3, admin, metrics, and schema posture.
docker compose --env-file deployments/.env.production \
  -f deployments/docker-compose.production.yml run --rm doctor

# 2. migrate: apply CE database migrations.
docker compose --env-file deployments/.env.production \
  -f deployments/docker-compose.production.yml run --rm migrate

# 3. smoke: verify the production-readiness smoke suite from the checked-out repo.
make test-smoke

# 4. start: run the server after doctor, migrations, and smoke are clean.
docker compose --env-file deployments/.env.production \
  -f deployments/docker-compose.production.yml up -d server
```

After start, verify from the reverse proxy host:

```bash
curl -fsS http://127.0.0.1:8080/healthz
curl -fsS http://127.0.0.1:8080/readyz
```

Then verify the HTTPS URL that users will use:

```bash
curl -fsS https://sync.example.com/healthz
curl -fsS https://sync.example.com/readyz
```

## PostgreSQL Pool Tuning

CE now exposes the PostgreSQL pool sizing through normal config/env loading so
operators do not stay pinned to the historical hard-coded `max=20` / `min=2`
across every deployment:

- `database_pool_max_conns`
- `database_pool_min_conns`
- `database_pool_max_conn_lifetime`
- `database_pool_max_conn_idle_time`
- `database_pool_health_check_period`

The defaults remain compatible with previous behavior when these settings are
omitted. Keep the duration settings at `0` to preserve pgxpool's own defaults.
For compose/env based deployments, the matching variables are:

- `HSYNC_DATABASE_POOL_MAX_CONNS`
- `HSYNC_DATABASE_POOL_MIN_CONNS`
- `HSYNC_DATABASE_POOL_MAX_CONN_LIFETIME`
- `HSYNC_DATABASE_POOL_MAX_CONN_IDLE_TIME`
- `HSYNC_DATABASE_POOL_HEALTH_CHECK_PERIOD`

## Watching Pool Pressure

When `metrics_enabled=true`, `/metrics` now exposes low-cardinality database
pool series for quick saturation checks:

- `hsync_db_pool_acquired_connections`
- `hsync_db_pool_idle_connections`
- `hsync_db_pool_total_connections`
- `hsync_db_pool_max_connections`
- `hsync_db_pool_constructing_connections`
- `hsync_db_pool_acquire_total`
- `hsync_db_pool_canceled_acquire_total`
- `hsync_db_pool_empty_acquire_total`
- `hsync_db_pool_empty_acquire_wait_seconds_total`

The redacted `doctor` output, ops summary, and support bundle also include the
configured pool values plus a safe runtime snapshot so operators can confirm
whether PostgreSQL is close to the configured ceiling without exposing the raw
DSN, password, or host secrets.

## Reverse Proxy

Use either sample as a starting point:

```bash
# nginx
deployments/reverse-proxy/nginx.conf

# Caddy
deployments/reverse-proxy/Caddyfile
```

Both examples:

- terminate TLS outside the application container
- forward `/api/v1/ws` as a WebSocket
- keep `/admin`, `/api/v1/admin`, and `/metrics` private
- leave certificate issuance and renewal to the operator

If your reverse proxy is in another container instead of on the host, place it
on an internal Docker network with the server and keep only the proxy published
to the internet.

## Backups

The CE production compose mounts `deployments/backups/postgres` at `/backup` in
the PostgreSQL service and includes a manual `postgres-backup` profile:

```bash
docker compose --env-file deployments/.env.production \
  -f deployments/docker-compose.production.yml \
  --profile backup run --rm postgres-backup
```

Back up object storage separately with the S3 tooling for your provider. Redis
stores cache/rate-limit state and append-only recovery data, but PostgreSQL plus
the object bucket are the authoritative restore set for sync metadata and blobs.

## Monthly DR Rehearsal

Run a monthly disaster-recovery rehearsal before the backup window is considered
closed. The CLI runner is read-only for production data: it does not start a
restore database, does not apply migrations, does not mutate synced blobs, and
does not replace the existing doctor or smoke suites. It only orchestrates the
existing checks in a repeatable order.

Recommended CE command:

```bash
go run ./cmd/hsync-server ops rehearsal --format human --since "$(date -u -d '24 hours ago' +%Y-%m-%dT%H:%M:%SZ)"
go run ./cmd/hsync-server ops rehearsal --format json > rehearsal-ce.json
```

Use `--manifest <file>` when verifying a previously captured restore manifest.
When no manifest is supplied, the runner generates a bounded manifest from the
current PostgreSQL metadata and immediately verifies that manifest against the
current metadata and object store. The check keeps the zero-knowledge boundary:
it verifies object existence and size only, and never downloads, parses, or
decrypts bundle or snapshot contents.

Monthly checklist:

1. Confirm the PostgreSQL backup completed and record the backup id, timestamp,
   retention policy, and restore owner.
2. Confirm the object-store backup or versioned bucket snapshot completed for
   the same recovery point.
3. Run `ops rehearsal --format json` from the release binary that would be used
   for recovery, and archive the JSON report with the backup record.
4. Review every step: build info, doctor, migrate status, schema drift, restore
   manifest verify, support bundle summary, and smoke-compatible endpoint list.
5. Treat any `blocking=true` step as a stop condition. Fix the listed action and
   rerun the full rehearsal.
6. Review `warn` steps explicitly. Common examples are Redis degradation,
   pending migrations, bounded manifest coverage, or missing ops alert
   destinations.
7. Run the existing smoke suite separately against the intended target or staging
   environment:

```bash
make test-smoke
```

8. Store the human summary, JSON report, smoke result, operator initials, and
   follow-up tickets in the monthly DR log.
