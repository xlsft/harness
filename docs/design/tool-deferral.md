# Tool deferral

A session of the desktop app registers about 110 built-in tools. Their definitions (name, description, input schema)
went with every request: on a measured session, about 36K of the first request's 62K input tokens, sent again every
turn, while most sessions use a handful of them. Deferral sends the definitions of rarely used tools only once the model
asks for them. It extends the mechanism MCP tools already had (ToolSearch and the `deferred_tools` reminder) to the
built-in tools; there is one mechanism, not two.

## What is deferred

The classification is one table, `builtinTiers` in `packages/client/toolsearch/classify.go`.

- **Always loaded**: the core coding set and what nearly every session or turn uses, plus what the system prompt
  steers to in ordinary work: Read, Write, Edit, MultiEdit, Glob, Grep, Bash, PowerShell, TaskOutput, TaskStop, Wait,
  Monitor, WebFetch, WebSearch, TodoWrite, AskUserQuestion, EnterPlanMode, ExitPlanMode, WrapUp, Agent, Skill, Advisor
  (present only when the user configured an advisor model) and ToolSearch itself.
- **Deferred**: tools for one kind of task: LSP, LS, notebooks; agents beyond starting one (SendMessage, ListAgents,
  TaskInput, Workflow, the task graph); moving the session (worktrees, ChangeDirectory, RequestDirectory, ProposeBranch,
  SyncWithBase); ReportFindings, MarkChapter; reaching the user (PushNotification, SendUserFile, Show, Visual,
  ReadNotifications, OpenInEditor, RevealPath, KeepAwake); the library (Memory, Knowledge, Issues, documents,
  ArtifactPublish); pull requests and CI; schedules; other sessions; session settings and preferences; terminal panes;
  previews; the browser; computer use; MCP resources.
- A tool the table does not name is always loaded, so a new tool is never hidden by accident.
  `TestEveryBundleToolIsClassified` fails until a tool added to `packages/core/prompts/tools` is put in a tier on
  purpose.

MCP tools keep their own rule (`HARNESS_TOOL_SEARCH`, deferred above 10% of the context window, `always_load` per
server); they now sit in the same loader.

## The setting

- `deferBuiltinTools`, a personal preference synced like the others (`UserPreferences.defer_builtin_tools`, field 60;
  `UpdateUserPreferencesRequest.defer_builtin_tools`). Settings > Sessions > Context has the switch, "Load rarely used
  tools on demand", with the trade-off in its description.
- **Default: on.** The saving is large and paid on every request of every session (see the numbers below), while the
  cost is one extra step the first time a session needs a deferred tool, which most sessions never do. Claude Code makes
  the same choice for its tool search. Turning it off restores the old behavior exactly.
- The organization's managed policy can force it: `deferBuiltinTools: true|false` in the policy document
  (`ManagedPolicy.defer_builtin_tools`, field 420, an optional bool; unset leaves it to each user). The view clients
  report (`ManagedPolicyView.defer_builtin_tools`, 420) drives "Set by your administrator" next to the disabled switch,
  `UserPreferences.defer_builtin_tools` reports the forced value, and the client refuses to change the preference while
  the policy sets it (FAILED_PRECONDITION). With several policy documents, off in any of them wins: it is the choice
  that keeps every definition in front of the model. The admin form (Settings > Managed policy) has "Each user chooses",
  "Loaded on demand" and "Always loaded".
- **When it applies**: when a session opens (`bootstrap.deferBuiltinTools`). An open session keeps its tool list, so a
  change never breaks a running session's prompt cache; resuming a session opens it again and reads the setting then.
  A server whose prompt bundle has no ToolSearch entry turns built-in deferral off.

## How loading works

- `toolsearch.Loader`, one per session, shared by the session's agents like the MCP catalog. Sources supply the
  deferred tools: the built-in source (registered tools the table defers, defined by the bundle, not disabled or
  hidden) and the MCP manager. The loader keeps the loaded names in load order.
- ToolSearch (`toolsearch.Tool`, moved from `packages/client/mcp`) is registered whenever anything is deferred. Its
  queries are unchanged: `select:<names>`, keywords, `+word`. Keyword search now also splits built-in names at case
  changes (`BrowserNavigate` matches "browser navigate"). It loads the matches and returns their definitions in a
  `<functions>` block.
