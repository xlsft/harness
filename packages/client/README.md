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
| `workspace` | Env placeholders, git snapshot, project_key (normalized git remote) |
| `memory` | Project instructions with Claude Code semantics: managed / user / project CLAUDE.md, AGENTS.md fallback, `@imports`, lazy subdirectory files, path-scoped rules, auto memory |
| `settings` | Settings hierarchy (policy → CLI → local → project → user → org) with per-rule sources and the workspace trust gate |
| `syncclient` | `SyncService` client: document cache (`~/.harness/cache/sync`), catch-up + watch loop, CAS writes, session transcript uploader; `synctest` is an in-memory server for tests |
| `vcs` | Device providers `gh` and `glab`: CLI detection (version, logged-in hosts), PR/MR and CI polling for the session branch, `ReportDeviceProviders` / `ReportDeviceSnapshot` uploads; `vcstest` is a fake `VcsService` |
| `importer` | `import claude`: uploads `~/.claude` (memory, settings, skills, agents, commands, output styles) and copies sessions |
| `tools` | Read, Write, Edit, Bash (incl. background tasks), Glob, Grep, AskUserQuestion, EnterPlanMode/ExitPlanMode, TodoWrite, TaskStop |
| `outputstyle` | Output style discovery: bundle built-ins, user styles (sync or `~/.claude`/`~/.harness/output-styles`), project styles (`.claude`/`.harness/output-styles`), frontmatter parsing |
| `permissions` | Rule engine with Claude Code semantics (deny → ask → allow, compound Bash, wrappers, gitignore paths, read-only allowlist, protected paths, modes), workspace trust (`~/.harness/trust.json`), allow-always persistence |
| `decision` | Deciders of the `auto` permission mode: local rules (Go port of the server's `rules` provider, `packages/core/src/decision/rules.ts`), `harness.decision.v1.DecisionService` client, chain with fallback, session cache |
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
harness-client -p --output-style explanatory "walk me through the parser"
harness-client -p --permission-mode auto "run the tests and fix what fails"
harness-client serve --socket /tmp/harness.sock     # AgentService for frontends
harness-client import claude --dry-run              # preview the ~/.claude import (add --sessions for transcripts)
harness-client sync --watch                         # catch up the sync cache, upload sessions, follow changes
```

Environment variables:
- `HARNESS_HOME` overrides `~/.harness`.
- `HARNESS_KEYRING=0` stores credentials only in `~/.harness/credentials.json`.
- `HARNESS_SERVER_URL` + `HARNESS_ACCESS_TOKEN` use a fixed access token (for example on SSH runners).
- `HARNESS_TMPDIR` sets the root of the per-session scratch directories.
- `HARNESS_NO_VCS=1` stops the session from polling PR/CI state through `gh`/`glab`.
- `BASH_DEFAULT_TIMEOUT_MS` and `BASH_MAX_TIMEOUT_MS` set the Bash timeouts.
- `HARNESS_RG` sets the ripgrep path (`-` forces the Go fallback).
- `HARNESS_AUTOCOMPACT_PCT` sets the auto-compaction threshold as a share of the context window (`92` or `0.92`, default 92%).
- `HARNESS_DISABLE_BACKGROUND_TASKS=1` disables `run_in_background` and auto-backgrounding.

## Auto permission mode

`auto` (`--permission-mode auto`, `SetPermissionMode(PERMISSION_MODE_AUTO)`, or `permissions.defaultMode: "auto"` in user, policy or org settings) replaces permission prompts with decisions. The Shift+Tab cycle is `default → acceptEdits → plan → auto` (`permissions.ModeCycle`, `permissions.NextMode`).

For each tool call the rule engine runs first: deny rules deny; allow rules, read-only calls and scratch paths allow; protected paths (also as Bash write targets) and explicit ask rules always prompt the user. Only what would otherwise prompt goes to the decider:

1. the session cache (ALLOW/DENY by tool, cwd and normalized input; cleared on every mode change);
2. the local rules (`decision/rulesengine.go`, a port of the server's `rules.ts` checked against its test table `packages/core/test/fixtures/decision-rules.json`): a hard deny is final, a fast allow (tests, builds, linters, version queries, read-only commands, edits inside the working directories) runs without a network call;
3. `DecisionService.Decide` (10 s timeout) with the tool input, cwd, additional directories, the last 3 user prompts (2000 characters each), the assistant text before the call and client facts (subcommands, write paths, network hosts, git branch). If the server fails, the local verdict (ASK) stands.

ALLOW runs the tool, DENY returns the reason to the model (the turn continues), ASK prompts the user with the reason; headless runs deny it instead and list it in `permission_denials` with `reason` and `decided_by`. Every verdict emits a `Notice` of kind `auto_decision` (over AgentService its text is JSON: `decision`, `provider`, `reason`, `categories`, `tool_use_id`, `tool_name`, `cached`, `text`) and is recorded as `autoDecision` on the tool_result transcript entry.

Project and local settings files cannot turn auto on; `permissions.disableAutoMode: "disable"` (or `"disableAutoMode": true`) at any level turns it off.

## Output styles

An output style changes how the assistant communicates without changing the tools. Sources, later wins on the same name (id or display name, case-insensitive):

1. built-ins from the prompt bundle (`output_styles`; an empty map leaves only `default`);
2. user styles: sync documents of kind `OUTPUT_STYLE` in scope `USER`, or, before the first full sync, `~/.claude/output-styles/*.md` then `~/.harness/output-styles/*.md`;
3. project styles: `.claude/output-styles/*.md` then `.harness/output-styles/*.md`, in the git root and then the cwd.

A style file is markdown with optional frontmatter `name` (default: the file stem), `description` and `keep-coding-instructions` (default `false`, as in Claude Code); the body is the style text.

Selection: `--output-style` (on `-p` and `serve`), else the style recorded in a resumed transcript, else the `outputStyle` setting (policy settings still win over the flag). `SetOutputStyle` switches mid-session; `default` or empty clears the style. An unknown name is an error from `SetOutputStyle` and a warning (plus a `session_warning` notice) with the default style at startup. Every change is recorded as a `harness-meta` entry (`outputStyle`), so resume keeps it; `SessionInfo.output_style` and `ListOutputStyles.active` report it, and the stream-json `init` message carries it as `output_style`.

Delivery keeps the prompt cache: the style text goes to the model through the bundle's `output_style` reminder (`{{name}}`, `{{content}}`; without the template the text is sent as is), injected into the next user message at session start, on every change (switching back to `default` sends the default style), after a compaction and after a rewind that removed it. A resumed session whose history already carries the text does not resend it. The system prompt and the tool list never change on a switch, with one exception: while a style with `keep_coding_instructions = false` is applied, the system sections `doing_tasks` and `tool_policy` are omitted. That changes the cache prefix, so it is applied only while the conversation is empty (session start, or `SetOutputStyle` before the first message) and after a compaction. A mid-session switch to (or away from) such a style delivers the text right away and changes the sections at the next compaction; `SetOutputStyle` then emits an `output_style_deferred` notice.

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
