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
- **The local client** (`packages/client/server/inboxfold.go`, `inbox.go`) folds the events of its own live sessions with the same rules (the Go and TypeScript folds are tested on the same scenarios: `inboxfold_test.go`, `packages/core/test/inbox.test.ts`; packages stay self-contained, so no shared fixture file). This covers sessions that are not mirrored (no server, remote control off by policy or switch) and is fresher for the ones that are.
- `AgentService.ListInbox` merges the two: local items win for local sessions; server items cover the user's other machines and always-on hosts. Without a server it is local only.

**Decision: fold events, do not query hosts.** Asking every host for its pending prompts at list time would make the inbox as slow as the slowest machine and empty for a machine that is asleep. Folding the events the relay already stores costs one row update per batch and lists in one query.

**Decision: attention is server time, except for history.** `attention_at` (the column) is the server's clock when the fold sees something new, since read markers are server time and machines' clocks drift. An event more than ten minutes old is history a resumed session replays to the relay: it keeps its own time, so reopening an old session doesn't make it unread again.

**Decision: offset paging.** The order (unread first, then attention) changes as items are read, so a keyset cursor would not be stable either. Pages are offsets; a frontend follows `WatchInbox` and lists again on `resync`. The client puts its own live sessions on the first page of their group and drops the server's copies of them from every page; counts are the server's plus the local sessions the server doesn't mirror.

**Decision: answers use the existing RPCs.** The inbox has no answer RPC of its own: `RespondToPermission`, `AnswerQuestion`, `RespondToPlan`, `Interrupt` with the item's session id. For a session of another machine the client's router sends them through the relay as before; a session the client's listing doesn't know yet is known from the inbox item (`inboxRemoteInfo`).

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
- **Budget**: `budget_usd` and `budget_tokens` (0: none; default the `projectBudgetUsd` preference). Usage is the coordinator's own plus every worker's, from their inbox items (the local fold for local sessions, the server's for remote ones). At the limit no worker starts and the run is `paused` with a notice; workers that run finish their turn (interrupting them mid-edit would leave half-done work), and the user raises the limit or stops. **Decision**: a budget is checked between turns, not inside a model request, so a run can go over it by the last turns that were already running.
- **Managed policy**: a policy document can set `disableProjectRuns`, and `projectMaxWorkers` / `projectBudgetUsd` as ceilings: the run takes the lower of the policy and the user's value. Worker sessions load settings like any session, so every other rule of the policy applies to them.
- **Permissions**: a worker gets the coordinator's permission mode and rules, never more. The coordinator's mode is the one the user chose. `bypassPermissions` is never passed to a worker on another machine (the relay refuses it anyway); it becomes `default` there. Disabled modes of the policy fall back to `default`. The coordinator cannot set a worker's mode.

### Restart

The record is written on every change. At start the client reads the records: a run that was `running` or `waiting_for_user` becomes `paused` with the reason "the client restarted", and local workers that were running are marked `interrupted`. `ResumeProjectRun` reopens the coordinator session and gives it a note with the state; remote workers kept running and report as usual. The task graph frees what was in progress on its own (`NewTaskGraph`).

**Decision: no automatic resume.** A restart can follow a crash in the middle of something the user wants to look at first, and resuming spends money. The inbox lists paused runs under Needs you.

### Seen from other devices

`SessionEvent.project_run_updated` (310) carries the run on the coordinator session, so a viewer through the relay sees it like a local one. `Command.project_run_control` (relay 310) pauses, resumes, stops and changes limits from another device; like every relay command it cannot widen permissions.

### `/delegate`: how people start one

A run is started from the conversation, not from a form. The user agrees on the work with the agent, then types
`/delegate [--workers N] [--budget USD] [what to add]`, and **this session** becomes the coordinator (`beginRun` in
`server/projectruns.go`, `server/delegate.go`). The conversation is the brief: the `delegate_started` reminder tells the
agent to split what was agreed into tasks and to give each helper a self-contained prompt, since helpers don't see the
conversation. The user goes on with something else; the helpers work in the background and report to this session.
`/delegate status|pause|resume|stop` steer it; a helper session can't delegate further.

**Decision: a command, not a page.** A separate "project run" form made people restate a goal the agent already
understood, and its words (coordinator, worker, target) described the machinery. In the UI it is "delegating" to
"helpers".

### UI

- `/inbox`: groups, virtualised list, answer permission (allow, deny), answer a question (option buttons), approve a plan, open, stop, mark read. Live.
- Above the composer of the delegating session (`DelegateBanner.vue`): what stage it is at, tasks done, helpers working,
  the spending against the budget, Pause/Resume and Stop (they send `/delegate …`), and the tasks with each helper's
  state and a way to open its session. `CreateProjectRun` stays for other clients; the Projects page is gone.

All logic is in the client and the server; the screens show and send.

### Workers on other machines

A remote worker is spawned with relay `SpawnSession` on a daemon host (the spawn now honours `worktree`: a new worktree of the repository at `cwd`, which stays under the host's allowed roots). It is not a linked child: the coordinator's machine follows it through the server's inbox, every 30 seconds while some remote worker works (and on every `ProjectStatus`), closes its graph task when it settles and hands the coordinator its report as a `SessionNote`, the same channel local workers use.

## What is not done

- Controlling a run from another device reaches it only while its coordinator session is open on its machine (relay commands are routed to open sessions). After a restart the run is paused there until resumed on that machine.
- Other machines of the user that are not daemon hosts don't accept workers; they show as unavailable targets with the reason.
- The Settings screen has no fields for `projectMaxWorkers` and `projectBudgetUsd` yet; `UpdateUserPreferences` and the settings files take them.

- A team across sessions in the subagent sense (shared `SendMessage` namespace) is not a thing; workers talk to the coordinator through notes.
- Merging remote workers' branches: the coordinator gets their branch names and pushes are up to them; `ProjectWorkerMerge` works for local worktrees only.
