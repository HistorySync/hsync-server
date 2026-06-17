# HistorySync CE Operator Playbook

This playbook is for day-2 operations on the Community Edition server. It is
deliberately scoped to CE-owned operator surfaces:

- admin console
- notification outbox
- metrics
- runtime settings

Keep the CE/EE boundary explicit:

- CE owns shared admin and observability surfaces that are useful for any
  self-hosted deployment.
- EE owns payment providers, commercial order lifecycle, refunds,
  reconciliation, team billing ownership, and other commercial policy.
- If an operational task needs payment orders, provider instances, refund
  resolution, or subscription fulfillment, that belongs in
  `../hsync-enterprise`, not back in CE.

This document reflects the current implementation. It does not replace
`docs/server-design.md`; it is the short path for operating the system that
already exists.

## 1. Entry points and prerequisites

### Main routes

- Console landing page: `GET /console` by default (`web_console_path`)
- Console overview data: `GET /api/meta/overview`
- Admin API root: `/admin` with `X-Admin-Key`
- Admin API mirror for console data: `/api/v1/admin/*` with `X-Admin-Key`
- Health: `GET /healthz`
- Readiness: `GET /readyz`
- Metrics: `GET /metrics` by default when `metrics_enabled=true`

### Required operator inputs

- `admin_key` must be configured or `/admin/*` returns forbidden. Use a long
  random value and keep the admin surface behind private networking or a trusted
  reverse proxy.
- The web console must be enabled (`web_enabled=true`) to use `/console`.
- Metrics must be enabled (`metrics_enabled=true`) to scrape Prometheus data.
- Background processing must stay enabled (`background_tasks_enabled=true`) if
  you expect the notification outbox scheduler to drain work automatically.

### First check when something feels wrong

1. `GET /healthz`
2. `GET /readyz`
3. `GET /api/meta/overview`
4. `GET /admin/ops/summary` with `X-Admin-Key`

That sequence quickly separates "process is up" from "dependencies are healthy"
from "admin surface is reachable" from "the server sees its own config and
storage shape the way operators expect".

### Prometheus alerts

Deployable CE alert rules live in
`deploy/observability/ce-alert-rules.yaml`. The P0/P1/P2 incident severity
definitions live in `docs/observability/incident-severities.md`.

Each alert includes `severity`, `summary`, `runbook_url`, and `first_action`
annotations. Use `first_action` for the first operator move, then follow the
section linked by `runbook_url`.

### Redacted support bundle

When a user reports an issue that needs support review, generate a support
bundle instead of sending separate screenshots or raw admin responses:

```bash
curl -H "X-Admin-Key: $HSYNC_ADMIN_KEY" \
  "https://<server>/admin/support-bundle?since=2026-06-08T00:00:00Z"
```

The console mirror is also available at
`GET /api/v1/admin/support-bundle?since=<RFC3339>`. The optional `since` query
limits recent scheduler history to the requested time window.

The bundle includes build info, the CE doctor report, readiness summary, ops
summary, recent scheduler runs, config presence, and the OpenAPI contract
version/path. It is designed to be safe for operator-to-support sharing after
review:

- Encrypted bundle and snapshot blob contents are never exported.
- Raw webhook payloads are not exported.
- Tokens, webhook secrets, license keys, private keys, API keys, cookies, and
  authorization values are redacted.
- Email addresses are masked with a stable hash prefix rather than shown in
  plaintext.
- Audit metadata is not exported as a raw sensitive-value dump; values matching
  the redaction policy are masked.
- PostgreSQL pool settings and a safe runtime snapshot are included so support
  can see whether the deployment is near pool saturation without seeing the raw
  DSN password or other secret material.

The bundle still reveals operational posture, dependency health, feature
enablement, counts, and timing. Review it before forwarding outside the
operator/support channel, especially if your deployment names, bucket names, or
internal hostnames are sensitive in your organization.

### Deployment preflight doctor

Run the offline preflight doctor before starting a new deployment or after
changing infrastructure config:

```bash
go run ./cmd/hsync-server doctor --format human
go run ./cmd/hsync-server preflight --format json
```

