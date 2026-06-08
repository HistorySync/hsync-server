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

CE does not hard-delete blob objects during the request and does not bypass
retention cleanup. Bundle and snapshot payloads remain encrypted blobs until the
normal retention cleanup path removes eligible data.

CE exposes `provider.AccountDeletionPolicy` as the edition seam. The CE default
policy allows deletion. Commercial, team, payment, refund, or operator-review
rules belong in Enterprise or another embedding edition, not in CE.
