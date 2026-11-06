# Agent core gaps

Working list for closing the agent-core rows of [the comparison](../comparison.md), which is out of date. Every `No`, `Partial` and `Planned` row of the tables named below was checked against the code, not against the document. A row's commit is filled in when the work lands, so someone else can pick up from here.

Status values: **done before** (the code on `main` already has it, the comparison is stale), **closed** (done here), **open** (worth doing, not done yet), **skipped** (with the reason).

Everything here is tested against the fake gateway, fake MCP server and local git repositories. No real account, key, model, code host, MDM profile, Windows machine or Linux kernel limit was used; [What only fakes have seen](#what-only-fakes-have-seen) lists what that leaves unproven.

## Decisions

- Agent definitions, skills and commands stay in `.harness/`, per the rule that nothing is read from `.claude/` or `CLAUDE.md` at runtime. `.worktreeinclude` is a repository file of its own, in no vendor's folder, and is read from the repository root.
- Rows that only name another vendor's plan, program or model are not capabilities and are skipped.
- Out of scope because they live on other branches: document tools, the Extensions screen, models, SSH, real Chrome, i18n.
- **A definition cannot grant what the user would have to switch on with a warning.** `permissionMode` can always make an agent stricter. It can make it freer only from default to acceptEdits or auto, and from acceptEdits to auto. Bypass from a definition is ignored, plan mode of the session always wins, and a mode the policy turned off isn't available (`permissions.AgentMode`).
- **An agent's own MCP servers need the user's decision on this device,** as a repository's `.mcp.json` does: a definition is a repository file or a document off the server. The prompt is user-only and blocks the `Agent` call; the answer is bound to the config hash in `mcp-approvals.json`. Plugin agents get neither `permissionMode` nor inline servers, as with hooks.
- **A fork starts at the last complete exchange.** The running turn's response still has an open tool call (the `Agent` call itself), and a request with an open call is refused, so that response is cut (`subagent.ForkEntries`). A fork stays on its parent's model, since the copied thinking and cache belong to it.
- **Machine policy sits below the server's policy and above everything else.** Sources, in order: server documents, macOS managed preferences (`/Library/Managed Preferences/com.xlsft.harness.plist`, top level or under `Settings`) or the Windows registry (`HKLM\SOFTWARE\Policies\xlsft\harness`, value `Settings`, a JSON string), `settings.json` in the system folder, then `settings.d/*.json` in name order. The system folder is the one that already holds the managed `AGENTS.md`. A hook in a machine policy needs no device trust, because only an administrator can write there; a hook delivered by the server still does.
- **`restrictReads` is a tool rule, not a boundary.** The read tools are refused outright. For the shell it can only go by paths a command line names, so a path built at run time gets through, and a word like `/foo/` passed to grep is refused too. The name says "restrict", the docs say this, and the sandbox stays the boundary. Any level can turn it on and none can turn off what another turned on.
- **A stale worktree is removed only when it holds nothing:** no uncommitted change, no untracked file, no commit that only it has, not locked, no open session or running agent in it, and untouched for `cleanupPeriodDays` (30; 0 turns it off). Ignored files don't count. Only `.harness/worktrees/*` is looked at, and a branch goes with it only if the client named it.
- **`.worktreeinclude` copies only what is both named and git-ignored,** never a symlink, never over an existing file, at most 2000 files and 512 MB. A file that isn't ignored would show up as a change in the new worktree.
- **`/fewer-prompts` never offers** rules for commands that delete, publish, escalate or run what they fetch (`neverSuggest` in `server/reports.go`), however often they were approved, and `apply` refuses a request from another device.
- **`/deep-research`, `/orchestrate`, `/interview`, `/recap` are prompts,** like `/init`: a user's skill of the same name replaces them. "Automatic orchestration" is therefore something the user asks for per task, not a mode that decides by itself; a mode would spend the user's budget on a guess.
- **Report scanning only warns.** `scanReport` catches the plain phrasings of planted instructions in a subagent's report and adds a warning next to the report. A clean scan says nothing, and the report stays marked as another model's output either way.
- **`stdin` on Bash is opt-in.** A command with an open input that reads it waits forever, where today it sees the end of input and goes on, so `TaskInput` works only on a command started with `stdin: true`.
- The usage rows count from the transcript: each assistant message now records `costUSD`. Sessions from before this change report tokens only, with a cost of what was recorded since.

## Settings added

| Key | Who can set it | What it does |
|---|---|---|
| `permissions.restrictReads` | any level, only turns on | refuse reads outside the working directories |
| `permissionProfiles`, `permissionProfile` | as the level's permissions; `--permission-profile` chooses | named `{allow, ask, deny, defaultMode, ...}` blocks; a project's widens nothing in an untrusted folder |
| `shellEnvironmentPolicy` | highest level; project files only when trusted | `{inherit: all\|core\|none, exclude, include, excludeSecrets, set}` for the model's commands. Not hooks or MCP servers |
| `bashMemoryLimitMB` | smallest of the levels | `ulimit -v` on Linux; macOS ignores such a limit, so nothing is claimed there |
| `webSearchMode` | any level can disable; else highest | `live`, `cached` (an hour, in the client's memory), `disabled` |
| `promptCacheTtl` | highest level | `5m` or `1h`; only the Anthropic adapter acts on it |

## Agent loop and context

| Row | Status | What, or why not | Commit |
|---|---|---|---|
| Cache TTL controls | closed | `promptCacheTtl`, `RequestMetadata.cache_ttl`, `ttl` on `cache_control` | 3 |
| Goal mode until condition | done before | `/goal`, `server/goal.go` | |
| Recap on return | closed | `/recap` | 2 |
| Session transcript export | done before | `LibraryService.ExportSession` | |
| Transcript search | done before | `TranscriptSearchService`, merged with local files | |

## Built-in tools: files and search

| Row | Status | What, or why not | Commit |
|---|---|---|---|
| Multi-chunk edit | closed | `MultiEdit`: several replacements in one file, all or none | 2 |
| List directory | closed | `LS` | 2 |
| LSP servers built in | skipped | Same model as the others: specs for four languages, binary on PATH, `lspServers` for more. Installing servers is a packaging choice, not a gap | |

## Shell and background tasks

| Row | Status | What, or why not | Commit |
|---|---|---|---|
| PowerShell tool | skipped | Windows doesn't ship yet and there is no Windows machine to test on. To do it: a `PowerShell` tool over `Shell` with `kind` powershell, registered on Windows only, with rules in `permissions/shell.go` | |
| Env filter for commands | closed | `shellEnvironmentPolicy` | 2 |
| Interactive stdin to a command | closed | `stdin` on Bash, `TaskInput` | 3 |
| Managed sidecar processes | skipped | `Preview*` with `.harness/launch.json` is this capability | |
| Memory cap per tool | closed | `bashMemoryLimitMB`, Linux only like the others; prefix tested, never run on Linux | 2 |

## Web, search, browser

| Row | Status | What, or why not | Commit |
|---|---|---|---|
| Domain filters on search | done before | `allowed_domains`, `blocked_domains` | |
| Cached, live, disabled modes | closed | `webSearchMode` | 2 |
| Drive the user's real signed-in browser, Browser extension | skipped | Real Chrome is on another branch | |
| Recording of browser session | closed | `BrowserRecord`: start, stop, an animated GIF in the scratch directory. Capture and encoding are tested with stub screenshots, not against Chrome | 3 |
| Comments on web pages | skipped | A Codex app surface; `BrowserPane` take-over and the composer cover pointing the agent at a page | |
| Public tunnel to preview | skipped | Needs a hosted relay we don't run; a self-hoster's own tunnel works with `Preview*` | |
| Code search index or panel | skipped | `Grep`, `LSP` and the Files pane cover it; an index is a project of its own with little gain over ripgrep | |
| Deep research workflow | closed | `/deep-research` | 2 |

## Subagents and teams

| Row | Status | What, or why not | Commit |
|---|---|---|---|
| Custom agent fields | closed | Beyond name, description, model, tools, read_only, hooks: `disallowedTools`, `permissionMode`, `maxTurns`, `effort`, `skills`, `isolation`, `mcpServers`; lists or comma strings; `model: inherit`. Not done: `memory`, `background` (agents are always in the background), `color` | 1b |
| Agents per run with own permissions | closed | `permissionMode`, `Request.Mode`, `permissions.AgentMode` | 1b |
| Agents with own MCP servers | closed | `mcpServers`: a name keeps a session's server through a `tools` list, an entry starts one for the agent. It runs until the session ends | 1b |
| Transient agent defined on the fly | closed | `--agents` on `-p` and `serve`; wins over definitions of that name. Not in `packages/sdk-ts` yet | 1b |
| Forked subagent with full history | closed | `fork` on `Agent`, reminder `agent_fork` | 1b |
| Agent teams with shared task list | done before | `team` on `Agent`, `TaskGraph*` | |
| Peer agent messaging | done before | `SendMessage` between team members | |
| Subagent output scanning | closed | `scanReport`: a warning beside the report | 1b |

## Worktrees

| Row | Status | What, or why not | Commit |
|---|---|---|---|
| Branch from a PR | closed | `from_pr` on `WorktreeOptions`, `pull_request` on `EnterWorktree`, a field in New session. Tries `refs/pull` then `refs/merge-requests`; a URL of another repository is refused | 1a, 3 |
| Copy gitignored files | closed | `.worktreeinclude` | 1a |
| Auto cleanup of worktrees | closed | `worktree.Prune`, at client start and daily | 1a |
| Non-git VCS | open | Needs `WorktreeCreate` and `WorktreeRemove` hook events, a way to read a hook's stdout as a path (`hooks.Outcome` has none), and a non-git form of `session.Worktree` that `ExitWorktree` and `Dirty` understand. Few people need it, so it was left for last | |

## Workflows and orchestration

| Row | Status | What, or why not | Commit |
|---|---|---|---|
| Save workflow as a command | closed | `/workflows save <run> <name> [--project]`; every saved script is a `/name`, which the model runs with `Workflow`, so the user still approves it | 2 |
| Automatic orchestration mode | closed | `/orchestrate`, per task; see Decisions | 2 |
| Deep review or research presets | closed | `/deep-research`, with `/code-review` already there | 2 |
| Loop on interval | done before | `/loop`, `ScheduleWakeup` | |
| Interview mode | closed | `/interview` | 2 |

## Plan mode

No `No` or `Partial` rows.

## Tasks and todo

| Row | Status | What, or why not | Commit |
|---|---|---|---|
| Task graph with dependencies | done before | `TaskGraph*` | |
| External task tracker | done before | `TaskTracker`, `TrackerPage.vue` | |

## Memory and instructions

| Row | Status | What, or why not | Commit |
|---|---|---|---|
| Auto memory across sessions | done before | `Memory` tool, `memory/autostore.go` | |
| Memory synced across machines | done before | One synced document per project | |
| `/init` generates instructions | done before | `command_init` | |
| Knowledge base | done before | `KnowledgeSearch`, `KnowledgeRead`, `KnowledgePage.vue` | |

## Permissions and modes

| Row | Status | What, or why not | Commit |
|---|---|---|---|
| Classifier decision log for admin | closed | `DecisionLogService`, Settings > Audit log > Auto mode decisions | 3 |
| Per-app rules for computer use | skipped | Marked in development, not a gap of this list | |
| Block reads outside working dirs | closed | `permissions.restrictReads`; see Decisions for its limit | 2 |
| Named permission profiles | closed | `permissionProfiles`, `--permission-profile` | 2 |
| Prompt suggestions to reduce asks | closed | `/fewer-prompts`, `/fewer-prompts apply` | 2 |
| Test a rule from CLI | closed | `harness-client permissions check <tool> --command ...`; exit 0 allow, 1 deny, 3 ask. It sees local files and machine policy, not server documents | 2 |

## Settings and managed policy

| Row | Status | What, or why not | Commit |
|---|---|---|---|
| Policy via MDM | closed | macOS managed preferences, Windows registry; never run against a real profile | 2 |
| Policy via system file | closed | `settings.json` and `settings.d/*.json` in the system folder | 2 |
| Admin UI for policy | done before | `SettingsManagedPolicy.vue`, `PolicyService` | |
| Config debug view | closed | `/debug-config`, `harness-client config debug`. Prints env names, never values | 2 |
| Feature flags list | closed | `/features`, `harness-client config features` | 2 |
| Bare or safe startup | closed | `--bare`, `--safe-mode`, plus `HARNESS_NO_SKILLS`, `HARNESS_NO_INSTRUCTIONS` | 2 |

## Images, PDF, notebooks

| Row | Status | What, or why not | Commit |
|---|---|---|---|
| Read PDF | done before | `pages` on `Read`, `tools/pdf.go` | |
| PDF viewer with comments | skipped | Viewer exists; comments overlap the document tools on another branch | |

## Cost and usage visibility

| Row | Status | What, or why not | Commit |
|---|---|---|---|
| Cost per session | closed | `/usage`, from `costUSD` on each assistant message, with the session's agents | 2 |
| Usage insights report | closed | `/insights [days]`, `harness-client insights [--days N] [--json]`: counting over local transcripts, no model | 2 |

## Security features

| Row | Status | What, or why not | Commit |
|---|---|---|---|
| Plugin validation | done before | `/plugin validate`, `plugins.Validate` | |
| Security white paper or trust center | skipped | Vendor's company surface, not a capability | |
| Bug bounty | skipped | Vendor's program | |
| Dedicated cyber-safety models | skipped | Vendor's model line | |

## Commits

| Mark | Commit | Subject |
|---|---|---|
| 1a | `6af26a17` | Worktrees |
| 1b | `bea5ca48` | Subagents |
| 2 | `6d1862bc` | Policy files, profiles, limits, tools, reports, commands |
| 3 | the commit after `6d1862bc` that adds `DecisionLogService` | Stdin, recording, cache TTL, decision log, pull request field |

## What only fakes have seen

- All model behavior: the new prompts (`command_*`, `agent_fork`, tool descriptions) have never met a real model. Run the eval suite before relying on them.
- `promptCacheTtl`: checked through the adapter's code path only, never against a provider.
- Pull requests: local bare remotes with `refs/pull/N/head` and `refs/merge-requests/N/head`, not GitHub or GitLab.
- MDM: the plist reader is only unit-tested on its JSON unwrapping, the registry reader on `reg query` text. Neither has seen a real profile.
- `BrowserRecord`: stub screenshots, no Chrome.
- `bashMemoryLimitMB`: the prefix is tested; it has never run on Linux.
- Windows: nothing here ran on it.
