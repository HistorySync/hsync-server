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
- `../hsync-enterprise/deployments/docker-compose.production.overlay.yml`:
  Enterprise overlay that reuses the CE production template.

## First CE Deploy

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

## Enterprise Deploy

Enterprise uses the CE production template plus the Enterprise overlay. Fill
both environment files:

```bash
cp ../hsync-server/deployments/.env.production.example \
  ../hsync-server/deployments/.env.production
cp deployments/.env.production.example deployments/.env.production
```

Run the same flow with both compose files and both env files:

```bash
# 1. doctor: merged CE + Enterprise preflight.
docker compose \
  --env-file ../hsync-server/deployments/.env.production \
  --env-file deployments/.env.production \
  -f ../hsync-server/deployments/docker-compose.production.yml \
  -f deployments/docker-compose.production.overlay.yml \
  run --rm doctor

# 2. migrate: Enterprise applies pending CE migrations first, then pending EE migrations.
docker compose \
  --env-file ../hsync-server/deployments/.env.production \
  --env-file deployments/.env.production \
  -f ../hsync-server/deployments/docker-compose.production.yml \
  -f deployments/docker-compose.production.overlay.yml \
  run --rm migrate

# 3. smoke: Enterprise smoke suite.
make test-smoke

# 4. start.
docker compose \
  --env-file ../hsync-server/deployments/.env.production \
  --env-file deployments/.env.production \
  -f ../hsync-server/deployments/docker-compose.production.yml \
  -f deployments/docker-compose.production.overlay.yml \
  up -d server
```

`hsync-enterprise migrate up` records CE migrations in `schema_migrations` and
Enterprise migrations in `enterprise_schema_migrations`. For rollback planning,
use `hsync-enterprise migrate status --json` or the overlay's migrate service
with `command: ["migrate", "status", "--json"]`.

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
