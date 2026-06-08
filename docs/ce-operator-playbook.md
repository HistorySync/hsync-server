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

- `admin_key` must be configured or `/admin/*` returns forbidden.
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
- `admin_key` presence for admin and ops routes.
- PostgreSQL connectivity and Redis optional degraded mode.
- CE migration readiness: `schema_migrations` consistency with embedded
  migrations, pending migration count, and rollback availability.
- CE schema drift for required tables, columns, and indexes used by auth, sync,
  settings, passkeys, notifications, and ops history.
- S3-compatible storage config and a read-only bucket/list probe.
- Metrics path and CIDR/address parsing.
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

## 2. Admin console

The CE console is a lightweight operator shell, not a separate frontend app.
It is served directly by the server and does not persist the admin key in
browser storage.

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

## 6. Recommended incident flow

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

## 7. CE / EE boundary reminders

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