The command loads the same CE config files and `HSYNC_` environment variables as
the server, but it does not start Fiber, register HTTP routes, run migrations, or
modify config. It exits with `0` when there are no `error` checks, `1` when any
`error` check is present, and `2` for command usage/output errors.

Supported output formats:

- `human`: grouped operator-readable lines with actions.
- `json`: structured report for CI/CD gates and deployment automation.

Severity meanings:

- `ok`: the check passed or the disabled state is explicitly supported.
- `warn`: deployment can run, but the operator should confirm the posture.
- `error`: fix before declaring the deployment ready.

The report never prints secret values. Secrets are shown only as `present`,
`missing`, or a short fingerprint prefix where useful.

CE preflight checks cover:

- JWT signing key and `security_secret` decoding.
- `admin_key` presence, obvious weak-format warnings, and admin route exposure
  guidance.
- PostgreSQL connectivity, configured pool sizing/lifecycle values, and a safe
  runtime pool snapshot alongside Redis optional degraded mode.
- Rate-limit fixed-window mode, limiter error fail mode, and Redis-unavailable
  fallback risk.
- CE migration readiness: `schema_migrations` consistency with embedded
  migrations, pending migration count, and rollback availability.
- CE schema drift for required tables, columns, and indexes used by auth, sync,
  settings, passkeys, notifications, and ops history.
- S3-compatible storage config and a read-only bucket/list probe.
- Metrics path and CIDR/address parsing.
- WebSocket origin policy and connection caps.
- SMTP and ops alert destination structure.
- Runtime settings for `maintenance_mode`, `signups_enabled`, and passkey
  WebAuthn origins/RP ID when PostgreSQL settings are reachable.

Use `--timeout` to bound dependency probes in CI or during an outage, for
example `--timeout 2s`. A timeout produces diagnostics instead of starting the
server and failing later.

### Upgrade and rollback workflow

Before an upgrade, inspect the target database without mutating it:

```bash
go run ./cmd/hsync-server migrate status --json
go run ./cmd/hsync-server migrate plan
go run ./cmd/hsync-server doctor --format human
```

Review `pending`, `applied`, and `rollback_available`. Unknown applied
migrations, migration name mismatches, or schema drift `error` findings mean the
database and binary are not a safe pair for the upgrade. Take the database backup
outside the server tooling; the CE migration commands do not create backups.

During the upgrade window, apply migrations:

```bash
go run ./cmd/hsync-server migrate up
```

If the upgrade fails, do not use the server to automatically roll back a
production database. Prefer restoring the operator-managed backup when data
integrity is uncertain. Use `go run ./cmd/hsync-server migrate down [n]` only
after reviewing the newest-first `rollback_available` plan and confirming the
down SQL is acceptable for the environment.

After the upgrade, rerun:

```bash
go run ./cmd/hsync-server doctor --format human
go test -tags=smoke -count=1 -timeout 300s ./cmd/hsync-server
```

### Disaster recovery rehearsal

Use a restore rehearsal before relying on a backup set for production recovery.
The built-in report stays inside the zero-knowledge boundary: it compares
PostgreSQL metadata, S3 object existence, and object size only. It never
downloads, parses, or decrypts bundle or snapshot blobs.

#### 1. Backup

Capture PostgreSQL and the object bucket from the same intended restore point.
Redis is optional in CE; treat it as cache/rate-limit state rather than the
authoritative source for user data.

For the full Docker stack, the PostgreSQL service mounts
`deployments/postgres/backup` as `/backup`. A typical operator-managed backup is:

```bash
docker compose -f deployments/docker-compose.full.yml exec postgres \
  pg_dump -U hsync -d hsync -Fc -f /backup/hsync-$(date +%Y%m%d%H%M%S).dump

mc mirror --overwrite <minio-or-s3-alias>/hsync-bundles ./backup/hsync-bundles
```

Use the S3 tool and consistency mode appropriate for your provider. The server
does not create cross-cloud backups and does not delete objects during backup.

After backup capture, generate a baseline manifest from the source environment:

```bash
curl -sS -X POST "https://<source>/admin/ops/restore-rehearsal" \
  -H "X-Admin-Key: $HSYNC_ADMIN_KEY" \
  -H "Idempotency-Key: restore-baseline-$(date +%s)" \
  -H "Content-Type: application/json" \
  -d '{"mode":"baseline","limit":1000}' \
  > restore-baseline.json
```

