# Auth: device flow and tokens

Web sign-in is a regular Better Auth session (cookie). Clients — CLI, desktop and SSH runners — get a **device token** through a device flow modeled on RFC 8628.

## Device flow

### 1. `POST /api/auth/device/code`
```json
// request
{ "client_name": "harness-client", "device_name": "macbook-pro", "os": "darwin", "arch": "arm64" }
// 200 response
{ "device_code": "…", "user_code": "WDJB-MJHT", "verification_uri": "https://host/device",
  "verification_uri_complete": "https://host/device?code=WDJB-MJHT", "expires_in": 600, "interval": 5 }
```

### 2. The user opens `verification_uri` in a browser
They sign in, see the device name and approve it.

### 3. `POST /api/auth/device/token`, polled by the client every `interval` seconds
```json
// request
{ "device_code": "…" }
// 400 while not yet approved
{ "error": { "type": "authorization_pending" } }   // also: "slow_down" (add 5 s to the interval), "expired_token", "access_denied"
// 200 response
{ "device_id": "dev_…", "device_token": "hdt_…", "access_token": "hat_…", "access_expires_in": 3600,
  "user": { "id": "usr_…", "email": "…", "name": "…", "role": "admin | user" } }
```

## Tokens

| Token | Purpose | Lifetime | Client-side storage |
|---|---|---|---|
| `device_token` (`hdt_…`) | Only exchanged for access tokens | Until revoked | OS keychain; fallback `~/.harness/credentials.json` (mode 0600) |
| `access_token` (`hat_…`) | All API requests: `Authorization: Bearer hat_…` | 1 hour | Memory only |

### `POST /api/auth/device/refresh`
```json
// request
{ "device_token": "hdt_…" }
// 200 response
{ "access_token": "hat_…", "access_expires_in": 3600 }
// 401 → the device was revoked: the client deletes its credentials and asks the user to run `harness login`
```

Client rules:
- Refresh proactively, 5 minutes before expiry.
- On any `401`, refresh once and retry the request. An open SSE stream is not interrupted.

## Other endpoints

- `GET /api/auth/me` → `{ user, device: { id, name, created_at, last_seen_at } }`.
- `POST /api/auth/device/logout` revokes the current device.
- Device management (list, revoke) lives in the web UI; its API will be specified later.
