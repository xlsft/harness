# Headless protocol

`harness-client -p` runs an agent without a frontend. This page is the contract for scripts, CI and SDKs: flags, input and output messages, exit codes. [`packages/sdk-ts`](../../packages/sdk-ts) is a small TypeScript SDK over it; anything that can start a process and read lines can do the same.

Interactive frontends use `harness.agent.v1.AgentService` instead (see [README.md](README.md)).

## Signing in

The client signs in with stored credentials (`harness-client login <server-url>`) or, on a runner, from the environment:

| Variable | |
|---|---|
| `HARNESS_DEVICE_TOKEN` + `HARNESS_SERVER_URL` | A device token, from `harness-client ci login <server-url>`. Refreshes its access token by itself and lasts until the device is revoked. Use this in CI. |
| `HARNESS_ACCESS_TOKEN` + `HARNESS_SERVER_URL` | A fixed access token. It expires in an hour and cannot refresh. |

## Flags

| Flag | |
|---|---|
| `-p`, `--print` | Run headless. The prompt is the arguments, else stdin. |
| `--output-format text\|json\|stream-json` | The final text; one result object; or the event log below. Default `text`. |
| `--input-format text\|stream-json` | `stream-json` reads user messages from stdin, one per line. Needs `--output-format stream-json`. Default `text`. |
| `--include-partial-messages` | With `stream-json` output: also the raw stream events. |
| `--json-schema <json-or-path>` | Make the final message one JSON value valid against this schema. It comes back as `structured_output`. |
| `--max-budget-usd <n>` | Stop before the next model request once the run has spent this much, subagents included. Priced models only. |
| `--max-turns <n>` | Maximum tool-use round trips per turn. |
| `--system-prompt`, `--system-prompt-file` | Replace the system prompt for this run. |
| `--append-system-prompt`, `--append-system-prompt-file` | Add text after the system prompt. |
| `--allowedTools`, `--allowed-tools` | Permission rules allowed without asking, e.g. `"Bash(go test *) Edit"`. Repeatable. |
| `--disallowedTools`, `--disallowed-tools` | Permission rules that are always denied. |
| `--permission-mode` | `default`, `acceptEdits`, `plan`, `auto`, `dontAsk` or `bypassPermissions`. Nobody answers prompts in a headless run: what would ask is denied and listed in `permission_denials`. |
| `--model`, `--effort`, `--output-style`, `--add-dir` | As in the frontends. |
| `-c`, `-r <id>`, `--fork-session`, `--session-id`, `--no-session-persistence` | Which session, and whether to keep it. |

## Output

`text` prints the final message; with `--json-schema`, the JSON value. Errors go to stderr.

`json` prints one line, the result message. `stream-json` prints one JSON object per line:

| `type` | `subtype` | |
|---|---|---|
| `system` | `init` | First line: `session_id`, `cwd`, `model`, `tools`, `permissionMode`, `version`, `resumed`. |
| `system` | `input_error` | A line of stream-json input was refused: `error`. The run goes on. |
| `system` | `compact_boundary`, `todos`, `task_started`, `task_finished`, `permission_mode`, `auto_decision`, `api_retry` | What happened along the way. |
| `assistant` | | `message`: `role`, `content`, `model`, `usage`, `stop_reason`. |
| `user` | | `message`: the prompt or tool results. `client_uuids` carries the `uuid`s of the input messages it was made of; `tool_use_result` the tool's structured result. |
| `stream_event` | | With `--include-partial-messages`: `event`, a raw model stream event. |
| `result` | see below | One per turn; the last line of a `-p` run without stream input. |

Every line has `session_id` and `uuid`. Unknown types and fields may appear: skip them.

### The result message

```json
{
  "type": "result", "subtype": "success", "is_error": false, "session_id": "...", "uuid": "...",
  "result": "the final message", "structured_output": {"n": 3},
  "num_turns": 2, "duration_ms": 1200, "duration_api_ms": 900, "total_cost_usd": 0.0123,
  "usage": {}, "modelUsage": {}, "stop_reason": "end_turn", "permission_denials": []
}
```

| `subtype` | |
|---|---|
| `success` | `result` holds the final message. |
| `error_max_turns` | `--max-turns` was reached. |
| `error_max_budget_usd` | `--max-budget-usd` was reached. |
| `error_max_structured_output_retries` | The final message stayed invalid against `--json-schema` after one retry. |
| `error_during_execution` | Anything else; `interrupted` is true for an interrupt. |

Errors carry `errors`, a list of strings. `structured_output` is only there with `--json-schema` and a valid value.

## Stream input

With `--input-format stream-json`, each line on stdin is one object:

```json
{"type":"user","uuid":"optional-id","message":{"role":"user","content":"Fix the failing test"}}
{"type":"user","message":{"role":"user","content":[{"type":"text","text":"What is on this image?"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"..."}}]}}
{"type":"control","subtype":"interrupt"}
```

- `content` is a string, or `text` and `image` blocks. A string may be a slash command.
- Each message runs to its own `result`, in order, in one session: the agent keeps its context between them.
- A message that arrives during a turn waits for that turn's result.
- `interrupt` stops the turn in progress, which ends with `"interrupted": true`. Later messages still run.
- A line that cannot be read gets a `system` `input_error` and is skipped.
- End of input ends the run after the last message.

## Exit codes

| Code | |
|---|---|
| 0 | The last turn succeeded. |
| 1 | It failed, or the run could not start. |
| 2 | The flags are wrong. |