The `manifest` field in the response is the portable restore target. If the
report is `degraded` because metadata was truncated, raise `limit` or use an
external full manifest workflow before depending on orphan detection.

#### 2. Restore

Restore into an isolated environment first, not directly into the production
traffic target.

1. Restore PostgreSQL from the chosen dump or database snapshot.
2. Restore or mirror the object bucket/prefix for `bundles/` and `snapshots/`.
3. Start the server against the restored PostgreSQL and bucket.
4. Run `migrate status --json`, `migrate plan`, and `doctor --format human` to
   confirm the restored database and binary are a safe pair.
5. Restore Redis only if your deployment needs warm rate-limit/cache continuity;
   otherwise let Redis repopulate after cutover.

#### 3. Verify

Run dependency and consistency checks first:

```bash
curl -sS -X POST "https://<restore>/admin/ops/check" \
  -H "X-Admin-Key: $HSYNC_ADMIN_KEY" \
  -H "Idempotency-Key: restore-check-$(date +%s)"

curl -sS -X POST "https://<restore>/admin/ops/consistency?limit=1000" \
  -H "X-Admin-Key: $HSYNC_ADMIN_KEY" \
  -H "Idempotency-Key: restore-consistency-$(date +%s)"
```

Then submit the baseline manifest to the restored environment:

```bash
jq '{mode:"verify", limit:1000, manifest:.manifest}' restore-baseline.json \
  | curl -sS -X POST "https://<restore>/admin/ops/restore-rehearsal" \
      -H "X-Admin-Key: $HSYNC_ADMIN_KEY" \
      -H "Idempotency-Key: restore-verify-$(date +%s)" \
      -H "Content-Type: application/json" \
      --data-binary @- \
  > restore-verify.json
```

Review these fields before cutover:

- `summary.missing_objects`: restore referenced PostgreSQL metadata but the S3
  object is absent. Restore the object backup before accepting sync traffic.
- `summary.size_mismatch`: object size differs from the baseline manifest.
  Restore the expected object version; do not inspect or mutate blob contents.
- `summary.orphan_objects`: object storage contains keys not referenced by the
  manifest. Investigate backup timing and retention; do not delete
  automatically from this report.
- `summary.metadata_mismatch`: restored PostgreSQL active metadata differs from
  the manifest. Restore the intended database backup point or regenerate the
  baseline from the source of truth.
- `checks.redis`: Redis failures are degraded, not fatal, because PostgreSQL and
  object storage are authoritative for CE restore validation.
- `recommendations`: operator actions to complete before cutover.

Enterprise deployments add restore checks for commercial entitlement tables,
`payment_orders`, AI credit ledger/reservation tables, and
`payment_provider_instances`. Missing Enterprise tables mean the restored
environment is not ready for billing, fulfillment, credit-consuming features, or
provider routing even when CE blob metadata is intact.

#### 4. Cutover

Cut traffic over only after restore verification is `ok`, or after every
`degraded` item is understood and accepted by the operator.

1. Keep source writes paused or in maintenance mode.
2. Run smoke tests against the restored target, including `/healthz`, `/readyz`,
   `/api/meta/overview`, `/admin/ops/summary`, and a representative authenticated
   read flow.
3. Point DNS/load balancer traffic to the restored target.
4. Watch `/metrics`, notification failures, WebSocket connections, and new
   `ops` reports for the first sync cycle.
5. Keep the source backup and baseline manifest until the new environment has
   passed its retention and rollback window.

The built-in background workers deliberately process bounded batches per pass so
one scheduler tick does not monopolize PostgreSQL:

- notification outbox: 50 rows per pass
- bundle retention purge: 100 rows per pass
- snapshot retention purge: 100 rows per pass
- operational history archive/purge: 500 rows per table per pass
- account erasure jobs: 50 jobs per pass

## 2. Admin console

The CE console is a lightweight operator shell, not a separate frontend app.
It is served directly by the server and does not persist the admin key in
browser storage.

