# Agent core gaps

Working list for closing the agent-core rows of [the comparison](../comparison.md), which is out of date. Every `No`, `Partial` and `Planned` row of the tables named below was checked against the code, not against the document. A row's commit is filled in when the work lands, so someone else can pick up from here.

Status values: **done before** (the code on `main` already has it, the comparison is stale), **closed** (done here), **open** (worth doing, not done yet), **skipped** (with the reason).

## Decisions

- Agent definitions, skills and commands stay in `.harness/`, per the rule that nothing is read from `.claude/` or `CLAUDE.md` at runtime. `.worktreeinclude` is a repository file of its own, in no vendor's folder, and is read from the repository root.
- Rows that only name another vendor's plan, program or model are not capabilities and are skipped.
- Out of scope because they live on other branches: document tools, the Extensions screen, models, SSH, real Chrome, i18n.

## Agent loop and context

| Row | Status | What, or why not | Commit |
|---|---|---|---|
| Cache TTL controls | open | | |
| Goal mode until condition | done before | `/goal`, `server/goal.go` | |
| Recap on return | open | `WrapUp` closes a turn; nothing summarizes on demand | |
| Session transcript export | done before | `LibraryService.ExportSession` | |
| Transcript search | done before | `TranscriptSearchService`, merged with local files | |

## Built-in tools: files and search

| Row | Status | What, or why not | Commit |
|---|---|---|---|
| Multi-chunk edit | open | | |
| List directory | open | | |
| LSP servers built in | skipped | Same model as the others: specs for four languages, binary on PATH, `lspServers` for more. Installing servers is a packaging choice, not a gap | |

## Shell and background tasks

| Row | Status | What, or why not | Commit |
|---|---|---|---|
| PowerShell tool | open | | |
| Env filter for commands | open | | |
| Interactive stdin to a command | open | | |
| Managed sidecar processes | skipped | `Preview*` with `.harness/launch.json` is this capability | |
| Memory cap per tool | open | | |

## Web, search, browser

| Row | Status | What, or why not | Commit |
|---|---|---|---|
| Domain filters on search | done before | `allowed_domains`, `blocked_domains` | |
| Cached, live, disabled modes | open | | |
| Drive the user's real signed-in browser, Browser extension | skipped | Real Chrome is on another branch | |
| Recording of browser session | open | | |
| Comments on web pages | skipped | A Codex app surface; `BrowserPane` take-over and the composer cover pointing the agent at a page | |
| Public tunnel to preview | skipped | Needs a hosted relay we do not run; a self-hoster's own tunnel works with `Preview*` | |
| Code search index or panel | skipped | `Grep`, `LSP` and the Files pane cover it; an index is a project of its own with little gain over ripgrep | |
| Deep research workflow | open | | |

## Subagents and teams

| Row | Status | What, or why not | Commit |
|---|---|---|---|
| Custom agent fields | open | | |
| Agents per run with own permissions | open | | |
| Agents with own MCP servers | open | | |
| Transient agent defined on the fly | open | | |
| Forked subagent with full history | open | | |
| Agent teams with shared task list | done before | `team` on `Agent`, `TaskGraph*` | |
| Peer agent messaging | done before | `SendMessage` between team members | |
| Subagent output scanning | open | | |

## Worktrees

| Row | Status | What, or why not | Commit |
|---|---|---|---|
| Branch from a PR | open | | |
| Copy gitignored files | open | | |
| Auto cleanup of worktrees | open | | |
| Non-git VCS | open | | |

## Workflows and orchestration

| Row | Status | What, or why not | Commit |
|---|---|---|---|
| Save workflow as a command | open | Saved scripts run by name through `Workflow`, not from the slash menu | |
| Automatic orchestration mode | open | | |
| Deep review or research presets | open | `/code-review` exists; no depth and no research preset | |
| Loop on interval | done before | `/loop`, `ScheduleWakeup` | |
| Interview mode | open | | |

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
| Classifier decision log for admin | open | JSON route only | |
| Per-app rules for computer use | skipped | Marked in development, not a gap of this list | |
| Block reads outside working dirs | open | | |
| Named permission profiles | open | | |
| Prompt suggestions to reduce asks | open | | |
| Test a rule from CLI | open | | |

## Settings and managed policy

| Row | Status | What, or why not | Commit |
|---|---|---|---|
| Policy via MDM | open | | |
| Policy via system file | open | Managed `AGENTS.md` only | |
| Admin UI for policy | done before | `SettingsManagedPolicy.vue`, `PolicyService` | |
| Config debug view | open | | |
| Feature flags list | open | | |
| Bare or safe startup | open | | |

## Images, PDF, notebooks

| Row | Status | What, or why not | Commit |
|---|---|---|---|
| Read PDF | done before | `pages` on `Read`, `tools/pdf.go` | |
| PDF viewer with comments | skipped | Viewer exists; comments overlap the document tools on another branch | |

## Cost and usage visibility

| Row | Status | What, or why not | Commit |
|---|---|---|---|
| Cost per session | open | Per-turn only | |
| Usage insights report | open | | |

## Security features

| Row | Status | What, or why not | Commit |
|---|---|---|---|
| Plugin validation | done before | `/plugin validate`, `plugins.Validate` | |
| Security white paper or trust center | skipped | Vendor's company surface, not a capability | |
| Bug bounty | skipped | Vendor's program | |
| Dedicated cyber-safety models | skipped | Vendor's model line | |
