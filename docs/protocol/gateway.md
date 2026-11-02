# LLM gateway

The client **never** talks to model providers directly. Every model call goes through core, and provider keys exist only on the server.

The canonical format is the **Anthropic Messages API**: content blocks, tools and SSE events. The server translates it to the upstream API (Anthropic, OpenAI Chat Completions, later OpenAI Responses), so the client sees one format for every model.

Every request requires `Authorization: Bearer hat_…`.

## `GET /api/llm/models`

Catalog of models available to the user.

```json
{
  "models": [
    {
      "id": "claude-sonnet",                 // the value the client sends as `model` (a server-side alias)
      "display_name": "Claude Sonnet 5.5",
      "provider": "anthropic-main",          // provider id, informational
      "context_window": 1000000,
      "max_output_tokens": 64000,
      "capabilities": {
        "thinking": true,                    // accepts the `thinking` field
        "effort": ["low", "medium", "high", "xhigh", "max"],   // [] means effort is not supported
        "images": true, "pdf": true,
        "parallel_tool_calls": true,
        "prompt_cache": true
      },
      "pricing": { "input": 3.0, "output": 15.0, "cache_read": 0.3, "cache_write": 3.75 }   // USD per 1M tokens, optional
    }
  ],
  "slots": { "main": "claude-sonnet", "small": "gpt-mini", "plan": "claude-opus" }   // per-user defaults
}
```

## `POST /api/llm/messages`

The body is a subset of the Anthropic Messages API plus harness fields:

```json
{
  "model": "claude-sonnet",
  "max_tokens": 32000,
  "system": [                                   // always an array of text blocks
    { "type": "text", "text": "…static…", "cache": true },   // cache: true marks a cache breakpoint (see below)
    { "type": "text", "text": "…dynamic…" }
  ],
  "messages": [
    { "role": "user", "content": [ { "type": "text", "text": "…" } ] },
    { "role": "assistant", "content": [
        { "type": "thinking", "thinking": "…", "signature": "…" },
        { "type": "text", "text": "…" },
        { "type": "tool_use", "id": "toolu_1", "name": "Read", "input": { "file_path": "/a" } } ] },
    { "role": "user", "content": [
        { "type": "tool_result", "tool_use_id": "toolu_1", "is_error": false,
          "content": [ { "type": "text", "text": "…" } ] } ] }
  ],
  "tools": [ { "name": "Read", "description": "…", "input_schema": { "type": "object", "properties": {}, "required": [] } } ],
  "tool_choice": { "type": "auto" },           // auto | any | tool{name} | none
  "thinking": { "type": "enabled", "budget_tokens": 8000 },   // or {"type":"adaptive"}, or omitted
  "effort": "high",                             // optional; the server lowers it to a supported level or drops it
  "stream": true,                               // M0: must be true
  "metadata": {
    "session_id": "ses_…",
    "request_class": "main | subagent | compaction | auxiliary",
    "agent_id": "…",                            // set for subagents
    "prompt_bundle_version": "2026-11-02.3"
  }
}
```

**Supported block types:**
- `text`, `image` (`source: {type: "base64", media_type, data}`), `document` (PDF, base64);
- `thinking`, `redacted_thinking`;
- `tool_use`, `tool_result` (`content` is an array of `text` / `image` blocks, or a string).

**Client rules:**
- Every `tool_use` in history must have a matching `tool_result` in the next user message. When a turn is interrupted, the client inserts a synthetic result with `is_error: true`.
- `thinking` and `redacted_thinking` blocks go back into history **unchanged** — Anthropic validates the signature. If the upstream does not support them, the server drops them.
- System reminders are `text` blocks inside user messages. The server does not interpret them.
- Put `cache: true` on the last block of each stable prefix: the static system part, the project context, and the last block of the last message. Use at most 4 breakpoints. The server turns them into `cache_control` for Anthropic and ignores them for other providers. Keep the order and content of the prefix stable between requests.

### Response: `text/event-stream`

Events and their order match Anthropic streaming:

```
event: message_start
data: {"type":"message_start","message":{"id":"msg_…","model":"claude-sonnet","role":"assistant","content":[],"stop_reason":null,"usage":{"input_tokens":0,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hel"}}
// delta.type: text_delta | thinking_delta | signature_delta | input_json_delta (partial_json)

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"input_tokens":1200,"output_tokens":340,"cache_read_input_tokens":1000,"cache_creation_input_tokens":0}}

event: message_stop
data: {"type":"message_stop","harness":{"cost_usd":0.0042,"upstream_model":"claude-sonnet-5-5","retries":0}}
```

Additional events:
- `event: ping` at least every 15 s. The client drops the stream after 5 minutes without a single byte.
- `event: harness_retry` — `{"attempt":2,"max_retries":10,"retry_delay_ms":4000,"error":{"type":"overloaded"}}`. The server retries the upstream only **before** the first content block; the client just shows it to the user.
- `event: error` — `{"type":"error","error":{…}}` (error format from [README.md](README.md)). The stream closes after it.

`stop_reason` is one of `end_turn | tool_use | max_tokens | stop_sequence | refusal | pause_turn`. OpenAI `finish_reason` maps as `stop → end_turn`, `tool_calls → tool_use`, `length → max_tokens`, `content_filter → refusal`.

`usage` is normalized across providers:
- `input_tokens` — uncached input tokens;
- `cache_read_input_tokens` — for OpenAI, `prompt_tokens_details.cached_tokens`;
- `cache_creation_input_tokens`;
- `output_tokens`, including reasoning tokens.

### Server behavior

The client doesn't need to know these details, but they are part of the behavioral contract.

- **Translation to OpenAI Chat:**
  - `system` → a system message;
  - `tool_result` → `tool` role messages, with images moved into a separate user message;
  - `thinking` → `reasoning_content`, or dropped;
  - `tools` → `functions`;
  - streamed `tool_calls` → `content_block_*` events with `input_json_delta`.
- **Effort.** `effort` maps to `output_config.effort` (Anthropic) or `reasoning_effort` (OpenAI), lowered to the nearest supported level.
- **Capability negotiation.** If the upstream rejects `thinking`, `cache_control`, system blocks or effort, the server retries without that feature and remembers the decision for the model.
- **Fallback chain.** A per-model chain configured by the admin; it triggers only on overloaded / unavailable errors and only before the first content block.
- **Usage and quotas.** Usage and cost are recorded per user. When a quota is exceeded the server returns `429 {type: "quota_exceeded"}`.

## `POST /api/llm/count_tokens`

Same body as `/messages` without `stream` and `max_tokens`. Response: `{ "input_tokens": 12345, "estimated": false }`. When an exact count is unavailable, the server returns an estimate with `estimated: true`.