CE intentionally keeps admin authentication simple: `/admin/*` and
`/api/v1/admin/*` use `X-Admin-Key`, plus an admin-specific per-IP fixed-window
rate limit. This is not an operator account, session, or RBAC model. Treat the
admin key like a deployment secret and do not expose the admin surface directly
to the public internet.

### Main console sections

- Overview
- Settings
- Audit logs
- Security stats
- Notification failures
- Health/readiness

### Console-backed routes

- `GET /admin/stats`
- `GET /admin/settings`
- `PUT /admin/settings/:key`
- `GET /admin/audit-logs`
- `GET /api/v1/admin/security/stats`
- `GET /admin/notifications/failures`
- `POST /admin/notifications/failures/:id/retry`
- `POST /admin/notifications/failures/retry`
- `POST /admin/notifications/failures/:id/requeue`
- `POST /admin/notifications/failures/:id/discard`
- `GET /admin/ops/summary`
- `POST /admin/ops/check`
- `POST /admin/ops/consistency`
- `POST /admin/ops/restore-rehearsal`

### Mutation safeguards

High-risk admin mutations require an `Idempotency-Key` header in addition to
`X-Admin-Key`:

- Runtime option writes.
- System setting writes.
- User quota recalculation.
- Notification retry, requeue, and discard actions.
- Ops dependency, consistency, and restore rehearsal checks.

Notification outbox actions use the server idempotency store to replay matching
requests. Other CE admin mutations require the header as an operator safety
guard and audit affordance, without adding the Enterprise operator/session/RBAC
model.

Admin routes use a dedicated `ce_admin` rate-limit policy before admin-key
validation, so missing or invalid key attempts are throttled too.

### Common states

- Console loads, but data panels fail:
  usually missing or wrong `X-Admin-Key`.
- Overview is healthy but readiness is degraded:
  often Redis is unavailable while PostgreSQL and storage are still usable.
- Readiness is unhealthy with `maintenance_mode=enabled`:
  expected during controlled maintenance; writes are rejected on normal API
  paths while health and admin routes stay available.

### Risk points

- The console is an operator convenience layer, not an authority. If console
  UI and direct API responses disagree, trust the API response body.
- Admin routes are protected only by `X-Admin-Key`. Treat that key like a root
  credential and avoid exposing `/admin` publicly without reverse-proxy,
  network, or VPN controls.
- The overview and readiness probes touch database, Redis, and storage. During
  an outage they can help diagnosis, but they also confirm the outage is real;
  do not mistake probe latency for a console bug.

### Recommended troubleshooting order

1. Confirm `/console` is reachable.
2. Confirm `/admin/stats` works with the same admin key outside the console.
3. Check `/readyz` for dependency failures.
4. Check `/admin/ops/check` for dependency details.
5. Check `/admin/audit-logs` for recent config or operator changes.

## 3. Runtime settings

CE runtime settings are a typed whitelist stored in `system_settings`. They are
not a free-form config map.

### Entry routes

- `GET /admin/settings`
- `PUT /admin/settings/:key`

### Current CE-owned setting keys

- `signups_enabled`
- `maintenance_mode`
- `announcement`
- `passkey_enabled`
- `passkey_origins`
- `passkey_rp_id`
- `passkey_rp_name`

### Groups and intent

- `auth`: signup and passkey feature gating
- `security`: WebAuthn relying-party values
- `notifications`: operator announcement text
- `operations`: maintenance-mode switch

### Common states

- Default only:
  the key appears in `GET /admin/settings` with `is_set=false`.
- Overridden:
  the key has a stored value and `updated_at`.
- Unknown key:
  `PUT /admin/settings/:key` fails with the unknown-setting error.
- Invalid typed value:
  `PUT /admin/settings/:key` fails validation, for example a non-boolean string
  for `maintenance_mode`.

### Operational effects to remember

- `maintenance_mode=true` makes `/readyz` unhealthy and blocks ordinary API
  write requests, while admin and health routes remain available.
- `signups_enabled=false` blocks new self-registration but does not affect
  existing accounts.
- `passkey_enabled` only controls passkey flows that are already wired to the
  settings service. Adding a new setting key alone does not change behavior.

### Risk points

