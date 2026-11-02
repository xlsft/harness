# harness core

The harness server: authentication and devices, the LLM gateway (provider keys never leave the server),
the model catalog and the prompt registry. It never executes user code; clients run the agent loop and tools.

Stack: Nitro v3, TypeScript (strict), Connect RPC (`@connectrpc/connect` v2 + protobuf-es v2), Better Auth,
Drizzle ORM on Postgres (node-postgres, or embedded PGlite when no server is configured), Vitest.

Contracts: [`/proto`](../../proto) (source of truth) and [`docs/protocol`](../../docs/protocol).

## Quick start

```bash
pnpm install
cp .env.example .env        # set HARNESS_ADMIN_EMAIL / HARNESS_ADMIN_PASSWORD at least
pnpm dev                    # http://localhost:3000
```

Production build:

```bash
pnpm build
node .output/server/index.mjs     # same env vars; PORT selects the port
```

Other scripts:

| Script | What |
|---|---|
| `pnpm test` | Vitest: unit, translation and contract tests (in-process server + mock upstream) on in-memory PGlite. With `TEST_DATABASE_URL` set, `test/postgres.test.ts` also runs against that server (throwaway databases; the role needs `CREATEDB`); add `TEST_ALL_ON_POSTGRES=1` to run every suite there |
| `pnpm typecheck` | `tsc --noEmit` |
| `pnpm admin:create <email> <password> [name]` | Create or promote an admin without restarting (with PGlite, stop the server first: the embedded database is single-process) |
| `pnpm db:generate` | After editing `src/db/schema.ts`: drizzle-kit SQL migration + embed it into `src/db/migrations.generated.ts` |
| `pnpm gen` | Regenerate `src/gen` from `/proto` with the local `protoc-gen-es` (needs `buf`); equivalent to the TS part of the root `buf.gen.yaml` |

Migrations are applied automatically on startup.

## Database

Postgres, in one of two forms; the code (`src/db/index.ts`, `getDb()`) is the same for both:

- **`DATABASE_URL` set:** a real Postgres through a node-postgres pool. A normal URL
  (`postgres://user:pass@host:5432/harness`) or a unix socket (`postgres:///harness?host=/run/postgresql`), which
  is how the single-container image reaches the Postgres it runs next to core.
