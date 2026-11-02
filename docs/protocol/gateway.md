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

## Model discovery

`POST /api/admin/providers/:id/discover` (admin JSON API) lists the upstream's models through the SSRF guard: Anthropic `/v1/models` (paginated), OpenAI-compatible `/models`. It returns candidates the admin can add to the catalog.

## Limits

- **Spend limits:** daily and monthly USD per role, with per-user overrides, plus a per-session budget (`/api/admin/quotas`; session default from `HARNESS_SESSION_BUDGET_USD`). They are checked before the upstream call. A request that started under the limit may overshoot it; the next one is rejected.
- **Device endpoints:** the unauthenticated device-flow RPCs are rate-limited per client IP (`HARNESS_DEVICE_RATE_LIMIT_PER_MIN`, default 30; `HARNESS_TRUST_PROXY` to use the proxy-supplied address).

## Network safety

Provider base URLs are admin-supplied, so outgoing requests pass an SSRF guard: DNS is resolved and private, link-local and metadata ranges are blocked, including across redirects. A provider can opt in with `allow_private_network` for local vLLM / Ollama.