- Settings are not startup-critical config replacement. Do not move secrets or
  infrastructure topology into `system_settings`.
- Sensitive values are masked in API responses by design. Do not try to use the
  list response as a round-trip export format.
- A setting becoming writable does not mean it is safe to use for commercial
  policy. Payment, refund, or provider-routing controls belong in EE.

### Recommended troubleshooting order

1. Read the effective value via `GET /admin/settings`.
2. Confirm whether the issue is "override missing" or "feature not wired".
3. Check `GET /admin/audit-logs` for `system_setting` changes.
4. Re-check `/readyz` if the setting affects maintenance or auth posture.
5. Only then change the value with `PUT /admin/settings/:key`.

## 4. Notification outbox

The CE notification system uses a durable outbox for best-effort delivery.
Payloads are queued in PostgreSQL and delivered by the notification scheduler.

### Entry routes

- `GET /admin/notifications/failures`
- `POST /admin/notifications/failures/:id/retry`
- `POST /admin/notifications/failures/retry`
- `POST /admin/notifications/failures/:id/requeue`
- `POST /admin/notifications/failures/:id/discard`

All mutation routes require an `Idempotency-Key`.

### Lifecycle states

- `pending`: eligible for delivery when `next_retry_at` is due
- `processing`: currently claimed by a worker
- `sent`: delivered successfully
- `failed`: exhausted retries or manually left failed
- `discarded`: operator explicitly stopped further delivery

### Delivery behavior

- Automatic processing runs from the `notification-outbox` scheduler task.
- Default interval comes from `notification_outbox_interval`.
- Automatic retries stop after 5 attempts.
- Error text is sanitized before being stored or returned to operators.
- Webhook endpoint URLs and secrets come from current notification preferences
  at send time; the outbox row does not persist provider secrets.

### Common states

- Failure list is empty:
  either delivery is healthy, notifications are disabled, or the outbox is not
  being populated for the scenario you expected.
- Items stay `pending`:
  background tasks may be disabled, the interval may be too long, or no worker
  instance currently holds the scheduler lock.
- Items flip between `pending` and `processing`:
  delivery is retrying and still failing upstream.
- Items reach `failed`:
  retry budget is exhausted; operator action is now required.

### Risk points

- `retry` attempts delivery immediately and may fail again for the same root
  cause.
- `requeue` moves a failed item back to `pending`; it does not fix the cause.
- `discard` is terminal from the operator perspective. Use it only when the
  notification is obsolete or harmful to keep retrying.
- If `background_tasks_enabled=false`, the outbox will not self-drain.
- The admin console exposes per-row `retry`, `requeue`, and `discard` actions,
  plus a "Retry visible failures" batch action. Each console mutation sends a
  fresh `Idempotency-Key`, refreshes the failure list after success, and shows
  whether the server returned a fresh or replayed response.
- Console error banners show the API error code and message. Treat the server
  response as authoritative; the console does not duplicate outbox state rules.

### Recommended troubleshooting order

1. Inspect `/admin/notifications/failures`.
2. Check whether the item is a one-off failure or a broad category failure.
3. Confirm SMTP or webhook configuration in deployment config.
4. Confirm the scheduler is enabled and running.
5. Use `requeue` when the delivery path is fixed and you want normal scheduler
   behavior.
6. Use `retry` when you want an immediate operator-forced attempt.
7. Use `discard` only when the message should no longer be sent.

## 5. Metrics

Metrics are CE-owned Prometheus metrics intended for low-cardinality fleet and
service monitoring.

### Entry route

- `GET /metrics` by default

The route only exists when `metrics_enabled=true`.

### Access model

- If `metrics_allowed_cidrs` is empty, the endpoint is open to any caller that
  can reach it.
- If `metrics_allowed_cidrs` is set, the caller IP must match one of the
  configured CIDRs or addresses.
- Metrics auth is intentionally separate from `admin_key`; use network policy,
  reverse proxy controls, or private routing for scrape access.

### Main metric families

