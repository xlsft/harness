# Tunnels and PTY data plane

> Status: stub. Only registration and authorization are defined, in [relay.proto](../../proto/harness/relay/v1/relay.proto): `RegisterTunnel`, `RequestTunnel`, `ListTunnels`, `AuthorizeTunnel` and `CloseTunnel`. This page lists what still has to be specified before tunnels are implemented in M2.

The bytes of a tunnel never go through RPC. The host connects outbound to `data_plane_url` using `data_plane_token`, both returned by `RegisterTunnel`. Browsers reach the tunnel at `public_url`.

## To be specified

1. **Transport between host and server.** Two options:
   - one long-lived HTTP/2 connection per host that multiplexes all proxied requests as streams;
   - one WebSocket per proxied connection.

   For each, decide how it behaves behind reverse proxies, how many connections and how much memory it costs, and how backpressure works.
2. **Preview framing.**
   - How a browser HTTP request and its response map onto the data plane: headers, streaming bodies, trailers.
   - WebSocket upgrades, for dev servers with HMR.
   - Limits on body size and request duration.
3. **PTY framing.**
   - Input and output byte streams.
   - Resize messages (`cols` and `rows`).
   - Signals such as SIGINT.
   - Exit status.
   - Reattaching after a viewer disconnects, with a scrollback buffer.
   - Encoding: binary frames or JSON control messages.
4. **Auth.**
   - Lifetime and scope of `data_plane_token`: one tunnel, bound to `hst_` and the epoch.
   - The owner-only cookie on `<id>.preview.<host>` that the `access_url` from `AuthorizeTunnel` sets: `SameSite`, `HttpOnly`, lifetime.
   - Revocation when the device is revoked or the session ends.
   - CSRF protection and isolating preview origins from the main app origin.
5. **Host-side policy.**
   - Bind only to loopback ports.
   - Optional port allowlist.
   - Which working directories a PTY may open in (session cwd or allowed roots).
6. **Lifecycle.** Idle timeouts, `expires_at` renewal, close codes, and what viewers see while the host is offline.
