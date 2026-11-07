# Diagnostics: what the model is offered, and the client's log

## The incident

In a desktop chat session the user asked for an artifact. The model answered that `ArtifactPublish` "isn't among the
functions I have available" and drew a `Visual` instead. A probe on the same machine found nothing wrong: sync opened,
the prompt bundle from the server had `ArtifactPublish`, no rule denied it. The installed app sent the sidecar's stdout
and stderr to `/dev/null`, so nothing of the session could be read afterwards.

## Root cause

`ArtifactPublish`, `KnowledgeSearch`, `KnowledgeRead` and the tracker tool (`TaskTracker`, now `Issues`) are registered only when the session has a
sync client (`tools.RegisterLibraryTools`, under `if s.Sync != nil` in `bootstrap.Open`). `bootstrap.Open` opened sync,
MCP, the pull request poller and the decision and search clients only inside `if gw == nil || src == nil`, that is,
only when it created the gateway and prompt adapters itself from the stored credentials.

`harness-client serve`, which the desktop app runs, creates one gateway and prompt source for all sessions
(`sharedAdapters`) and passes them in `Options.Gateway` and `Options.Prompts`. So every session of serve skipped the
whole block: no sync (no library tools, no synced settings, memory, output styles or agent definitions, no transcript
upload), no MCP servers at all (not even device-local ones: `openMCP` returns when `o.MCP` is nil), no PR/CI poller and
no `Session.User`. Nothing logged it, because `startSync` was never called. One-shot runs (`-p`) create their adapters
in `Open`, which is why a probe from the terminal saw sync working. This has been so since sync was wired into the
bootstrap; it is not a timing problem, and resume, fork, chat mode, output styles and the Advisor and subagent paths
are all downstream of it (subagents share the session's tool registry).

Ruled out on the way: deny rules (the engine's `IsDisabled`), the bundle (the fake bundle with the prompts behaves
like the server's), `agent.toolNames()` dropping tools (it only drops disabled and hidden ones; `Bundle.ToolDefs` drops
tools without a prompt, which is reported now as `no_prompt`), and `syncclient.Open` failing (it fails only when the
cache directory cannot be opened; a failed catch-up still returns an offline syncer).

## The fix

- `bootstrap.Options` carries `Tokens`, `User` and `SyncHub` beside shared adapters. `Open` runs the server block
  whenever it has credentials, whoever made the adapters; with injected adapters and no `Tokens` (tests) there is still
  no server.
- `sharedAdapters` (serve) sets them, and an `mcp.Client`. `SyncHub` is one sync client (cache, watch loop, uploader)
  for all sessions of the process, opened by the first session that needs it and retried by the next one if it failed.
  Per-session clients would have run one watch stream per open session on the same cache directory. `Server.Close`
  closes the hub after the sessions, so their last uploads go through it.
- `harness-client ci` uses `sharedAdapters` too and clears these fields: a review run keeps to the gateway and prompts,
  as before (no sync, transcript upload or MCP servers in CI). Revisit if reviews should search the knowledge base.
- A session with `NoPersistence` registers nothing with the uploader (no Track, no status on close): it keeps no
  transcript, so it must not upload one or change the synced state of its id (`debug tools` opens sessions that way).
  Its sent files still go to the object store.

Regression tests: `server/servertools_test.go` runs serve the way the desktop does (shared adapters, credentials,
`SyncHub`, a fake gateway and the `synctest` fake sync service at one address via `fakegateway.Server.Mount`) and
asserts that the tool definitions actually sent to the model contain the library tools for a folder session, a session
without a folder, the same session resumed after close, a fork, and a subagent. Without the fix the first assertion
fails with the user's symptom (`Visual` offered, `ArtifactPublish` not). `cmd/harness-client` checks that
`sharedAdapters` carries the connection.

Behaviour that changes for desktop sessions: synced settings, memory, output styles and agent definitions now apply;
MCP servers (server-side and device-local) start; transcripts upload; the PR/CI poller runs. All of this was the
intended behaviour of a signed-in session.

## Seeing what the model is offered

- `Session.ToolReport()` (`bootstrap/tooldiag.go`) lists the definitions the next request carries, in request order
  (`Agent.OfferedTools`, the same function the request loop uses), then each registered tool that is not offered with
  a reason: `disabled` (a deny rule without a pattern), `hidden` (a `ModelHider` whose device-local switch is off),
  `mcp_deferred` (loaded with ToolSearch), `no_prompt` (the session's frozen bundle has no definition, e.g. a resumed
  session on an old bundle), and `needs_sync` for the library tools when the session has no sync client, with the
  reason sync is off.
- `/tools` (built-in slash command), `AgentService.ListSessionTools` (`proto/harness/agent/v1/diagnostics.proto`) and
  `harness-client debug tools --session <id>` show it. `debug tools` asks a running client with `--url`/`--socket`; without
  them it opens the session itself as serve would, read-only, without hooks or MCP servers. The desktop's client has a
  random token only the app knows, so for the desktop `/tools` in the session is the way.
- `Session.SyncOff` says why a session with credentials has no sync (turned off for the run, `HARNESS_NO_SYNC=1`, or
  the sync client could not start). Such a session gets a warning in `Session.Warnings` (shown as a `session_warning`
  notice) naming the tools that are off. A session without credentials at all (tests, injected adapters) gets none.

## The client's log

- `clientlog` writes `$HARNESS_HOME/logs/client.log` from serve, one-shot (`-p`) and CI runs: info and up (debug with
  `--verbose` or `HARNESS_DEBUG=1`), rotated at 4 MiB to `client.log.1`..`.3`, files mode 0600. Stderr keeps getting
  warnings and up, as before. `HARNESS_NO_LOG_FILE=1` turns the file off.
- Nothing secret: attributes whose names contain token, secret, password, authorization, cookie, api key or credential,
  or are named key, prompt, content, text, system, input or body, are replaced by `[redacted]`; token-like values
  (`hat_…`, `sk-…`, `Bearer …`) are masked in messages, string values and errors. Code must still not log prompt text
  under another name; the redaction is the safety net, not the rule.
- Lines worth having: client start (version, platform), the listen address (never the token), each session opened
  (id, model, resumed, fork, no folder, bundle version, number of offered tools, sync state), and the existing warnings
  (sync unavailable, MCP disabled, settings problems).
- The desktop shell appends the sidecar's stdout and stderr (startup errors, panics) to `logs/sidecar.log`, rotated when
  the sidecar starts once it is past 2 MiB (three older files). The serve banner printed there names the address and
  says the token came from the environment; the token itself is never printed when the app passes it.

## Settings > About

"Copy diagnostics" copies `GetDiagnostics.text`: app and client versions, platform, the server's address and version,
whether the client is signed in, the log folder, each open session with its model, place, bundle, sync state and the
tools its model lacks with reasons, and the last 80 lines of `client.log` (redacted again). "Open logs folder" (desktop
only) opens `GetDiagnostics.logs_dir`. The client builds the report; the page only copies or opens it.

## Tested only on fakes

Everything above runs against `internal/fakegateway` and `syncclient/synctest`; no real server, account or model. The
desktop's log redirection has a unit test for rotation (`log_tests` in `src-tauri/src/lib.rs`); the installed app was
not rebuilt or run.
