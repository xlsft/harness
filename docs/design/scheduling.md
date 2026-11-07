# Scheduled tasks, goal mode and teams

How xlsft`s harness runs work without someone asking each time. Contracts: `proto/harness/agent/v1/schedules.proto`, `teamwork.proto`, the `DOCUMENT_KIND_SCHEDULE*` kinds of `sync.proto`. Prompt texts: `packages/core/prompts/README.md`.

## Schedules

A schedule is a prompt and a trigger: once, interval, cron, changed files or a webhook. Nothing runs on the server: the client that is up on the schedule's machine starts a session, or continues one, and follows it until it settles.

| Piece | Where |
|---|---|
| What is due, cron, file scans, lease | `packages/client/schedule` (all time goes through `Runner.Tick`) |
| Starting and settling runs, sync, RPCs, `/loop` | `packages/client/server/schedules.go` |
| Wake-up tools | `packages/client/tools/wakeup.go` |
| Webhook route | `packages/core/src/services/schedule-webhook.ts` |
| Screen | `packages/ui/app/components/ScheduledPage.vue`, words in `lib/schedule/types.ts` |

**What syncs.** The state file is `HARNESS_HOME/schedules/state.json`. A schedule is also a user-scope document unless it is device-local: tied to a session here, using an ACP agent or a local model, or made with no server connected. Device-local schedules and their history never leave the machine. Every other schedule names its machine (`device_id`, `device_name`); other machines list it, edit it and ask for a run, and only that machine runs it. The history is a document written by that machine alone.

**Consent on the machine that runs it.** A document can be edited from anywhere, so what runs is checked against a hash this machine allowed (`Task.TrustHash`: prompt, trigger, folder, model, mode, machine). An edit made elsewhere leaves the schedule waiting for "Allow on this machine", as hooks wait for their hash. Pausing and renaming are not in the hash.

**Asking another machine.** Run now on another machine and a webhook post both write a `DOCUMENT_KIND_SCHEDULE_TRIGGER` document, which waits in the feed for however long that machine is away. The machine marks it handled, runs it and deletes it, so a failed delete can't run it twice.

**A machine that is away.** A schedule can name an always-on host (`away_host_id`) and a repository. The host runs it while its machine is away, on a checkout of its own, and lands the work as a branch. The machine and the host each ask the server for a due time, so one slot runs once; a machine that can't reach the server runs its own schedule, and a host doesn't. `docs/design/hosts.md`, decision 19.

**Webhooks.** The URL is `<server>/api/schedules/webhook/<document id>.<secret>`. The server stores only the secret's hash, from the schedule document; the secret stays on the machine, which is the only one that can show the URL. A post is limited to 64 KB, to a burst of 30 and then one per two seconds per schedule, and to 100 waiting per user. What was posted reaches the run through the `schedule_trigger` reminder, marked as data, and never as the prompt. Event triggers refuse `bypassPermissions`.

**When the client was down.** A time more than two minutes late is missed: it runs once if `catch_up` is on, marked as a catch-up, or is recorded as missed. A time that comes while the previous run works is recorded as skipped. Runs a stopped client left behind read as interrupted. A lease in the state file keeps a second client on the machine (the app and a terminal) from running the same schedule.

**File triggers** poll, every two seconds by default, with no platform watchers. A change fires after two looks in a row saw the same thing and the cooldown has passed; what changes during a run belongs to the run and moves the baseline, so a run can't trigger itself.

**A run's session** closes when it settles if nobody is looking at it; the transcript stays in the list, and the history links to it.

## Wake-ups and `/loop`

`ScheduleWakeup` makes a device-local schedule on the session: one time at least 60 seconds ahead, or repeating, for at most 200 runs and a week, at most 10 per session. It comes back as the `scheduled_wakeup` reminder, the model's own note and not the user's words; the session reopens if it has to.

`/loop <interval> <prompt or /command>` makes the same kind of schedule, starting now, for up to a week. Each round is a message of the user, skipped while the session works. Without an interval the model paces itself with `ScheduleWakeup`. `/loop stop` ends both.

## Goal mode

`/goal [--max N] <condition>` sets a goal; `stop` and `resume` control it. After each turn a request to the small model reads the conversation and answers `{"met", "reason"}`, so the working model doesn't grade itself. Not met and under the cap: `goal_continue` starts the next turn. An interruption, a failed turn or a check that can't be read three times pauses it; the cap (default 20, setting `goalMaxTurns`) ends it. `GoalUpdated` carries the state to the banner above the composer. A session that closes and resumes comes back paused.

## Teams and the task graph

The graph (`tools/taskgraph.go`) is per session, next to the transcript, shared by the main agent and all its subagents: tasks with dependencies, an atomic claim, cycles refused. What an agent holds goes free when its run ends.

A team is agents started with the same `team` name in the `Agent` tool: ordinary subagents with three additions (`subagent/team.go`). Each is told who it works with. `SendMessage` already reaches any agent of the session, and now the main agent as `lead`. A member that ends a turn with free tasks is told about them instead of settling, and a settled member is woken when one comes free, until it stops taking any. Members are peers of one session on one machine; a team across sessions or machines is not a thing.
