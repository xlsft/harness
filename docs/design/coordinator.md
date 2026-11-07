# Inbox and project runs

Two features that sit on top of what already exists: the **inbox**, one place that shows every session and background agent of the user across machines, and **project runs**, a coordinator session that splits a goal into tasks and hands them to worker sessions. Contracts: `proto/harness/agent/v1/inbox.proto`, `proto/harness/agent/v1/project_runs.proto`, `proto/harness/relay/v1/inbox.proto`.

## What is reused

| Existing piece | Used for |
|---|---|
| Relay (`relay.proto`, `packages/core/src/relay/service.ts`) | Every open session on a signed-in machine is mirrored by default (`remoteControl`, default on). Its durable events reach the server, so the server can tell which sessions wait for the user on every machine. Answers go back through the existing `RespondToPermission`, `AnswerQuestion`, `RespondToPlan`, `Interrupt` commands. |
| `SessionInfo.unread`, `SessionStatus` | The sidebar's unread flag and the status stay what they are. The inbox has its own read marker (below), and marking an item read also clears `unread` on the session's server copy. |
| Session links (`server/sessionlinks.go`, `SessionStart`) | Workers are started sessions: linked to the coordinator, the coordinator's permission mode and rules, turn reports as `SessionNote`s. No second channel between sessions. |
| Task graph (`tools/taskgraph.go`) | The coordinator's plan is its session's task graph. A worker's task has the worker's session name as owner. |
| `SpawnSession` (relay) | Starting a worker on an always-on host (a daemon host with allowed roots). The spawn now takes `worktree`. |
| Subagents (`SubagentUpdated`) | Background agents show in the inbox as rows under their session. |

Nothing new reads `.claude/` or `CLAUDE.md`.

## Inbox

### Groups

Each item is a session (or a subagent under a session) in one group, in this order:

1. **Needs you**: a permission prompt, a question or a plan waits (`SessionStatus.NEEDS_INPUT`, or the pending list is not empty).
2. **Failed**: the last turn ended on an error, or a subagent failed.
3. **Finished**: the last turn ended and left a report (the final assistant text, trimmed). Unread first.
4. **Running**: a turn or a subagent runs.

Idle sessions that the user has read and that have no report do not show; the sidebar has them. Closed sessions (no longer open anywhere) stay in Finished or Failed until read, and for at most 14 days.

### Where the state comes from

- **The server** (`packages/core/src/relay/inbox.ts`) folds the relay events of each mirrored session into one `relay_inbox` row: group, pending requests (permission: tool, a one-line summary of the input, suggested rules; question: the questions and options; plan: the first lines), subagents (id, name, state, report), last report, usage of the last turn, title, folder, device, parent. It is updated in `PublishEvents`, `RegisterSession`, `UnregisterSession` and `Heartbeat`. Host presence is read at list time, so an item of a machine that went away says so.
- **The local client** (`packages/client/server/inbox.go`) folds the events of its own live sessions with the same rules (Go and TypeScript reducers share fixtures in their tests). This covers sessions that are not mirrored (no server, remote control off by policy or switch) and is fresher for the ones that are.
- `AgentService.ListInbox` merges the two: local items win for local sessions; server items cover the user's other machines and always-on hosts. Without a server it is local only.

**Decision: fold events, do not query hosts.** Asking every host for its pending prompts at list time would make the inbox as slow as the slowest machine and empty for a machine that is asleep. Folding the events the relay already stores costs one row update per batch and lists in one query.

**Decision: the reducer keeps only what the inbox shows.** Tool input is cut to a summary (400 characters), a report to 600, questions to their text and option labels. The full request stays in the event log, which the session view reads.

### Read state

`relay_inbox.read_at` per user and session. An item is unread when something happened that needs attention (`attention_at`: a request, a finished or failed turn) after `read_at`. `MarkInboxRead` sets it from any device; opening the session from the inbox marks it read. Live updates carry the new state to the user's other devices. Read state of local-only sessions (no server) is kept in memory and is lost with the client: there is no other device to sync with.

### Paging and live updates

- `InboxService.ListInbox(group, page_size ≤ 200, page_token)` pages one group, ordered by unread, then `attention_at` descending, then id; counts of every group come with each page. The UI loads the first page of each group and the next page when the list scrolls near the end (virtua `VList`).
- `InboxService.WatchInbox` streams `InboxChange` (upsert or remove of one item) for the caller; it starts with a `caught_up` marker so a frontend lists first and then applies changes. The client's `AgentService.WatchInbox` merges the server stream with local changes; a frontend that falls behind is told to list again (`resync`).
- Per-user fan-out goes through the relay bus (`publishInbox` / `subscribeInbox`), in-process like the rest of the relay bus.

### Privacy

Rows are keyed by user and only ever read for the caller (`p.user.id`). No admin RPC reads them. Usage on items is the user's own.

