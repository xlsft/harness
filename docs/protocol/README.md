# harness protocols

Contracts between packages. The TypeScript server (`packages/core`) and the Go packages (`packages/client`, `packages/cli`) share no code, so these documents are the source of truth. If an implementation and a document disagree, fix one of them in the same change.

| Document | Parties | Status |
|---|---|---|
| [auth.md](auth.md) | client ↔ core | M0 |
| [gateway.md](gateway.md) | client ↔ core | M0 |
| [prompts.md](prompts.md) | client ↔ core | M0 |
| [sdk-stream.md](sdk-stream.md) | frontend (cli / ui / desktop) ↔ client, stdio NDJSON | M0 (owned by client) |
| sync.md | client ↔ core | M1 |
| relay.md | client ↔ core ↔ web / mobile | M2 |

## Conventions

- **Transport.** HTTPS with UTF-8 JSON bodies. Protocol fields use `snake_case`.
- **Versioning.** Every client request carries `Harness-Protocol: 1`. If the version is unsupported, the server responds `426` with `{error: {type: "protocol_version", min, max}}`.
- **Client identity.** `User-Agent: harness-client/<version> (<os>; <arch>)`.
- **Errors** are always JSON:
  ```json
  { "error": { "type": "invalid_request | authentication | permission | not_found | rate_limit | quota_exceeded | overloaded | upstream | internal", "message": "…", "retry_after_ms": 1200 } }
  ```
  Status codes: 400, 401, 403, 404, 429, 529 (overloaded), 502 (upstream), 500.
- **Timestamps** are RFC 3339 in UTC.
- **IDs** are opaque strings with a type prefix: `usr_`, `dev_`, `prv_`, `ses_`, `msg_`.
