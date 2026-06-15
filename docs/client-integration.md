# Client Integration

This document describes the minimum Community Edition client contract for a
desktop app, CLI, browser extension, or other sync-capable client.

The server stores encrypted bundle and snapshot blobs plus metadata. It does
not inspect or transform bundle contents.

## Scope

This guide covers:

- email/password registration and login
- refresh token usage
- device token issuance
- bundle and snapshot sync
- WebSocket push handling
- step-up verification for sensitive actions
- device revocation recovery

This guide does not cover:

- a full SDK
- UI decisions
- encryption format design

## Minimal flow

### 1. Register or log in

Use one of:

- `POST /api/v1/auth/register`
- `POST /api/v1/auth/login`

When Turnstile is enabled, include `turnstile_token`.

Successful password auth returns:

- `access_token`
- `refresh_token`
- `expires_in`

If the account has TOTP enabled, `POST /api/v1/auth/login` returns:

- `requires_2fa=true`
- `challenge`
- `expires_in`

Complete that challenge with `POST /api/v1/auth/login/2fa`.

### 2. Keep the access token fresh

Access tokens are short-lived bearer tokens for normal HTTP API calls.

Before or after expiry, exchange the current refresh token with:

- `POST /api/v1/auth/refresh`

Replace the in-memory access token with the new `access_token`.

### 3. Register one logical client device

Generate and persist a stable UUID per installed profile, then request a
device token with:

- `POST /api/v1/devices/{uuid}/token`

Recommended request fields:

- `device_name`
- `platform`
- `app_version`

The response includes:

- `device_token`
- `expires_in`
- `device`

Use the user `access_token` for REST API calls. Use the `device_token` only
for WebSocket authentication.

### 4. Open push delivery

Connect to:

- `GET /ws/push`

with:

- `Authorization: Bearer <device_token>`

Current CE push message types:

- `bundle_uploaded`
- `snapshot_uploaded`
- `device_revoked`

Treat push as an invalidation signal. Re-fetch metadata or content through HTTP
instead of assuming the push payload is a full state sync.

### 5. Upload and fetch bundles

Upload encrypted bundle blobs with:

- `POST /api/v1/bundles`

Required multipart fields:

- `bundle`
- `bundle_id`
- `device_uuid`
- `lamport_lo`
- `lamport_hi`
- `event_count`
- `cipher_id`
- `key_generation`

Read bundle metadata and bytes with:

- `GET /api/v1/bundles`
- `GET /api/v1/bundles/{id}`

### 6. Upload and fetch snapshots

Upload encrypted snapshot blobs with:

- `POST /api/v1/snapshots`

Required multipart fields:

- `snapshot`
- `snapshot_id`
- `base_hlc`
- `cipher_id`
- `key_generation`

Read snapshot metadata and bytes with:

- `GET /api/v1/snapshots/latest`
- `GET /api/v1/snapshots/{id}`

### 7. Perform step-up for sensitive actions

Some routes require a short-lived `X-HSync-Verification` header.

For TOTP accounts, call:

- `POST /api/v1/auth/verify`

with:

- `method=totp`
- `code`

The response returns `verification_token`. Send it as:

- `X-HSync-Verification: <verification_token>`

Example sensitive route:

- `POST /api/v1/devices/{uuid}/revoke`

### 8. Handle revocation

Device revocation effects:

- the revoke request succeeds through `POST /api/v1/devices/{uuid}/revoke`
- online devices receive `device_revoked` over WebSocket
- future token refreshes for that same device UUID return `DEVICE_REVOKED`

When a client receives or discovers revocation, it should:

- delete the cached device token
- stop reconnect loops for `/ws/push`
- stop using that device UUID for uploads
- require a fresh trusted session before registering a new device UUID

## Error handling

CE errors use the standard envelope from
`docs/api/openapi.ce.yaml`:

```json
{
  "request_id": "uuid",
  "error": {
    "code": "SOME_CODE",
    "message": "human-readable message"
  }
}
```

Client behavior should branch on `error.code`, not on English message text.

Recommended handling:

- `INVALID_CREDENTIALS`: keep the current session unauthenticated and ask for new credentials.
- `TURNSTILE_REQUIRED`: fetch a fresh challenge token and retry the auth request.
- `TURNSTILE_FAILED`: do not silently loop; require a fresh Turnstile solve.
- `INVALID_REFRESH_TOKEN`: discard the session and require full login.
- `TWO_FACTOR_REQUIRED`: continue the login flow with `/api/v1/auth/login/2fa`.
- `TWO_FACTOR_INVALID_CODE`: allow retry with rate-limit aware UX.
- `TWO_FACTOR_LOCKED`: stop retrying until the lock window passes.
- `STEP_UP_REQUIRED`: start a new step-up flow before retrying the protected request.
- `STEP_UP_INVALID`: discard the cached verification token and request a new one.
- `STEP_UP_EXPIRED`: request a fresh verification token and retry once.
- `DEVICE_NOT_REGISTERED`: issue a device token first, then retry bundle upload.
- `DEVICE_REVOKED`: stop using that device UUID and device token.
- `QUOTA_EXCEEDED`: pause uploads and surface storage-limit guidance.
- `CONFLICT`: treat as an application-level conflict, not a transport retry signal.
- `BAD_REQUEST`: treat as a client bug or malformed request; do not blind-retry.

## Retry guidance

Safe default behavior:

- Retry transient network failures with backoff.
- Retry `5xx` responses only when the request is idempotent from the client's perspective.
- Do not blindly retry `4xx` responses except after a concrete recovery step such as refresh, re-auth, or step-up.
- Reconnect WebSocket with backoff, but stop reconnecting after `DEVICE_REVOKED`.

## Local and CI conformance modes

`pkg/clientconformance` supports lightweight protocol modes for local and CI
validation:

- `HSYNC_CONFORMANCE_TURNSTILE_MODE=fake`: default mode; accepts one fixed test token in-suite.
- `HSYNC_CONFORMANCE_TURNSTILE_MODE=skip`: disables Turnstile enforcement in the harness.
- `HSYNC_CONFORMANCE_2FA_MODE=real`: runs the full TOTP setup, login, and step-up flow.
- `HSYNC_CONFORMANCE_2FA_MODE=fake`: reserved lightweight mode for environments that only need contract coverage.
- `HSYNC_CONFORMANCE_2FA_MODE=skip`: mints a short-lived step-up token directly in the harness.
- `HSYNC_CONFORMANCE_PASSKEY_MODE=disabled`: default CE expectation; passkey login is unavailable.
- `HSYNC_CONFORMANCE_PASSKEY_MODE=fake`: reserved lightweight mode for environments that only need contract coverage.
- `HSYNC_CONFORMANCE_PASSKEY_MODE=skip`: skips the passkey-disabled assertion.

These modes are for protocol verification only. They are not production auth
configurations.
