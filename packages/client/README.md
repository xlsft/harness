# packages/client

The universal local client of harness, written in Go. It runs the agent loop and executes tools on the local filesystem. Every model call goes through the harness server's LLM gateway. The client holds no provider keys and no prompt texts of its own: prompts come from the server's prompt bundle.

It ships in two forms:
- a Go library, imported by `packages/cli`;
- the `harness-client` binary: headless runs and `AgentService` for the desktop UI and SSH sessions.

## Layout

| Package | Purpose |
|---|---|
| `llm` | Canonical message model (content blocks, usage, requests) and the transport-neutral stream accumulator |
| `gateway` | `Gateway` interface plus `Send` (retries before the first content block, accumulation) |
| `gateway/connectgw` | Connect/gRPC adapter for `harness.llm.v1.GatewayService` |
| `auth` | `Service` interface, Connect adapter, device-flow `Login`, `Manager` (token refresh), keychain/file credential `Store` |
| `prompts` | `Source` interface, Connect adapter, bundle cache (`~/.harness/cache/prompts`), placeholder rendering, system assembly with cache markers |
| `workspace` | Env placeholders, git snapshot, CLAUDE.md / AGENTS.md discovery |
| `tools` | Read, Write, Edit, Bash (incl. background tasks), Glob, Grep, AskUserQuestion, EnterPlanMode/ExitPlanMode, TodoWrite, TaskStop |
| `permissions` | Rule engine with Claude Code semantics (deny → ask → allow, compound Bash, wrappers, gitignore paths, read-only allowlist, protected paths, modes), workspace trust (`~/.harness/trust.json`), allow-always persistence |
| `session` | Append-only JSONL transcripts in Claude Code shape (`~/.harness/projects/<slug>/<id>.jsonl`), branch-aware rebuild, file checkpoints (`~/.harness/file-history/<id>/`), fork |
| `agent` | The loop (`Agent.RunTurn`), reminders, todos, checkpoints/rewind, compaction, and `Driver` (message queue, background turns) |
| `bootstrap` | Wires one session together |
| `server` | `harness.agent.v1.AgentService` over a unix socket (h2c) |
| `sdk` | Headless `-p` output: text, json, or a one-way stream-json event log |
| `rpc`, `pbconv` | Connect plumbing (HTTP/2 clients, auth retry, error mapping) and llm ↔ protobuf conversion |
| `gen` | Code generated from `/proto` (do not edit) |
| `internal/fakegateway` | Scripted fake harness server for tests and local end-to-end runs |

## Usage

```bash
go build ./cmd/harness-client

harness-client login https://harness.example.com
harness-client whoami
harness-client -p "fix the failing test" --allowedTools "Edit Bash(go test *)" --output-format json
harness-client -p -c "and now the README"           # continue the latest session in this directory
harness-client -p -c --fork-session "try another way"  # copy the latest session into a new one
harness-client -p -c "/compact keep the API notes"   # compact the latest session
harness-client -p --permission-mode plan "plan the refactor"
harness-client serve --socket /tmp/harness.sock     # AgentService for frontends
```

Environment variables:
- `HARNESS_HOME` overrides `~/.harness`.
- `HARNESS_KEYRING=0` stores credentials only in `~/.harness/credentials.json`.
- `HARNESS_SERVER_URL` + `HARNESS_ACCESS_TOKEN` use a fixed access token (for example on SSH runners).
- `HARNESS_TMPDIR` sets the root of the per-session scratch directories.
- `BASH_DEFAULT_TIMEOUT_MS` and `BASH_MAX_TIMEOUT_MS` set the Bash timeouts.
- `HARNESS_RG` sets the ripgrep path (`-` forces the Go fallback).
- `HARNESS_AUTOCOMPACT_PCT` sets the auto-compaction threshold as a share of the context window (`92` or `0.92`, default 92%).
- `HARNESS_DISABLE_BACKGROUND_TASKS=1` disables `run_in_background` and auto-backgrounding.

## Local end-to-end run without the real server

```bash
go run ./internal/fakegateway/cmd/fakegateway -init-project /tmp/demo
go run ./internal/fakegateway/cmd/fakegateway -addr 127.0.0.1:7777 &
cd /tmp/demo && git init -q
HARNESS_HOME=/tmp/demo-home HARNESS_KEYRING=0 harness-client login http://127.0.0.1:7777
HARNESS_HOME=/tmp/demo-home harness-client -p "fix the test" --allowedTools "Edit Bash(go test *)" --output-format stream-json
```

## Code generation

Go code in `gen/` is generated from `/proto` with `buf generate` (see the root `buf.gen.yaml`). If the remote buf plugins are not reachable, install `protoc-gen-go` and `protoc-gen-connect-go` locally and use the same template with `local:` plugins.