- `hsync_http_requests_total`
- `hsync_auth_failures_total`
- `hsync_uploads_total`
- `hsync_quota_reservations_total`
- `hsync_scheduler_runs_total`
- `hsync_scheduler_run_duration_seconds`
- `hsync_scheduler_failures_total`
- `hsync_notification_delivery_total`
- `hsync_idempotency_events_total`
- `hsync_readiness_dependency_status`
- `hsync_rate_limit_errors_total`
- `hsync_rate_limit_redis_fallback_active`
- `hsync_websocket_connections_active`
- `hsync_websocket_upgrade_rejections_total`
- `hsync_db_pool_acquired_connections`
- `hsync_db_pool_idle_connections`
- `hsync_db_pool_total_connections`
- `hsync_db_pool_max_connections`
- `hsync_db_pool_constructing_connections`
- `hsync_db_pool_acquire_total`
- `hsync_db_pool_canceled_acquire_total`
- `hsync_db_pool_empty_acquire_total`
- `hsync_db_pool_empty_acquire_wait_seconds_total`

### Common states

- 404 on `/metrics`:
  metrics are disabled or the path was overridden.
- 403 on `/metrics`:
  caller IP is outside `metrics_allowed_cidrs`.
- Metrics scrape works but readiness is unhealthy:
  expected during partial outages; metrics and readiness are separate surfaces.

### Risk points

- Do not expose `/metrics` directly to the public internet.
- Keep labels low cardinality. If a future change needs per-user or per-order
  labels, stop and redesign it before landing in CE metrics.
- Readiness gauge values are normalized into `ok`, `disabled`,
  `not_configured`, and `error`; use the raw `/readyz` body when you need exact
  text.

### Recommended troubleshooting order

1. Confirm the route exists and the scrape target gets 200.
2. Check whether the problem is endpoint access or missing series.
3. Check `/readyz` for the same dependency family.
4. Check scheduler metrics if the symptom involves periodic work.
5. Check notification delivery metrics if the symptom involves outbox failures.
6. For PostgreSQL pressure, compare `hsync_db_pool_acquired_connections` and
   `hsync_db_pool_total_connections` against `hsync_db_pool_max_connections`,
   then look for growth in `hsync_db_pool_empty_acquire_total` or
   `hsync_db_pool_empty_acquire_wait_seconds_total`.

### Alert coverage

The CE rule file covers these operator-facing alerts:

- `HSyncCEReadyzCritical`: critical database or storage readiness failure.
- `HSyncCES3Unavailable`: S3-compatible object storage readiness failure.
- `HSyncCENotificationFailureSpike`: notification delivery failure spike.
- `HSyncCEQuotaReservationRollbackSpike`: upload quota reservation rollback spike.
- `HSyncCESchedulerStale`: selected scheduler tasks have not reported a recent run.

Keep additional alert labels low-cardinality. Dependency, task, category,
result, and severity are acceptable; user, device, bundle, snapshot, request,
email, and object ids are not.

## 6. Rate-Limit Degradation

CE uses the shared fixed-window limiter for public auth and authenticated API
budgets. With Redis available, counters are shared across server instances.
Without Redis, `rate_limit_redis_unavailable_fallback` controls the startup
fallback:

- `memory`: use in-process fixed-window buckets. This preserves local throttling
  for a single instance, but each instance counts independently in a fleet.
- `deny`: fail closed for rate-limited routes until Redis is restored.
- `disable`: remove limiter enforcement. Use only when a trusted gateway owns
  equivalent rate limiting.

Limiter backend errors during request handling follow fail-mode settings:

- `rate_limit_fail_mode` is the default bucket behavior.
- `rate_limit_public_auth_fail_mode` applies to public auth buckets such as
  login, register, password reset, and verification-send limits.
- `fail_open` allows the request and logs a warning.
- `fail_closed` rejects the request with `RATE_LIMITED`.

Doctor reports the active policy, the fixed-window algorithm, Redis-backed
versus memory-backed scope, and whether fail-open or memory fallback creates a
multi-instance risk. Metrics expose:

- `hsync_rate_limit_errors_total{policy,fail_mode,action}` for limiter backend
  errors.
- `hsync_rate_limit_redis_fallback_active{mode}` when the process starts without
  Redis and activates `memory`, `deny`, or `disable`.

