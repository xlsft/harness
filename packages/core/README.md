# harness core

The harness server: authentication and devices, the LLM gateway (provider keys never leave the server),
the model catalog and the prompt registry. It never executes user code; clients run the agent loop and tools.

Stack: Nitro v3, TypeScript (strict), Connect RPC (`@connectrpc/connect` v2 + protobuf-es v2), Better Auth,
Drizzle ORM on SQLite (libSQL driver), Vitest.

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
| `pnpm test` | Vitest: unit, translation and contract tests (in-process server + mock upstream) |
| `pnpm typecheck` | `tsc --noEmit` |
| `pnpm admin:create <email> <password> [name]` | Create or promote an admin without restarting |
| `pnpm db:generate` | After editing `src/db/schema.ts`: drizzle-kit SQL migration + embed it into `src/db/migrations.generated.ts` |
| `pnpm gen` | Regenerate `src/gen` from `/proto` with the local `protoc-gen-es` (needs `buf`); equivalent to the TS part of the root `buf.gen.yaml` |

Migrations are applied automatically on startup.

## Environment

See [`.env.example`](.env.example). The important ones:

| Variable | Default | Purpose |
|---|---|---|
| `HARNESS_SECRET` | generated into the data dir | Master key: Better Auth secret and AES-256-GCM key for provider secrets (HKDF-derived) |
| `HARNESS_PUBLIC_URL` | `http://localhost:$PORT` | Base for `verification_uri`, Better Auth base URL / trusted origin |
| `HARNESS_DATA_DIR` | `./.data` | SQLite database and generated secret |
| `HARNESS_ADMIN_EMAIL`, `HARNESS_ADMIN_PASSWORD` | — | Admin bootstrap on start |
| `HARNESS_GRPC_PORT` | off | Extra h2c listener with plain gRPC (see Transport) |
| `HARNESS_PROMPTS_DIR` | `./prompts` | Prompt files (see [`prompts/README.md`](prompts/README.md)) |
| `HARNESS_DEVICE_RATE_LIMIT_PER_MIN` | `30` | Per-IP token bucket (burst and refill per minute) for `StartDeviceAuth`, `PollDeviceAuth`, `RefreshAccessToken`, each limited separately; `0` disables |
| `HARNESS_TRUST_PROXY` | `false` | Take the client IP from the rightmost `X-Forwarded-For` entry (set when behind a reverse proxy) |
| `HARNESS_SESSION_BUDGET_USD` | — | Default per-session budget when the quota config has no `session_usd` |

## Endpoints

### Connect services (client protocol)

| Service | Methods |
|---|---|
| `harness.auth.v1.DeviceAuthService` | `StartDeviceAuth`, `PollDeviceAuth`, `RefreshAccessToken`, `GetMe`, `Logout`, `ListDevices`, `RevokeDevice` |
| `harness.llm.v1.GatewayService` | `ListModels`, `StreamMessage` (server streaming), `CountTokens` |
| `harness.prompts.v1.PromptService` | `GetBundle` |

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
| `/api/auth/**` | Better Auth (email + password sessions, admin plugin: `/api/auth/admin/create-user`, …) |
| `/api/admin/providers[/:id]` | Provider CRUD (`GET`, `POST`, `PATCH`, `DELETE`) |
| `POST /api/admin/providers/:id/discover` | Candidate models from the upstream's `/v1/models` (through the SSRF guard): `upstream_model`, `display_name`, `context_window` / `max_output_tokens` when reported, `catalog_ids` already using it |
| `/api/admin/models[/:id]` | Model catalog CRUD; `GET /:id` also lists features turned off by negotiation, `PATCH {reset_negotiation:true}` clears them |
| `/api/admin/slots` | `GET` / `PUT {main, small, plan}` |
| `/api/admin/usage` | Recent usage records (`?user_id=&limit=`) |
| `/api/admin/quotas` | Spend limits: `GET` (`?user_id=` adds current spend + effective limits), `PUT` (replace), `PATCH` (merge, `null` removes) |
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

Provider fields: `type` (`anthropic` | `openai-chat`), `base_url`, `api_key`, `headers` (extra request headers),
`allow_private_network`, `enabled`. Base URL conventions: Anthropic — host root (`/v1/messages` is appended;
a trailing `/v1` is tolerated); OpenAI-compatible — up to and including `/v1` (`/chat/completions` is appended).
Keys and headers are encrypted at rest and never returned (only `api_key_hint` and `header_names`).

Model fields: `id` (alias clients send), `provider_id`, `upstream_model`, `context_window`, `max_output_tokens`,
`capabilities`, `pricing` (USD / 1M tokens), `fallbacks` (aliases tried on overload), `extra_body`
(merged into the upstream request, e.g. vLLM/Qwen switches), `roles` (visibility), `enabled`, `sort_order`.

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

## Layout

```
src/
  env.ts                 config from env
  bootstrap.ts           migrations + admin bootstrap
  db/                    Drizzle schema, embedded migrations, libSQL client
  auth/better-auth.ts    Better Auth instance (email+password, admin plugin), ensureAdmin()
  services/              transport-agnostic domain logic
    devices.ts           device flow, device/access tokens
    providers.ts         provider registry + encryption
    models.ts            catalog, slots, negotiation memory, effort downgrade
    gateway.ts           prepareMessages / runMessages (retries, negotiation, fallback, usage), countTokens
    prompts.ts           prompt bundle builder (files + overrides)
    usage.ts             usage listing
  gateway/               canonical types, upstream adapters (anthropic, openai-chat), errors, cost
  rpc/                   Connect layer: proto <-> canonical conversion, service impls, error mapping,
                         fetch handler (Nitro) and h2c listener
  http/                  plain-HTTP routes (admin API, /device, Better Auth) and the shared route table
  lib/                   crypto, SSRF guard, SSE parser, ids, errors
  gen/                   generated protobuf code (do not edit)
prompts/                 prompt files (see prompts/README.md)
drizzle/                 SQL migrations (drizzle-kit)
test/                    vitest; helpers/ (in-process server, mock upstream), fixtures/ (upstream SSE)
```

## Gateway behavior notes

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
