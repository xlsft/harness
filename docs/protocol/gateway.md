# LLM gateway: server behavior

The wire contract is [`harness.llm.v1.GatewayService`](../../proto/harness/llm/v1/gateway.proto). Client-side rules — tool pairing, unchanged thinking blocks, cache breakpoints — are documented in the proto comments. This document covers what the server does behind the contract.

## Translation

The canonical model follows the Anthropic Messages API. Per upstream type:

- **Anthropic.** Near passthrough:
  - `cache = true` → `cache_control: {type: "ephemeral"}`;
  - `effort` → `output_config.effort`;
  - thinking blocks and signatures are preserved.
- **OpenAI Chat Completions:**
  - system blocks → one system message;
  - `tool_result` blocks → `tool` role messages, with images from tool results moved into a follow-up user message;
  - thinking blocks → `reasoning_content` where the upstream supports it (DeepSeek, Qwen style), otherwise dropped;
  - `tools` → `functions`; streamed `tool_calls` → `content_block_start` / `input_json` deltas / `content_block_stop`;
  - `reasoning_content` deltas → thinking blocks;
  - `effort` → `reasoning_effort`;
  - `finish_reason`: `stop → END_TURN`, `tool_calls → TOOL_USE`, `length → MAX_TOKENS`, `content_filter → REFUSAL`;
  - `prompt_tokens_details.cached_tokens` → `cache_read_input_tokens`.
- **OpenAI Responses** (provider type `openai-responses`):
  - system blocks → `instructions`;
  - tool_use / tool_result → `function_call` / `function_call_output` items;
  - `effort` → `reasoning.effort`;
  - requests use `store: false` and `include: ["reasoning.encrypted_content"]`, so no state is kept upstream;
  - reasoning items become thinking blocks whose `signature` (prefix `oair1.`) encodes the encrypted item; it is replayed unchanged before the related function calls;
  - signatures from other providers are dropped.

## Reliability

- **Retries.** Upstream calls are retried with exponential backoff and jitter, honoring `retry-after` (stop if it exceeds 60 s) and `x-should-retry`. Retries happen only **before the first content block**, and each one emits a `Retry` event. Authentication errors, spend-limit errors and TLS failures are not retried.
- **Capability negotiation.** If the upstream rejects `thinking`, `cache_control`, system blocks or effort, the server retries without that feature and remembers the decision for the model.
- **Fallback chain.** A per-model chain configured by the admin. It triggers only on overloaded / unavailable errors, only before the first content block, and skips models with a smaller context window.
- **Keep-alive.** A `Ping` is sent at least every 15 s.

## Accounting and limits

- Usage is normalized (see `Usage` in content.proto), priced from the model catalog and recorded per user, device and session. `MessageStop.cost_usd` reports the cost of the message.
- When a quota is exceeded, the server returns `RESOURCE_EXHAUSTED` with `ERROR_TYPE_QUOTA_EXCEEDED`.
- `RequestMetadata.request_class` allows routing and accounting per class, for example sending compaction to a cheaper model.

## Providers: global and personal

Providers and their API keys are configured in the apps' settings through `harness.llm.v1.ProviderService` (proto/harness/llm/v1/providers.proto); the local client forwards these calls to the server with the user's credentials. The admin JSON API (`/api/admin/providers`, `/api/admin/models`, `/api/admin/slots`) manages global providers only.

- **Global** providers (admins) are offered to every user, subject to each model's `roles`. Model ids are the admin's aliases.
- **Personal** providers are added by any user with their own key. Only the owner sees them and their models, and only the owner's requests can use them; another user (an admin included) gets the same `NOT_FOUND` as for a missing model. Their ids are generated (`u-<random>`, a reserved prefix) and their model ids are always `<provider id>/<upstream model>`, so they never clash with global aliases or other users' models. A user's catalog lists global models first, then their own; slots, the classifier model and the decision chain's `api_key_provider` accept global ones only. Personal providers cannot use `allow_private_network`. Usage and quotas are recorded for the user as for any request.
- **Groups and presets.** Providers come in two groups: `llm` (the gateway's models) and `classifier` (auto-mode decision services: a typed decision model or an HTTP policy webhook; global only, since the decision chain is server-wide, and they serve no chat models). `ListPresets` serves the well-known services the apps connect in one step (packages/core/src/services/presets.ts: OpenRouter, Anthropic, OpenAI, Kimi Code, Kimi Platform, Z.ai, DeepSeek, Gemini, xAI, Ollama; OpenRouter decisions and TypeSafe for classifiers): `CreateProvider` with `preset_id` needs only the preset's fields (usually the key), and a global id is derived when none is given. A preset without a model list ships a curated one, which `DiscoverModels` returns instead of asking the service. Providers created before presets are reported with the preset whose type and base URL they match. `TestProvider` is the one-click check: an LLM provider lists its models (401 / 403: the key is rejected), a classifier provider answers one sample decision.
- Keys are write-only everywhere (`api_key_set` instead); admins see a user's providers without even the key hint (`GET /api/admin/users/:id/providers`) and may delete them. Personal keys are encrypted with a per-user data key derived from `HARNESS_SECRET`, and upstream error text is scrubbed of provider secrets. This protects keys against database dumps and an admin account taken over through the API, not against someone who holds both the server secret and the database.

## Model discovery

`POST /api/admin/providers/:id/discover` (admin JSON API, global providers) and `ProviderService.DiscoverModels` (any provider the caller may change) list the upstream's models through the SSRF guard: Anthropic `/v1/models` (paginated), OpenAI-compatible `/models`. They return candidates to add to the catalog. Where a list says so, a candidate carries prices and capabilities: OpenRouter's `reasoning`, `supported_parameters` and `architecture.input_modalities`, Anthropic's `capabilities`. A model added from the list starts with them, and each discovery fills them in on models already added, only where the value is still the empty default and nobody set it by hand; a list that says nothing changes nothing. `ProviderService.SetModelEfforts` sets a model's effort levels by hand for lists that do not report them (OpenAI's), and is kept from then on, also when it is none. A model's recommended effort (capability `default_effort`, one of its levels) follows the same rules: it comes from OpenRouter's `reasoning.default_effort` when that is one of the model's levels, or by hand through `SetModelEfforts`, also as none; Anthropic and OpenAI lists report none. It goes when the level does.

## Limits

- **Spend limits:** daily and monthly USD per role, with per-user overrides, plus a per-session budget (`/api/admin/quotas`; session default from `HARNESS_SESSION_BUDGET_USD`). They are checked before the upstream call. A request that started under the limit may overshoot it; the next one is rejected.
- **Device endpoints:** the unauthenticated device-flow RPCs are rate-limited per client IP (`HARNESS_DEVICE_RATE_LIMIT_PER_MIN`, default 30; `HARNESS_TRUST_PROXY` to use the proxy-supplied address).

## Network safety

Provider base URLs are admin-supplied, so outgoing requests pass an SSRF guard: DNS is resolved and private, link-local and metadata ranges are blocked, including across redirects. A provider can opt in with `allow_private_network` for local vLLM / Ollama.