- **`DATABASE_URL` unset:** embedded [PGlite](https://pglite.dev) (Postgres compiled to WASM, in-process),
  persisted to `$HARNESS_DATA_DIR/pglite`. Nothing to install, for local development and quick trials. It is
  single-process: a second process on the same directory fails fast (`pglite.lock`). `DATABASE_URL=memory://`
  keeps it in memory (tests).

Migrations are drizzle-kit SQL (`drizzle/`, pg dialect) embedded into `src/db/migrations.generated.ts`, so the
bundle needs no migration files. At startup `ensureDb()` applies the pending ones in a single transaction under
`pg_advisory_xact_lock`, recorded in `__harness_migrations`; concurrent startups wait for each other, and a
failed migration rolls back completely.

## Environment

See [`.env.example`](.env.example). The important ones:

| Variable | Default | Purpose |
|---|---|---|
| `HARNESS_SECRET` | generated into the data dir | Master key: Better Auth secret and AES-256-GCM key for provider secrets (HKDF-derived) |
| `HARNESS_PUBLIC_URL` | `http://localhost:$PORT` | Base for `verification_uri`, Better Auth base URL / trusted origin |
| `DATABASE_URL` | unset: PGlite in `$HARNESS_DATA_DIR/pglite` | Postgres URL (TCP or unix socket); see Database |
| `HARNESS_DATA_DIR` | `./.data` | Generated secret, embedded PGlite database |
| `HARNESS_ADMIN_EMAIL`, `HARNESS_ADMIN_PASSWORD` | — | Admin bootstrap on start |
| `HARNESS_GRPC_PORT` | off | Extra h2c listener with plain gRPC (see Transport) |
| `HARNESS_PROMPTS_DIR` | `./prompts` | Prompt files (see [`prompts/README.md`](prompts/README.md)) |
| `HARNESS_DEVICE_RATE_LIMIT_PER_MIN` | `30` | Per-IP token bucket (burst and refill per minute) for `StartDeviceAuth`, `PollDeviceAuth`, `RefreshAccessToken`, each limited separately; `0` disables |
| `HARNESS_TRUST_PROXY` | `false` | Take the client IP from the rightmost `X-Forwarded-For` entry (set when behind a reverse proxy) |
| `HARNESS_SYNC_TOMBSTONE_DAYS` | `30` | Tombstone retention (minimum 30) |
| `HARNESS_SYNC_HEARTBEAT_MS` | `20000` | `WatchChanges` heartbeat interval (max 20 s) |
| `HARNESS_SESSION_BUDGET_USD` | — | Default per-session budget when the quota config has no `session_usd` |
| `HARNESS_MCP_IDLE_MS` | `300000` | Idle timeout of pooled upstream MCP sessions |
| `HARNESS_MCP_REQUEST_TIMEOUT_MS` | `60000` | Timeout of proxied MCP requests other than `tools/call` (connect, list, get, read) |
| `HARNESS_MCP_TOOLS_TTL_MS` | `300000` | Age after which `GetToolCatalog` refetches a remote server's tool list |
| `HARNESS_MCP_CATALOG_FETCH_TIMEOUT_MS` | `10000` | Per-server timeout of that refetch (on failure the last list is served) |
| `HARNESS_MCP_CATALOG_RETENTION_DAYS` | `7` | Catalog snapshots stay fetchable this long after their last use (minimum 7) |
| `HARNESS_MCP_LAUNCH_RATE_PER_MIN` | `30` | Per-device token bucket for `IssueStdioLaunch` |

## Endpoints

### Connect services (client protocol)

| Service | Methods |
|---|---|
| `harness.auth.v1.DeviceAuthService` | `StartDeviceAuth`, `PollDeviceAuth`, `RefreshAccessToken`, `GetMe`, `Logout`, `ListDevices`, `RevokeDevice` |
| `harness.llm.v1.GatewayService` | `ListModels`, `StreamMessage` (server streaming), `CountTokens` |
| `harness.prompts.v1.PromptService` | `GetBundle` |
| `harness.sync.v1.SyncService` | `ListChanges`, `WatchChanges` (server streaming), `GetDocument`, `PutDocument`, `BatchPutDocuments`, `DeleteDocument`, `UpsertSession`, `AppendSessionEvents`, `GetSession`, `ListSessions`, `GetSessionTranscript` |
| `harness.search.v1.SearchService` | `Search`, `ListProviders` |
| `harness.decision.v1.DecisionService` | `Decide`, `ListProviders` (see Decisions) |
| `harness.mcp.v1.McpService` | Registry (`List/Get/Create/Update/DeleteServer`), OAuth (`StartOAuth`, `GetOAuthFlow`, `GetAuthStatus`, `RevokeAuth`), devices (`ReportDeviceStatus`, `ListDeviceStatus`, `IssueStdioLaunch`), proxy (`ListTools`, `ListPrompts`, `ListResources`, `CallTool` (server streaming), `GetPrompt`, `ReadResource`), `GetToolCatalog` (see MCP) |

Mounted at `/<package>.<Service>/<Method>`. Authenticated calls use `Authorization: Bearer hat_…`.
Errors carry a `harness.common.v1.ErrorInfo` detail.

**Transport.** The main HTTP port (Nitro) serves the **Connect** and **gRPC-Web** protocols (HTTP/1.1, or HTTP/2
behind a proxy). Plain **gRPC** needs HTTP/2 trailers, which Nitro's fetch-based pipeline cannot produce, so
it is served by an optional h2c listener (`HARNESS_GRPC_PORT`, `node:http2` + `connectNodeAdapter`) that
accepts all three protocols on the same router. Go clients can use the Connect protocol against the main port
(connect-go's default) or gRPC against the h2c port.

Try it with curl:

```bash
curl -s localhost:3000/harness.auth.v1.DeviceAuthService/StartDeviceAuth \
  -H 'content-type: application/json' -d '{"deviceName":"laptop"}'
```

### Plain HTTP

| Path | What |
|---|---|
| `/device` | Device approval page (sign in, enter or confirm the user code, approve/deny) |
| `GET /api/mcp/oauth/callback` | Redirect target of MCP OAuth flows (`<HARNESS_PUBLIC_URL>/api/mcp/oauth/callback`); result page or 303 to the flow's `return_url` |
| `/api/auth/**` | Better Auth (email + password sessions, admin plugin: `/api/auth/admin/create-user`, …) |
| `/api/admin/providers[/:id]` | Provider CRUD (`GET`, `POST`, `PATCH`, `DELETE`) |
| `POST /api/admin/providers/:id/discover` | Candidate models from the upstream's `/v1/models` (through the SSRF guard): `upstream_model`, `display_name`, `context_window` / `max_output_tokens` when reported, `catalog_ids` already using it |
| `/api/admin/models[/:id]` | Model catalog CRUD; `GET /:id` also lists features turned off by negotiation, `PATCH {reset_negotiation:true}` clears them |
| `/api/admin/slots` | `GET` / `PUT {main, small, plan, classifier}` (`classifier`: auto-mode classifier model, falls back to `small`) |
| `/api/admin/usage` | Recent usage records (`?user_id=&limit=`) |
| `/api/admin/quotas` | Spend limits: `GET` (`?user_id=` adds current spend + effective limits), `PUT` (replace), `PATCH` (merge, `null` removes) |
| `/api/admin/decision` | Auto-mode decision chain: `GET` (`{config, configured}`, secrets redacted), `PUT` (replace) |
| `GET /api/admin/decision/log` | Decision audit log, newest first (`?limit=&session_id=&user_id=`) |
| `PATCH /api/admin/mcp/servers/:id` | `{allow_private_network: bool}`: let the MCP proxy reach private / loopback addresses for this server (reset when its URL moves to another origin) |
| `/api/health` | Liveness |

Admin routes accept an admin's Better Auth session cookie or an admin's device access token.

## Admin setup example

```bash
TOKEN=hat_...   # an admin's access token (device flow) — or use the session cookie
H=(-H "authorization: Bearer $TOKEN" -H 'content-type: application/json')

curl -s localhost:3000/api/admin/providers "${H[@]}" -d '{
  "id": "anthropic", "type": "anthropic", "base_url": "https://api.anthropic.com", "api_key": "sk-ant-..."}'
curl -s localhost:3000/api/admin/providers "${H[@]}" -d '{
  "id": "ollama", "type": "openai-chat", "base_url": "http://127.0.0.1:11434/v1", "allow_private_network": true}'

curl -s localhost:3000/api/admin/models "${H[@]}" -d '{
  "id": "claude-sonnet", "display_name": "Claude Sonnet", "provider_id": "anthropic",
  "upstream_model": "claude-sonnet-4-5", "context_window": 200000, "max_output_tokens": 64000,
  "capabilities": {"thinking": true, "effort": ["low","medium","high"], "images": true, "pdf": true, "prompt_cache": true},
  "pricing": {"input": 3, "output": 15, "cache_read": 0.3, "cache_write": 3.75},
  "fallbacks": []}'

curl -s -X PUT localhost:3000/api/admin/slots "${H[@]}" -d '{"main": "claude-sonnet", "small": "claude-sonnet"}'
```

Provider fields: `type` (`anthropic` | `openai-chat` | `openai-responses`), `base_url`, `api_key`, `headers` (extra request headers),
`allow_private_network`, `enabled`. Base URL conventions: Anthropic — host root (`/v1/messages` is appended;
a trailing `/v1` is tolerated); OpenAI-compatible — up to and including `/v1` (`/chat/completions` or `/responses` is appended; works for
OpenAI and Responses-compatible endpoints such as OpenRouter).
Keys and headers are encrypted at rest and never returned (only `api_key_hint` and `header_names`).

Model fields: `id` (alias clients send), `provider_id`, `upstream_model`, `context_window`, `max_output_tokens`,
`capabilities`, `pricing` (USD / 1M tokens), `fallbacks` (aliases tried on overload), `extra_body`
(merged into the upstream request, e.g. vLLM/Qwen switches), `roles` (visibility), `enabled`, `sort_order`.

## Sync

- **Versions.** One global counter (`sync_counter`, an int8 row upserted in `nextSyncVersion` inside the writing
  transaction, so readers only ever see committed versions and its row lock orders writers); document writes are
  also serialized in-process, so versions commit in order and live events go out in version order.
- **Documents** are unique per (owner, scope, project_key, kind, key); org documents have no owner. CAS per
  `base_version` (unset / 0 = create only / N); mismatches come back as `Conflict`, not as errors. Writing identical
  content is a no-op. Every write is kept in `sync_document_revision` (no read API yet).
- **Authorization.** Users read and write their own user/project documents; org documents are admin-written.
  The feed contains org documents targeted at the caller's role (or at everyone); other org documents appear as
  tombstones, so caches drop documents re-targeted away from a role. Admins can `GetDocument` any org document.
- **Validation.** JSON kinds (settings, permission rules, hooks, SSH hosts, policies) must be JSON objects and are
  stored byte-exact; `SETTINGS.permissions` only takes `defaultMode`, `additionalDirectories`,
  `disableBypassPermissionsMode`; project-scope SETTINGS are rejected; `files` only on SKILL and MEMORY;
  RULE keys are relative `.md` paths; POLICY is org-only; MCP_SERVER cannot be written here.
- **Tombstones** are purged after `HARNESS_SYNC_TOMBSTONE_DAYS` (hourly, opportunistically); cursors older than the
  highest purged version, or newer than the head, get `resync_required`. `next_page_token` continuations never do.
- **WatchChanges**: subscribe, catch up through `ListChanges` pages, `CaughtUp`, then live changes from the
  in-process bus; a `Heartbeat` with the head after 20 s of silence.
- **Sessions**: ids are UUID v4; `update_paths` use `info.<field>` and `prompt_bundle_version`. Transcript lines are
  stored byte-exact, deduplicated per (session, agent_id, uuid); `seq` is one counter per session shared by the main
  thread and subagents (each thread pages by `agent_id`). `ListSessions` searches titles, cwd, repo and transcript
  text (case-insensitive substring).

## MCP

`McpService` (proto/harness/mcp/v1/mcp.proto, PLAN §1.5) keeps every MCP server config and authorization on the
server. Code in `src/mcp/`.

- **Registry.** Scopes USER and PROJECT (+ `project_key`) belong to their owner; ORG servers are admin-written and
  visible to everyone or to `target_roles`. Names are unique per (owner, scope, project_key) — a clash is
  `ABORTED`/`CONFLICT`. `UpdateServer`/`DeleteServer` are CAS on `base_version`; mismatches come back as `Conflict`.
  Versions come from the global sync counter: each write also writes a `DOCUMENT_KIND_MCP_SERVER` notification
  row (key = server id, empty content, same version) in the same transaction, so `WatchChanges` subscribers
  refetch. Authorization changes of a user are announced the same way (new notification version, server version
  unchanged).
- **Secrets** are write-only (`SecretChanges`), AES-256-GCM encrypted per server and name (HKDF purpose
  `mcp-secrets`, AAD `<server id>:secret:<name>`). Reads return `SecretInfo` for stored names and for names
  referenced as `${secret:NAME}` but not set (`has_value=false`). `config_hash` = SHA-256 over the canonical JSON
  of transport, stdio (command, args, env, cwd), remote (url, headers), overrides and secret names.
- **OAuth** (remote servers): `StartOAuth` runs RFC 9728 / RFC 8414 discovery (or `remote.oauth.authorization_server`),
  dynamic client registration when the provider has a registration endpoint (else `remote.oauth.client_id`, with an
  optional `OAUTH_CLIENT_SECRET` secret), PKCE S256 and an RFC 8707 resource indicator. The callback stores the
  tokens encrypted per (user, server). Tokens are refreshed when they expire within 60 s and once after an upstream
  401; a failed refresh or a surviving 401 makes the server `NEEDS_AUTH`. Changing `remote.url` or adding
  `remote.oauth.scopes` requires a new authorization. `RevokeAuth` drops the grant (best-effort RFC 7009).
- **Proxy.** One pooled upstream MCP session per (server, user) via the official SDK client (Streamable HTTP, SSE;
  WebSocket through an undici transport), closed after `HARNESS_MCP_IDLE_MS`, on config / grant changes, and
  re-created on connection loss. Headers, URL secrets and bearer tokens are injected server-side; every connection
  (including OAuth endpoints) goes through the SSRF guard unless an admin set `allow_private_network`. After three
  consecutive failures new connections back off (1 s doubling, max 60 s). `CallTool` streams progress, then one
  result; cancelling the RPC sends `notifications/cancelled` upstream. Tools of servers with
  `requires_user_interaction` carry `"requiresUserInteraction": true` in `annotations_json`; upstream annotations
  are passed through unchanged otherwise.
- **stdio.** Devices report availability, approval (bound to `config_hash`), last error and discovered tools.
  `IssueStdioLaunch` requires a device access token, the current hash and an APPROVED report for it; it applies
  per-device / per-OS overrides, substitutes secrets, expires after 60 s, is audit-logged (`mcp_audit`, no values)
  and rate-limited per device.
- **Catalog.** `GetToolCatalog` snapshots tools of all enabled visible servers (remote: proxy-fetched, cached per
  user with `HARNESS_MCP_TOOLS_TTL_MS`; stdio: latest device report). The version is content-addressed
  (`cat_` + SHA-256 of user, project key and tool lists), each version is stored once and served unchanged, and
  snapshots are kept `HARNESS_MCP_CATALOG_RETENTION_DAYS` (>= 7) after their last use.

## Quotas

Checked before the upstream is called; a used-up limit fails `StreamMessage` with `RESOURCE_EXHAUSTED` +
`ERROR_TYPE_QUOTA_EXCEEDED` (do not retry). Spend is the recorded usage cost (model pricing).

```json
{
  "roles": { "user": { "daily_usd": 5, "monthly_usd": 50 } },
  "users": { "usr_...": { "daily_usd": 20 } },
  "session_usd": 2
}
```

- Periods are UTC calendar days / months. Per period, a user's own value replaces the role value.
- `session_usd` (fallback `HARNESS_SESSION_BUDGET_USD`) caps the spend per `RequestMetadata.session_id`.
- `0` blocks; absent = unlimited. A request that starts under a limit may overshoot it; the next one is rejected.

## Decisions

`DecisionService` backs the client's `auto` permission mode: the client asks whether a tool call may run
without a prompt. A chain of providers answers, configured with `PUT /api/admin/decision`:

```json
{
  "providers": [
    { "type": "rules" },
    { "type": "http", "id": "opa", "settings": { "url": "https://opa.corp/v1/decide", "bearer_token": "…", "timeout_ms": 5000 } },
    { "type": "llm", "settings": { "timeout_ms": 8000 } }
  ],
  "on_error": "ask",
  "cache_ttl_seconds": 600,
  "classifier_model": null,
  "rate_limit_per_min": 120,
  "rules": { "extra_deny": [{ "pattern": "\\bnpm publish\\b", "reason": "CI publishes", "categories": ["production_change"] }],
             "extra_allow": ["^make docs$"], "protected_branches": ["main", "master", "release*"], "deny_all_force_push": false }
}
```

- Unconfigured default: `[rules, llm]`, `on_error: ask`.
- `rules` (src/decision/rules.ts) is deterministic and dependency-free: hard denies (pipe-to-shell, destructive
  `rm`, force pushes to protected branches, infra teardown, DB drops, production deploys, secret exfiltration,
  sudo / persistence, raw disk writes) and a fast-allow list (read-only commands, in-workspace test / lint /
  build runs, edits inside the working directories). Its DENY is final. The Go client ports the same table:
  `test/fixtures/decision-rules.json`.
- `llm` calls a catalog model through the gateway (`request_class: auxiliary`, charged to the user, quotas
  apply) with the prompts in `prompts/decision/` and a forced `classify` tool call; malformed output is ASK;
  a timeout is an error. ALLOW / DENY are cached in memory per user + input + cwd + recent intent.
- `http` POSTs the request (snake_case, plus `user_id`, `user_role`, `device_id`) to a policy webhook through
  the SSRF guard (`allow_private_network` for internal hosts) and expects `{decision, reason, categories?, confidence?}`.
- The first ALLOW / DENY wins, ASK falls through, errors answer ASK (`on_error: ask`) or fall through (`next`);
  nobody deciding gives ASK from provider `none`. `Decide` is rate-limited per user. Every decision is logged
  in `decision_log`.

## Layout

```
src/
  env.ts                 config from env
  bootstrap.ts           migrations + admin bootstrap
  db/                    Drizzle schema (pg), embedded migrations + runner, node-postgres / PGlite driver
  auth/better-auth.ts    Better Auth instance (email+password, admin plugin), ensureAdmin()
  services/              transport-agnostic domain logic
    devices.ts           device flow, device/access tokens
    providers.ts         provider registry + encryption
    models.ts            catalog, slots, negotiation memory, effort downgrade
    gateway.ts           prepareMessages / runMessages (retries, negotiation, fallback, usage), countTokens
    prompts.ts           prompt bundle builder (files + overrides)
    usage.ts             usage listing
  decision/              DecisionService: rules engine, llm / http providers, chain, config, cache
  search/                SearchService providers (SearXNG)
  mcp/                   McpService: registry + secrets, OAuth, proxy (SDK client, pool), stdio devices, catalog
  gateway/               canonical types, upstream adapters (anthropic, openai-chat), errors, cost
  rpc/                   Connect layer: proto <-> canonical conversion, service impls, error mapping,
                         fetch handler (Nitro) and h2c listener
  http/                  plain-HTTP routes (admin API, /device, Better Auth) and the shared route table
  lib/                   crypto, SSRF guard, SSE parser, ids, errors
  gen/                   generated protobuf code (do not edit)
prompts/                 prompt files (see prompts/README.md)
drizzle/                 SQL migrations (drizzle-kit, pg dialect)
test/                    vitest; helpers/ (in-process server, mock upstream, MCP + OAuth fixture), fixtures/ (upstream SSE)
```

## Gateway behavior notes

- **OpenAI Responses** (`openai-responses`): system -> `instructions`, tool_use/tool_result -> `function_call` /
  `function_call_output` items, effort -> `reasoning.effort`, max_tokens -> `max_output_tokens`. Reasoning is stateless:
  `store: false` + `include: ["reasoning.encrypted_content"]`; a reasoning item becomes a thinking block (summary text)
  whose `signature` encodes the item (`oair1.` prefix) so it is replayed verbatim before the related function calls.
  Foreign signatures are dropped (and the Anthropic adapter drops `oair1.` ones). Stop sequences are not supported
  upstream and are dropped. `reasoning` and `include` are negotiable features.

- Canonical model inside the server is Anthropic-shaped JSON; `src/rpc/convert.ts` maps it to/from protobuf.
- Retries (exponential backoff with jitter, `retry-after(-ms)`, `x-should-retry`; give up if retry-after > 60 s)
  happen only before the first content block; each emits a `Retry` event. 401/402/403 and TLS errors are not retried.
- Capability negotiation: a 400/422 that names a feature we sent (`thinking`, `effort`/`output_config`/`reasoning_effort`,
  `cache_control`, system blocks, `stream_options`, `max_tokens` vs `max_completion_tokens`, `parallel_tool_calls`) is retried
  without it and remembered per model in the DB.
- Fallback: on `overloaded`, after 2 retries on the current model the next model from `fallbacks` is tried
  (models with a smaller context window are skipped).
- `Ping` every `HARNESS_PING_INTERVAL_MS` (10 s) while nothing else was sent.
- `MessageStop.served_model` is the catalog alias that answered (differs from the requested model after a fallback).
- OpenAI tool calls whose deltas interleave across `index` are buffered per index: one call streams live, the others are
  emitted (in index order) once the live call's arguments are complete JSON or at the end of the stream.
- SSRF guard: the address actually connected to is checked (custom DNS lookup in the undici connector),
  IP-literal hosts are checked up front, redirects are only followed within the same origin.
