# LLM-gateway

Клиент **никогда** не говорит с провайдерами напрямую. Все вызовы моделей идут через core. Ключи провайдеров есть только на сервере.

Канонический формат — **Anthropic Messages API** (content blocks, tools, SSE-события). Сервер транслирует его в upstream: Anthropic, OpenAI Chat Completions, позже OpenAI Responses. Клиент видит один формат для любой модели.

Все запросы требуют `Authorization: Bearer hat_…`.

## `GET /api/llm/models`
Каталог моделей, доступных пользователю.
```json
{
  "models": [
    {
      "id": "claude-sonnet",                 // id, который клиент передаёт в `model` (алиас на сервере)
      "display_name": "Claude Sonnet 5.5",
      "provider": "anthropic-main",          // id провайдера, информационно
      "context_window": 1000000,
      "max_output_tokens": 64000,
      "capabilities": {
        "thinking": true,                    // принимает поле `thinking`
        "effort": ["low", "medium", "high", "xhigh", "max"],   // [] — effort не поддерживается
        "images": true, "pdf": true,
        "parallel_tool_calls": true,
        "prompt_cache": true
      },
      "pricing": { "input": 3.0, "output": 15.0, "cache_read": 0.3, "cache_write": 3.75 }   // USD за 1M токенов, может отсутствовать
    }
  ],
  "slots": { "main": "claude-sonnet", "small": "gpt-mini", "plan": "claude-opus" }   // значения по умолчанию для пользователя
}
```

## `POST /api/llm/messages`
Тело — подмножество Anthropic Messages API плюс поля harness:

```json
{
  "model": "claude-sonnet",
  "max_tokens": 32000,
  "system": [                                   // всегда массив текстовых блоков
    { "type": "text", "text": "…статика…", "cache": true },   // cache: true — точка кэша (см. ниже)
    { "type": "text", "text": "…динамика…" }
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
  "thinking": { "type": "enabled", "budget_tokens": 8000 },   // или {"type":"adaptive"} / отсутствует
  "effort": "high",                             // опционально; сервер понижает до поддерживаемого или отбрасывает
  "stream": true,                               // M0: только true
  "metadata": {
    "session_id": "ses_…",
    "request_class": "main | subagent | compaction | auxiliary",
    "agent_id": "…",                            // для субагентов
    "prompt_bundle_version": "2026-11-02.3"
  }
}
```

**Поддерживаемые типы блоков:**
- `text`, `image` (`source: {type:"base64", media_type, data}`), `document` (PDF, base64);
- `thinking`, `redacted_thinking`;
- `tool_use`, `tool_result` (`content` — массив `text`/`image` или строка).

**Правила для клиента:**
- Каждому `tool_use` в истории должен соответствовать `tool_result` в следующем user-сообщении. При прерывании клиент сам вставляет синтетический результат с `is_error: true`.
- `thinking`/`redacted_thinking` возвращаются в историю **без изменений** (подпись важна для Anthropic). Если upstream их не поддерживает, сервер выбросит их сам.
- System-reminders — это `text`-блоки внутри user-сообщений. Сервер их не интерпретирует.
- `cache: true` ставится на последний блок стабильного префикса: статика system, затем project context, затем последний блок последнего сообщения. Не больше 4 точек. Сервер переводит их в `cache_control` для Anthropic, для остальных провайдеров игнорирует. Порядок и содержимое префикса между запросами должны быть стабильны.

### Ответ: `text/event-stream`
События и их порядок — как в Anthropic streaming:

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

Дополнительно:
- `event: ping` — не реже раза в 15 с. Клиент рвёт стрим, если 5 минут не пришло ни одного байта.
- `event: harness_retry` — `{"attempt":2,"max_retries":10,"retry_delay_ms":4000,"error":{"type":"overloaded"}}`. Сервер ретраит upstream **до** первого content-блока. Клиент только показывает это пользователю.
- `event: error` — `{"type":"error","error":{…}}` (формат ошибки из README). После него стрим закрывается.

`stop_reason` ∈ `end_turn | tool_use | max_tokens | stop_sequence | refusal | pause_turn`. Сервер маппит `finish_reason` OpenAI: `stop→end_turn`, `tool_calls→tool_use`, `length→max_tokens`, `content_filter→refusal`.

`usage` нормализован для всех провайдеров:
- `input_tokens` — некэшированные входные токены;
- `cache_read_input_tokens` — у OpenAI это `prompt_tokens_details.cached_tokens`;
- `cache_creation_input_tokens`;
- `output_tokens`, включая reasoning.

### Что делает сервер (клиенту знать не нужно, но это контракт поведения)
- Трансляция в OpenAI Chat:
  - `system` → system-сообщение;
  - `tool_result` → сообщения роли `tool`, картинки из них — отдельным user-сообщением;
  - `thinking` → `reasoning_content` или отбрасывание;
  - `tools` → `functions`;
  - стрим `tool_calls` → `content_block_*` с `input_json_delta`.
- `effort` → `output_config.effort` (Anthropic) / `reasoning_effort` (OpenAI), понижение до поддерживаемого уровня.
- **Capability negotiation:** если upstream отверг `thinking`, `cache_control`, system-блоки или effort, сервер повторяет запрос без этой фичи и запоминает решение для модели.
- **Fallback-цепочка** моделей (настраивается admin) включается только на overloaded/unavailable и только до первого content-блока.
- Учёт usage и стоимости, квоты: при превышении `429 {type:"quota_exceeded"}`.

## `POST /api/llm/count_tokens`
Тело как у `/messages`, без `stream` и `max_tokens`. Ответ: `{ "input_tokens": 12345, "estimated": false }`. Если точный подсчёт недоступен, возвращается оценка с `estimated: true`.
