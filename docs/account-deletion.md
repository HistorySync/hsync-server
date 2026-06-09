# Account deletion boundary

CE owns the generic self-service account deletion workflow.

`DELETE /api/v1/me/account` requires a normal user bearer token and a fresh
`X-HSync-Verification` step-up token. The workflow writes an audit request event,
evaluates the configured `AccountDeletionPolicy`, then writes an audit result
event for deleted, blocked, review-required, or failed outcomes.

When policy allows deletion, CE:

- soft-deletes the `users` row
- revokes all refresh tokens
- revokes all devices, which invalidates device tokens and WebSocket auth
- deletes TOTP state and backup codes
- deletes passkey credentials
- expires unconsumed passkey session challenges
- creates an `account_erasure_jobs` row with `requested_at`, `eligible_at`,
  `status`, `summary`, and `last_error`
- soft-deletes live bundle and snapshot metadata so the normal retention cleanup
  path can remove encrypted blob objects after the grace period

CE does not hard-delete blob objects during the request and does not bypass
retention cleanup. Bundle and snapshot payloads remain encrypted blobs until the
normal retention cleanup path removes eligible data.

After retention eligibility, the retention scheduler processes pending or failed
erasure jobs. It removes remaining account security rows (refresh tokens,
devices, 2FA, passkeys, notification preferences, quota rows), verifies bundle
and snapshot metadata/blob objects have been purged, anonymizes the deleted user
tombstone, and writes a final certificate JSON into `account_erasure_jobs.summary`.
The certificate records deleted categories, retained categories and reasons,
timestamps, and the zero-knowledge boundary: the server verifies opaque object
deletion by key and never parses or decrypts bundle or snapshot contents.

Every status transition is audited with `account.erasure.job_created`,
`account.erasure.job_started`, `account.erasure.job_finished`, or
`account.erasure.job_failed`. Support/admin context lookups include erasure job
status and the stored summary/certificate for the user ID.

CE exposes `provider.AccountDeletionPolicy` as the edition seam. The CE default
policy allows deletion. Commercial, team, payment, refund, or operator-review
rules belong in Enterprise or another embedding edition, not in CE.

CE also exposes `provider.AccountErasureReporter` for edition-specific retention
notes in the certificate. The CE default reporter returns no additional retained
records.
