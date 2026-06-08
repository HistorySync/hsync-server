# HistorySync Alert Severity Guide

This guide defines the incident severity used by the Prometheus alert rules in
`deploy/observability`. Keep the severity small and operational: page on P0,
interrupt the owning operator on P1, and queue P2 for normal working hours.

## Severity Levels

| Level | Meaning | Examples | Target first response |
| --- | --- | --- | --- |
| P0 | The service is unable to accept safe production traffic or can lose acknowledged work. | `/readyz` critical dependency failure, S3 unavailable, PostgreSQL unavailable. | Immediate page; keep or enter maintenance mode until the dependency is healthy. |
| P1 | A shared workflow is degraded and needs same-day operator action before backlog or customer impact grows. | Notification failure spike, quota reservation rollback spike, scheduler stale. | Triage within 30 minutes; identify whether the issue is dependency, config, or code regression. |
| P2 | A non-urgent operational warning, capacity drift, or isolated repair item. | One-off notification failures, optional Redis degraded mode, expected dry-run retention findings. | Review during business hours and fold into normal operations. |

## Handling Order

1. Confirm liveness and readiness: `GET /healthz`, then `GET /readyz`.
2. Check dependency detail through `GET /admin/ops/summary` or
   `POST /admin/ops/check`.
3. Stabilize customer-facing writes first. Use maintenance mode when continuing
   write traffic could create more failed uploads, stuck reservations, or
   misleading operator history.
4. Check the alert's `first_action` annotation and linked runbook.
5. For payment or Enterprise license incidents, switch to the Enterprise runbook
   before making commercial state changes.
6. Record the action taken, the root cause, and whether the alert expression or
   threshold needs adjustment.

## Alert Rule Layout

- CE rules live in `deploy/observability/ce-alert-rules.yaml`.
- Enterprise rules live in the Enterprise repository at
  `deploy/observability/enterprise-alert-rules.yaml`.
- Alert labels intentionally stay low-cardinality. Use dependency, task,
  provider, job, status, and bucket labels only; do not add user, order, email,
  request, device, or object identifiers.
