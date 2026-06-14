# Client Integration

This document describes the minimal CE client integration flow for a desktop
app or browser extension. It covers protocol sequencing only. The server treats
bundle and snapshot payloads as opaque encrypted blobs and does not parse them.

## 1. Authenticate the user

Register or log in with email/password:

- `POST /api/v1/auth/register`
- `POST /api/v1/auth/login`

Both routes require `turnstile_token` in CE when Turnstile is enabled. A
successful login returns:

- `access_token`: bearer token for normal API calls
- `refresh_token`: long-lived token used to mint new access tokens
- `expires_in`: access token lifetime in seconds

If the account has TOTP enabled, `POST /api/v1/auth/login` returns:

- `requires_2fa=true`
- `challenge`
- `expires_in`

Complete that flow with `POST /api/v1/auth/login/2fa`.

## 2. Refresh access tokens

When the access token expires, call:

- `POST /api/v1/auth/refresh`

with `refresh_token` and replace the in-memory access token with the returned
`access_token`.

## 3. Register a client device and get a WS token

Choose a stable client-generated UUID per installed app profile, then request a
device token:

- `POST /api/v1/devices/{uuid}/token`

Recommended request fields:

- `device_name`
- `platform`
- `app_version`

The response contains:

- `device_token`: bearer token for `GET /ws/push`
- `expires_in`: CE currently uses 24 hours
- `device`: persisted device metadata

The device token is separate from the user access token. Use the access token
for REST API calls and the device token only for WebSocket auth.

## 4. Open the push channel

Connect:

- `GET /ws/push`

with:

- `Authorization: Bearer <device_token>`

Current CE push message types:

- `bundle_uploaded`
- `snapshot_uploaded`
- `device_revoked`

Clients should treat push as an invalidation signal and re-fetch metadata or
content from HTTP endpoints rather than assuming the push payload is complete.

## 5. Sync bundles

Upload encrypted bundle blobs with:

- `POST /api/v1/bundles`

Multipart fields:

- `bundle`: file payload
- `bundle_id`
- `device_uuid`
- `lamport_lo`
- `lamport_hi`
- `event_count`
- `cipher_id`
- `key_generation`

Read back metadata and content with:

- `GET /api/v1/bundles`
- `GET /api/v1/bundles/{id}`

The CE server stores the uploaded bytes as-is and does not inspect bundle
contents.

## 6. Sync snapshots

Upload encrypted snapshot blobs with:

- `POST /api/v1/snapshots`

Multipart fields:

- `snapshot`: file payload
- `snapshot_id`
- `base_hlc`
- `cipher_id`
- `key_generation`

Read back metadata and content with:

- `GET /api/v1/snapshots/latest`
- `GET /api/v1/snapshots/{id}`

## 7. Step-up for sensitive actions

Sensitive user actions require an `X-HSync-Verification` header. For TOTP
accounts, obtain it with:

- `POST /api/v1/auth/verify`

using:

- `method=totp`
- `code`

The response returns `verification_token`. Send it as:

- `X-HSync-Verification: <verification_token>`

Example sensitive route:

- `POST /api/v1/devices/{uuid}/revoke`

Without step-up, CE returns `STEP_UP_REQUIRED`.

## 8. Device revocation handling

When a device is revoked:

- REST revocation is done via `POST /api/v1/devices/{uuid}/revoke`
- existing clients receive `device_revoked` over WebSocket
- future `POST /api/v1/devices/{uuid}/token` calls for that revoked device
  return `DEVICE_REVOKED`

Clients should clear the revoked device token, stop reconnect loops, and
require the user to re-register the device under a new trusted session.

## Error handling contract

CE errors use the standard envelope from `docs/api/openapi.ce.yaml`:

```json
{
  "request_id": "uuid",
  "error": {
    "code": "SOME_CODE",
    "message": "human-readable message"
  }
}
```

Client logic should branch on `error.code`, not on English text. The canonical
code catalog lives in `pkg/apierrors/apierrors.go`, and the HTTP status mapping
in that catalog should match the OpenAPI document.

Common codes in the sync flow include:

- `INVALID_CREDENTIALS`
- `INVALID_REFRESH_TOKEN`
- `TURNSTILE_REQUIRED`
- `PASSKEY_DISABLED`
- `STEP_UP_REQUIRED`
- `DEVICE_REVOKED`
- `BAD_REQUEST`
- `CONFLICT`
- `QUOTA_EXCEEDED`

## Local and CI test modes

The CE client conformance suite in `pkg/clientconformance` supports lightweight
test-only auth modes:

- Turnstile is faked in-suite with a fixed accepted token.
- `HSYNC_CONFORMANCE_2FA_MODE=real` runs the full TOTP flow.
- `HSYNC_CONFORMANCE_2FA_MODE=skip` mints a short-lived step-up token in the
  harness for revoke-flow testing.
- `HSYNC_CONFORMANCE_PASSKEY_MODE=disabled` asserts the default CE behavior that
  passkey login is unavailable.
- `HSYNC_CONFORMANCE_PASSKEY_MODE=skip` skips the passkey-disabled assertion.

These modes are intended for protocol verification only. They do not replace
production auth configuration.