When Redis is down, do not assume fleet-wide rate limiting still exists unless
the fallback is `deny` or an external gateway enforces the same budget.

Operational exposure checklist:

- Admin: expose only over private network/VPN or a trusted reverse proxy path;
  never rely on the admin key alone for internet-facing deployments.
- Metrics: keep `/metrics` on an internal scrape path, use
  `metrics_allowed_cidrs`, and avoid exposing it publicly.
- WebSocket: configure `websocket_allowed_origins` for browser origins and size
  per-process connection caps to the load balancer topology.

## 7. WebSocket Push Hardening

The push endpoint is `GET /ws/push`. It authenticates with the per-device
WebSocket token, preferably in `Authorization: Bearer <device_token>`. The
legacy `?token=` query parameter remains available for older clients.

Device token lifecycle:

- Clients obtain or rotate the token with `POST /api/v1/devices/:uuid/token`
  under a user JWT session.
- The raw token is returned only once; CE stores only `SHA-256(token)` plus a
  server-enforced expiry timestamp.
- CE currently enforces a 24 hour token lifetime. A WebSocket reconnect after
  expiry must request a fresh device token first.
- Revoked devices cannot refresh tokens and existing WebSocket auth stops
  working as soon as the old token is rejected.
- Token issuance and rejection are audited with device UUID, platform, and
  rejection reason only; no plaintext token is logged or exported.

Origin policy:

- `websocket_origin_check_disabled=false` by default.
- Requests without an `Origin` header are allowed and rely on device-token auth.
- Browser requests with `Origin` are allowed only when the origin matches the
  request host, unless `websocket_allowed_origins` is configured.
- `websocket_allowed_origins` entries must be full `http` or `https` origins
  with no path, query, or fragment, such as `https://app.example.com`.

Connection caps:

- Multiple tabs and multiple devices for the same user are allowed.
- `websocket_max_connections_per_user` caps the total active WebSocket
  connections for one user across devices and tabs.
- `websocket_max_connections` caps active WebSocket connections for the process.
- Set either cap to `0` only when intentionally disabling that limit.

Rejected upgrades:

- Bad browser origins are rejected with 403 and counted as
  `hsync_websocket_upgrade_rejections_total{reason="origin"}`.
- Global or per-user capacity rejections return 429 and are counted as
  `hsync_websocket_upgrade_rejections_total{reason="capacity"}`.
- Active connections are exported as `hsync_websocket_connections_active`.

In multi-instance deployments these caps are per process. There is no Redis or
cluster-wide WebSocket connection counter in CE, so size per-instance limits
with the load balancer topology in mind.

### Recommended troubleshooting order

1. Check `/metrics` for active WebSocket connections and rejection reasons.
2. Confirm browser `Origin` exactly matches one of `websocket_allowed_origins`
   or the public request host.
3. Confirm the device token is fresh and the device has not been revoked.
4. If `/api/v1/devices/:uuid/token` returns 429, back off and retry instead of
   forcing repeated refresh attempts.
5. Raise per-user caps only when expected multi-tab/device behavior needs it.
6. Raise global caps only after checking CPU, memory, and file descriptor headroom.

## 7. Recommended incident flow

When an operator or AI agent is dropped into a CE incident, use this order:

1. `GET /healthz`
2. `GET /readyz`
3. `GET /api/meta/overview`
4. `GET /admin/ops/summary`
5. `GET /admin/audit-logs`
6. `GET /admin/settings`
7. `GET /admin/notifications/failures`
8. `GET /metrics` if enabled

That order usually tells you whether the issue is:

- dependency outage
- maintenance toggle
- admin auth problem
- notification backlog
- config drift
- normal degraded mode because Redis is optional

## 8. CE / EE boundary reminders

Keep these lines hard:

- CE admin console can show shared health, settings, audit, security, and
  notification state.
- CE should not grow payment-order dashboards, refund workflows, provider
  instance editors, or subscription-commercial policy.
- EE may extend the same console shell, but the commercial actions and data
  models must stay in `../hsync-enterprise`.

If a future task asks for:

- payment reconciliation
- refund lifecycle
- provider instance routing
- manual plan grants tied to commercial fulfillment

route that work to EE first and keep CE generic.