- The model learns what is deferred from the `deferred_tools` reminder: one `name: summary` line per tool, the summary
  being the first sentence of the tool's description (`toolsearch.Summary`, data, not a second hand-written list). Every
  agent that can call ToolSearch gets it with its first message; it comes again after a compaction (listing only what
  is still unloaded) and when a resumed session's MCP catalog moved. About 1.4K tokens, once, instead of 18K on every
  request.
- An agent whose tool list leaves ToolSearch out (the read-only Explore and Plan types) gets its deferred tools in full:
  it could not load them otherwise.

### Calling a deferred tool that was not loaded

The system prompt, skills and reminders name tools the model is expected to call directly (`ReportFindings` in
`/code-review`, `ScheduleWakeup` in `/loop`, `Workflow` for saved workflows, `PrMonitor`, `ReadNotifications`). For
built-in tools the client **loads them transparently** (`agent.loadOnCall`) rather than returning an error, because an
error that only says "load it first" costs a model round trip every time a prompt sends the model to such a tool. A
guessed input must not run with defaults, though (Go's decoder ignores unknown fields), so the input is validated
against the tool's JSON schema first: valid input loads the tool and runs the call through the usual permission
checks; invalid input loads the tool as well and returns an InputValidationError that carries the definition, so the
retry is informed. MCP tools keep the error naming ToolSearch: their schemas are third-party and a call reaches an
outside server.

The system prompt says that tools it names may be deferred (TOOL-12 in `prompt-requirements.md`), so a model does not
conclude a tool is missing.

### Persistence

- Each load is recorded in the transcript (`Entry.loadedTools`, a meta entry). `session.Rebuild` collects them from
  every entry, so the set survives compactions and rewinds; a resumed session restores them in order
  (`Loader.Restore`). Transcripts from before the record are replayed from their ToolSearch calls and their calls of
  deferred tools (`Loader.RestoreFromHistory`, what MCP did before), and what that finds is recorded.
- A compaction keeps the loader as it is: the loaded definitions stay in the request, and the listing of what is still
  deferred is sent again.

## Prompt caching

Tools are the first part of the cached prefix (tools, then system, then messages; the gateway's Anthropic adapter
places at most four breakpoints, the client marks the last static system block, the project context and the last
message). The request's tool list is built in a fixed order (`agent.toolDefs`):

1. the bundle definitions of the registered tools that are not deferred, in registration order;
2. ExtraTools: MCP tools that are not deferred, and browser fallbacks;
3. the loaded deferred tools, built-in and MCP alike, in load order.

The first two parts never change while the session runs, and a load only appends to the third, so nothing that was
sent is ever reordered. A load still changes the start of the prefix, so the request after it writes the cache anew
once; the requests after that read it again. That is why the prompts ask for every tool a step needs in one ToolSearch
call, and why the always-loaded set includes everything a typical turn uses. (Provider-side deferral, such as
Anthropic's `defer_loading` with tool references, would avoid even that one write; it is provider-specific and left for
later.)

## Diagnostics

- `/tools`, `ListSessionTools` and `harness-client debug tools`: a deferred built-in tool that is not loaded has the
  reason `deferred` ("listed by name, loaded with ToolSearch or by its first call"); `FormatToolReport` lists deferred
  tools on a line of their own, not under "Not offered". Loaded ones are offered like any other.
- The context breakdown (`GetContextUsage`): "System tools" counts the built-in definitions actually sent, loaded
  deferred ones included; `agent.ContextUsage.DeferredSystemTools` is what deferral holds back.

## Measurements

`go test ./bootstrap -run TestDeferralTokenSavings -v` opens a new session against the fake gateway with the real
prompt bundle (`packages/core/prompts`) and reads the context breakdown, once with the setting off and once on. The
test session registers 71 built-in tools (no browser, computer use, terminal or preview hosts, which the desktop app
adds):

| | tools offered | system tools (tokens) |
|---|---|---|
| deferral off | 71 | 30,654 |
| deferral on | 21 | 12,653 (18,333 held back; the listing is about 1,439 once) |

The desktop app's extra tools (13 browser, 12 computer use, 5 terminal, 4 preview) are all deferred, so its always-loaded
set is the same 21 tools: about 12.6K tokens per request instead of the measured 36K.

## Not done

- Per-model tiers (a small local model might want an even smaller always-loaded set) would need the classification in
  the bundle; it lives in the client for now.
- Subagents share the session's loader: a load by one agent appends to the others' tool lists too, as MCP loads did.
- A tool that stops hiding itself while the session runs (computer use switched on mid-session) becomes loadable with
  ToolSearch, but the listing the model got at the start does not name it until the next compaction sends it again.