### CLI

`harness-client agents [--group needs|failed|finished|running] [--json] [--watch]` lists the inbox from the signed-in server (no running client needed). `harness-client agents read <session>` marks an item read. Answers are given in the app or the session; the CLI lists and points to the session id.

## Project runs

### Shape

A project run is a **coordinator session** with a run record next to it: `HARNESS_HOME/project-runs/<run id>.json` (0600), on the coordinator's machine. The record holds the goal, the limits, the workers and the state. The coordinator session's task graph is the plan.

States: `planning` → `running` ⇄ `waiting_for_user` → `merging` → `done` | `failed` | `stopped`. `paused` when the budget runs out, the user pauses it or the client restarted while workers were local.

### The coordinator

`CreateProjectRun(goal, cwd, limits, targets)` opens a session with the `project_coordinator` instruction (prompt registry key, with a fallback text in the client) and the goal as its first message. The coordinator has four tools on top of the usual ones:

| Tool | What |
|---|---|
| `ProjectWorkerStart` | Start a worker for a task of the graph: `task_id`, `prompt`, `target` (`worktree` default, `here`, or a host id from `ProjectStatus`). Refused over the limits. |
| `ProjectStatus` | The run: workers with state and usage, budget used and left, available targets. |
| `ProjectWorkerMerge` | Merge a finished local worktree worker's branch into the coordinator's branch (`git merge --no-ff`); a conflict is aborted and reported. Asks permission like a command. |
| `ProjectFinish` | Ends the run with a summary (`done`) or a reason (`failed`). |

Decision points use `AskUserQuestion`: the run is `waiting_for_user` while a question or permission of the coordinator waits, and the inbox shows it under Needs you.

### Workers

A worker is a session started with the existing `StartSession` path (local, `here` or in a worktree) or with relay `SpawnSession` on a host (always-on hosts and other machines that accept new sessions). It is linked to the coordinator, so its turn reports come back as notes and wake the coordinator. The graph task it works on is set `in_progress` with the worker's name as owner; its report closes the task (`done`, or `failed` for a failed turn), and the coordinator decides what comes next.

**Decision: workers are sessions, not subagents.** Sessions survive the coordinator's turn, can run on other machines, have their own transcript, inbox row and permission prompts, and the user can open and steer one. Subagents are the right tool inside one session; project runs are about many.

### Limits

- **Parallel workers**: `max_workers` of the run (default the `projectMaxWorkers` preference, 4; 1 to 32). A start over it is refused with the count.
- **Budget**: `budget_usd` and `budget_tokens` (0: none; default the `projectBudgetUsd` preference). Usage is the coordinator's own plus every worker's, from their `TurnFinished` events (local) or inbox rows (remote). At the limit no worker starts, running workers are told to stop after their turn, and the run is `paused` with a notice; the user raises the limit or stops.
- **Managed policy**: a policy document can set `disableProjectRuns`, and `projectMaxWorkers` / `projectBudgetUsd` as ceilings: the run takes the lower of the policy and the user's value. Worker sessions load settings like any session, so every other rule of the policy applies to them.
- **Permissions**: a worker gets the coordinator's permission mode and rules, never more. The coordinator's mode is the one the user chose. `bypassPermissions` is never passed to a worker on another machine (the relay refuses it anyway); it becomes `default` there. Disabled modes of the policy fall back to `default`. The coordinator cannot set a worker's mode.

### Restart

The record is written on every change. At start the client reads the records: a run that was `running` or `waiting_for_user` becomes `paused` with the reason "the client restarted", and local workers that were running are marked `interrupted`. `ResumeProjectRun` reopens the coordinator session and gives it a note with the state; remote workers kept running and report as usual. The task graph frees what was in progress on its own (`NewTaskGraph`).

**Decision: no automatic resume.** A restart can follow a crash in the middle of something the user wants to look at first, and resuming spends money. The inbox lists paused runs under Needs you.

### Seen from other devices

`SessionEvent.project_run_updated` (310) carries the run on the coordinator session, so a viewer through the relay sees it like a local one. `Command.project_run_control` (relay 310) pauses, resumes, stops and changes limits from another device; like every relay command it cannot widen permissions.

### UI

- `/inbox`: groups, virtualised list, answer permission (allow, deny), answer a question (option buttons), approve a plan, open, stop, mark read. Live.
- `/projects`: runs with state, workers, budget bar and the graph, a form for a new run, pause, resume, stop, limits.

All logic is in the client and the server; the screens show and send.

## What is not done

- A team across sessions in the subagent sense (shared `SendMessage` namespace) is not a thing; workers talk to the coordinator through notes.
- Merging remote workers' branches: the coordinator gets their branch names and pushes are up to them; `ProjectWorkerMerge` works for local worktrees only.
