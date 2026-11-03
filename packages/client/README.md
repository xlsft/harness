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
| `tools` | Read, Write, Edit, Bash (incl. background tasks), Glob, Grep, AskUserQuestion, EnterPlanMode/ExitPlanMode, TodoWrite, TaskStop, TaskOutput, Wait |
| `web` | WebSearch (`harness.search.v1.SearchService` on the server) and WebFetch (local fetch, http → https except private hosts, same-host redirects only, 10 MB / 60 s limits, HTML → Markdown, binary refused, 15-minute in-memory cache per URL, answer from the small-slot model through the gateway with the bundle's `webfetch_summary` prompt) |
| `netguard` | Private-network classification (loopback, RFC 1918, link-local, ULA, CGNAT, metadata names, names resolving there) shared by the permission engine and WebFetch's dial guard |
| `outputstyle` | Output style discovery: bundle built-ins, user styles (sync or `~/.claude`/`~/.harness/output-styles`), project styles (`.claude`/`.harness/output-styles`), frontmatter parsing |
| `permissions` | Rule engine with Claude Code semantics (deny → ask → allow, compound Bash, wrappers, gitignore paths, read-only allowlist, protected paths, modes), workspace trust (`~/.harness/trust.json`), allow-always persistence; WebSearch / WebFetch need a rule (`WebSearch`, `WebFetch(domain:host)`), auto mode or the user, and a WebFetch of a private-network host is a user-only ask unless a rule names that exact host |
| `decision` | Deciders of the `auto` permission mode: local rules (Go port of the server's `rules` provider, `packages/core/src/decision/rules.ts`), `harness.decision.v1.DecisionService` client, chain with fallback, session cache |
| `session` | Append-only JSONL transcripts in Claude Code shape (`~/.harness/projects/<slug>/<id>.jsonl`), branch-aware rebuild, file checkpoints (`~/.harness/file-history/<id>/`), fork |
| `mcp` | MCP servers from `harness.mcp.v1.McpService`: remote tools through the core proxy, stdio servers via `IssueStdioLaunch` and the Go MCP SDK with per-device approval (`~/.harness/mcp-approvals.json`), `mcp__<server>__<tool>` tools, deferred tools and `ToolSearch` |
| `agent` | The loop (`Agent.RunTurn`), reminders, todos, checkpoints/rewind, compaction, and `Driver` (message queue, background turns) |
| `bootstrap` | Wires one session together |
| `worktree` | Session worktrees (`<repo>/.harness/worktrees/<name>`) and local branch listing for the new-session pickers |
| `server` | `harness.agent.v1.AgentService` over a unix socket (h2c) and, for browsers, a guarded loopback TCP listener (`tcp.go`: bearer token, Origin/CORS allowlist, Host check) |
| `relay` | Host side of `harness.relay.v1.RelayService` (remote control): mirrors live sessions to the server and runs viewer commands |
| `sdk` | Headless `-p` output: text, json, or a one-way stream-json event log |
| `rpc`, `pbconv` | Connect plumbing (HTTP/2 clients, auth retry, error mapping) and llm ↔ protobuf conversion |
| `gen` | Code generated from `/proto` (do not edit) |
| `internal/fakegateway` | Scripted fake harness server for tests and local end-to-end runs |
| `internal/gittest` | Throwaway git repositories for tests |
| `internal/fakemcp` | Fake `McpService` and a fake stdio MCP server (also as `cmd/fake-mcp-server`) for tests and e2e |

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
harness-client serve --listen 127.0.0.1:7801       # ... for browsers (see packages/ui/README.md)
harness-client import claude --dry-run              # preview the ~/.claude import (add --sessions for transcripts)
harness-client sync --watch                         # catch up the sync cache, upload sessions, follow changes
```

Environment variables:
- `HARNESS_HOME` overrides `~/.harness`.
- `HARNESS_KEYRING=0` stores credentials only in `~/.harness/credentials.json` (and the environments' encryption key only in `~/.harness/environments/key`).
- `HARNESS_WEB_URL` is reported as `InitializeResponse.web_url`: a web UI that opens a session at `<url>/?session=<id>`.
- `HARNESS_SERVER_URL` + `HARNESS_ACCESS_TOKEN` use a fixed access token (for example on SSH runners).
- `HARNESS_TMPDIR` sets the root of the per-session scratch directories.
- `HARNESS_NO_VCS=1` stops the session from polling PR/CI state through `gh`/`glab`.
- `BASH_DEFAULT_TIMEOUT_MS` and `BASH_MAX_TIMEOUT_MS` set the Bash timeouts.
- `HARNESS_RG` sets the ripgrep path (`-` forces the Go fallback).
- `HARNESS_AUTOCOMPACT_PCT` sets the auto-compaction threshold as a share of the context window (`92` or `0.92`, default 92%).
- `HARNESS_DISABLE_BACKGROUND_TASKS=1` disables `run_in_background` and auto-backgrounding.
- `HARNESS_NO_MCP=1` disables MCP servers.
- `HARNESS_NO_INTERRUPT_REPLY=1` (or the setting `"interruptReply": false`) turns off the model's short reply after the user stops a turn.
- `HARNESS_MCP_APPROVE=name1,name2` (or `*`) approves those stdio MCP servers for this run without asking (not remembered). Otherwise a stdio server asks on first use through the interactive frontend and the answer is stored per config hash.
- `HARNESS_TOOL_SEARCH`: `auto` (default; defer MCP tools behind `ToolSearch` when their definitions exceed 10% of the context window, `auto:<percent>` to change the share), `always`, `never`.

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

## Remote control

When the client is signed in to a server, `harness-client serve` and the TUI register the process as a relay host (`relay.Attach`) and mirror every session through `RelayService`, so the web can watch and control it. Opt out with `serve --remote-control=false`, `HARNESS_REMOTE_CONTROL=0`, or per project with the setting `"remoteControl": false`.

- Events: the session's live log (the same `SessionEvent`s `Subscribe` serves) goes up in batches every 100 ms or 64 events. The server's seq continues the stored history of a resumed session (`acked_seq` from `RegisterSession`); the history replayed into a resumed session's log is sent only when the server has no events for the session yet. After a failure the host resends from `acked_seq`, never resending `AssistantDelta`s. A lost epoch (`FAILED_PRECONDITION`) re-registers the session.
- Commands: run through the same `AgentService` methods local frontends call, in order per session (Compact and Rewind do not block later commands), deduplicated by `command_id` (a redelivered command is re-acked with its first outcome). Expired commands and `bypassPermissions` (as a mode or a spawn mode) are acked `OUTCOME_REJECTED`; an answer to a prompt that was already answered is `OUTCOME_ALREADY_RESOLVED`.
- Liveness: heartbeats at the server's interval (~20 s), reconnect with exponential backoff and jitter. On `SessionSuperseded` (another device took the session over) the host stops mirroring that session.
- Spawn: only with `Options.Daemon` and under `AllowedRoots` (no `harness daemon` command yet). Tunnels are not implemented.

## Worktree sessions and new-session pickers

`CreateSession` with `worktree` runs the session in a new git worktree of the repository containing `cwd` (`FAILED_PRECONDITION` outside a repository or before the first commit):

- path `<main checkout>/.harness/worktrees/<name>`, also when `cwd` is inside another worktree; `/.harness/worktrees/` is appended to `.git/info/exclude` once (the `.gitignore` is never edited);
- `name` defaults to `adjective-noun-xxxx` and must be one path segment (1-64 of `A-Za-z0-9._-`, starting with a letter or digit; `INVALID_ARGUMENT` otherwise); a taken name (existing directory or default branch) becomes `name-2`, `name-3`, ...;
- branch `harness/<name>` (or `branch`) is created with `--no-track` from `base_ref`, default the current branch (the commit when detached); an existing `branch` is checked out instead unless a worktree has it (`FAILED_PRECONDITION`), and `base_ref` then records its tip commit;
- the session cwd is the same subdirectory inside the worktree (created when it is untracked);
- the worktree is recorded as a `harness-meta` entry (`worktree`, with `cwd`), so `SessionInfo.worktree` survives resume and `ListSessions`, and the synced metadata carries it as `info.worktree`. `worktree` with `resume_session_id` needs `fork` (the fork moves into the new worktree);
- worktrees are never removed automatically; only a worktree whose session failed to start is rolled back.

`ListRecentFolders` scans the local transcripts of all projects (newest first) plus the open sessions; a session in a harness worktree counts for the matching folder of the main checkout. `ListDirectory` lists subdirectories (symlinks to directories included, dot entries only with `include_hidden`, at most 500, case-insensitive order). `ListBranches` lists local branches with `checked_out` (in any worktree) and the tip's commit time, newest first. git always runs without a shell, with a timeout and `GIT_TERMINAL_PROMPT=0`.

## Resumed sessions and titles

`AskUserQuestion` publishes `Question` and waits with the session in `NEEDS_INPUT`, without a timeout. Chat messages sent meanwhile (also with `DELIVERY_NOW`) stay queued and do not end the wait; after the answer they are read right after the tool result. `AnswerQuestion` resolves it with per-question answers (`skipped` per question), `QUESTION_OUTCOME_SKIPPED` (the model decides itself) or `QUESTION_OUTCOME_MESSAGE` (the queued messages leave the queue and become the free-form answer); `Interrupt` cancels it. A frontend that resubscribes gets the open question from the event log.

A session opened with `resume_session_id` (or `fork`) starts its event log with its history rebuilt from the transcript, so `Subscribe(after_seq=0)` shows the conversation so far; live events continue the seq without a gap. The replay follows the active branch across compactions and contains the durable events a frontend saw live: `UserMessage`, `TurnStarted` / `TurnFinished` per turn (usage summed from the transcript, wall time from timestamps, no cost; a turn cut short by an interrupt or by the process stopping is `RESULT_INTERRUPTED`), `AssistantMessage` with thinking, text and tool_use blocks, `ToolCallStarted` / `ToolCallFinished` pairs (result content and `structured_result_json`; a call without a result is finished as interrupted), `TodosUpdated` after `TodoWrite`, resolved `Question` / `PlanProposed` cards for `AskUserQuestion` / `ExitPlanMode` (a question carries its outcome and answers; one that was interrupted or still open when the process stopped is `QUESTION_OUTCOME_CANCELLED`, since the turn that asked it is gone), `PermissionModeChanged` where a prompt recorded a different mode, `Compacted` where each compaction happened (the summary itself is not shown) and a `rewind` notice for rewinds and forks at a checkpoint. Meta entries (project context, system reminders), deltas, permission prompts and background task events are not replayed. At most the last 200 turns and 10,000 events are replayed (`Server.HistoryTurns`, `Server.HistoryEvents`); older turns are dropped whole and a `history_truncated` notice comes first.

`SessionInfo` of a resumed session carries the recorded title, the model, the git branch and `diff_stats` (tracked changes against `HEAD` plus untracked files; in a harness worktree, against the merge base with `base_ref`), refreshed after every turn.

Titles: the first line of the first prompt is the provisional title. After the first turn (the second when the first prompt has fewer than three words) the client asks the small-slot model (the session model when the server has no small slot; request class `AUXILIARY`) for a title, with the bundle's `title_instruction` and a short rendering of the conversation (first prompt, start of the answer, later prompts; about 2,500 characters). It runs in the background with a 30 s timeout, once per open session; failures are logged and keep the provisional title. The result is written as an `ai-title` entry, published with `SessionUpdated` and set as the synced `info.title`. A `custom-title` entry always wins and is never overwritten. `ListSessions` and resume read the recorded custom or AI title. `Server.NoAITitles` turns this off; headless `-p` runs do not go through the server and get no AI title.

## Sessions of other machines

`ListSessions` also returns the user's sessions that the harness server lists for other machines (`SessionInfo.remote`, with `device_id` / `device_name`); a session that is live here, has a local transcript or carries this machine's device id is never shown as remote. With the relay viewer (on unless `HARNESS_REMOTE_CONTROL=0`), `RelayService.ListHosts` adds `device_online` and `remote_controllable` (open on its online machine); without it they stay unknown. `Subscribe` on a remote session follows it live through the relay when it is remote-controllable, otherwise it replays the server transcript and ends. Commands for a remote-controllable session are forwarded to `RelayService` (send, interrupt, permission / question / plan answers, model, effort, mode, output style, compact, tasks, file suggestions); other remote sessions refuse them with `FAILED_PRECONDITION`. Pin, unread, archive and rename write the server copy, delete removes it, and `CreateSession` with `fork` copies the server transcript into a new local session in `cwd` (paths rewritten to `cwd`, subagent transcripts not copied). See `server/remote.go`.

## Environments

An environment belongs to a primary folder and lives in `~/.harness/environments/<hash>.json` (0600, directory 0700), keyed by the folder's absolute path: a name, additional folders, new-session defaults (worktree, base branch) and environment variables in `.env` syntax (`environment/dotenv.go`). The `.env` text is sealed with AES-256-GCM; the key is in the OS keychain (service `harness-client`, account `environment-key`), or in `~/.harness/environments/key` (0600) with `HARNESS_KEYRING=0` or without a keychain. Values are never logged.

`CreateSession` in a folder adds the environment's existing additional folders to the working directories and its variables to the environment of shell commands and background tasks (they override the client's own; the harness's internal variables cannot be overridden; a `PATH` set there is replaced by the login-shell snapshot). `UpdateEnvironment` and `AddSessionDirectory` apply to the open sessions of the folder from their next tool call (working directories, permissions' working directories, variables) and queue an `environment_changed` reminder with the directories and variable names. The system prompt's environment section, MCP servers and background tasks already running keep what they started with. `SetSessionFolder` closes an idle session, moves its transcript to the new folder's project directory and resumes it there with the same model, mode and effort. Remote sessions refuse all of these. See `server/environment.go`.

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
