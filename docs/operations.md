# Multi-Instance Operations Notes

This note complements [ce-operator-playbook.md](ce-operator-playbook.md) for CE deployments that run more than one server instance against the same PostgreSQL database.

## Scheduler coordination

- CE background tasks do not elect a leader through Redis or an external coordinator.
- Each scheduler task uses a PostgreSQL advisory lock with a stable task-specific key.
- In a healthy fleet, only one instance runs a given task at a time, but different tasks can run on different instances.
- A rolling restart can move task ownership between instances; treat that as normal behavior.
- If one worker crashes after durable state changes but before it records its own
  run summary, a later instance may rerun the task and produce a zero-work pass.
  Treat the persisted row state as the source of truth, not any single process's
  in-memory view.

## Notification outbox workers

- Notification work distribution is PostgreSQL-backed, not in-memory.
- Outbox claim queries use `FOR UPDATE SKIP LOCKED`, so multiple workers split due rows without double-claiming the same notification.
- Duplicate delivery prevention depends on durable row state in PostgreSQL, not on per-process memory.
- If one worker is busy sending a claimed notification, another worker should see different rows or no row, not the same claim.

## Retention and erasure jobs

- Retention cleanup and account erasure rely on PostgreSQL row-state transitions as the source of truth.
- Account erasure uses `MarkRunning` as the race gate; only one worker should transition a pending or failed job into `running`.
- Durable completion is the important boundary. If a worker fails after `MarkCompleted` succeeds, later workers should treat the job as already finished.
- If a worker fails before durable completion, the row may remain retryable or require operator follow-up depending on the last persisted state.

## Redis degraded mode

- Redis is optional in CE for cache and rate-limit support; it is not the scheduler coordination mechanism.
- If Redis is unavailable but PostgreSQL and object storage are healthy, readiness and ops checks may report `degraded` rather than `unhealthy`.
- In that mode, shared fleet-wide rate limiting may fall back to per-process memory or the configured fail mode. Do not assume cross-instance counters still exist.
- Redis being down does not change scheduler ownership: advisory locks still
  coordinate background work, and the outbox / retention / erasure state
  transitions remain the duplicate-prevention boundary.

## Failure expectations

- Process crashes do not permanently strand PostgreSQL advisory locks; the backing session lock is released when the connection drops.
- Process crashes can still leave application work half-finished if the durable state transition did not happen yet.
- Notification retries and admin mutation endpoints should always be treated as replayable and idempotency-aware, not exactly-once at the process level.
- In CE, prefer verifying the persisted PostgreSQL state first, then deciding whether to rerun or manually repair background work.

## Operator checklist

- Keep `background_tasks_enabled=true` on every instance that is expected to participate in background work.
- Watch scheduler freshness and notification failure metrics per deployment, not per single node.
- When Redis is down, confirm the configured rate-limit fallback before assuming the fleet is safe to leave running unchanged.
- For incidents involving duplicate-looking background work, inspect the persisted job or outbox row state before changing advisory-lock strategy.
