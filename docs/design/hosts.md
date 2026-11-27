# Always-on hosts

How xlsft`s harness works when no laptop is awake: `harness-client daemon`, a long-lived client on a server or in a container, signed in with a device token. Contracts: `proto/harness/relay/v1/hosts.proto`. Server: `packages/core/src/relay/hosts.ts`. Client: `packages/client/daemon`, `cmd/harness-client/daemon.go`. Image: `deploy/Dockerfile.host`, `deploy/compose.host.yml`.

Sessions in one-off containers on runner nodes, a third scope of host, are in [cloud-sessions.md](cloud-sessions.md).

A host is a machine like any other: its sessions are ordinary sessions, mirrored through the relay, watched and steered from any viewer. What it adds is taking work with nobody at the keyboard. **Nothing runs on the server**, as before: the server queues, checks and mints credentials; agents only ever run on a host.

## Decisions

Numbered so later changes can refer to them. A decision that changes is edited in place, with what it replaced.

1. **A work queue on the server, not relay commands.** Work (`host_work`) is a row with a state: queued, claimed, running, then done, failed, cancelled or expired. The daemon pulls it with a long poll (`ClaimHostWork`). The alternative, a `Command` on the relay stream, delivers at least once but knows nothing of capacity, and it expires in hours, and a spawn is held for 30 s while a clone can take minutes. A claim is the one place where capacity is checked, atomically. So no `Command` field was needed.
2. **`StartHostWork` returns at once.** The viewer gets a work id, and follows `GetHostWork` until the row names a session; then it opens that session as any session of another machine. A full or offline host just means the row waits, up to 24 hours.
3. **Two RPC services.** `HostsService` is for people (list, register, start, follow): the client proxies it to frontends and the web console reaches it with its cookie. `HostAgentService` is for daemons and is never proxied: it hands out credentials.
4. **One relay host per user on a host.** The session-running part is the same for both kinds of host: a process signed in *as the user*, so that models, quotas, usage, the managed policy, sync and the relay owner are all that user's, and no code path needs an "on behalf of".
5. **A personal host (the default) is that process.** It is registered to one user and runs only their work. Isolation between users is not a question on it.
6. **A team host is a supervisor plus one worker per user.** The supervisor's token can do nothing but talk to `HostAgentService`; it has no models, no sessions, no relay. For each member with work it asks for a worker token (`IssueWorkerToken`) and starts a child under that member's own Unix user. The worker is decision 5 with a delegated device token of that member.
7. **A team host must prove it can separate users, or it takes no work.** It reports its isolation at registration. `uid` means Linux, supervisor running as root, per-user uids. Anything else, the server refuses work and worker tokens for it and says why; the daemon refuses `--shared` before registering. So what a team of one machine without that gets is a host per user. There is no "shared, unsafe" switch.
8. **Host tokens are narrower than the user.** A device registered as a host (`device.kind`) never carries the user's admin or other role: an admin's personal host acts as a plain user. A supervisor token is refused on every service but `HostAgentService`. Worker tokens are rotated each time a worker starts, and go over a pipe, not the environment. Removing someone from the team's group revokes their worker's device.
9. **A device token registers as a host only if it was made for it**: by `harness-client daemon login`, or `CreateAgentHost` in the app. Otherwise running the daemon with a laptop's stored sign-in would quietly drop that laptop's role.
10. **The agent never holds git credentials.** The daemon asks the server for a short-lived token when it clones, fetches or pushes, and gives it to that one git process, through the environment and an inline credential helper: never in argv, a URL or `.git/config`. So the agent commits; the daemon fetches at the start of each turn, so that `origin/*` shows what the code host has, and pushes when the session settles. An agent's own `git push` or `git fetch` fails for lack of credentials; that is the intent. A GitHub App's token for this is cut down to one repository and to its contents. A GitLab connection's access token reaches whatever it was made for, so make it narrow.
11. **Credentials come from the code host connection, with the user's rights.** The user's own OAuth token on the connection comes first. The connection's system identity (GitHub App installation token, GitLab access token) is used only where an admin said so, in the host's `allowed_repositories`. Without either the work fails with "connect your account". The server does not clone anything itself and no laptop's `gh` is involved.
12. **Work lands when the session settles.** At the end of each turn the daemon commits what is uncommitted (`git add -A`, which honours `.gitignore`), pushes the session's branch without force and, for a pull request, opens one as a draft if the branch has none. It never pushes to the base branch. It reports this in `HostWorkUpdated` on the session and on the work row. Auto-committing was chosen over leaving changes in the tree because a tree on a server nobody looks at is lost work. These git commands take nothing from the repository that could run code or catch the token: hooks, helpers, proxies and fsmonitor are overridden on the command line, and a repository whose configuration rewrites remote URLs is refused.
13. **Capacity counts working sessions**, not open ones: sessions at work or waiting for someone's answer, as the daemon reports them (the only one that knows, after a crash), plus work claimed and not yet running. `max_sessions` for the host, `max_sessions_per_user` so that one person can't fill a team host. An idle session stays open so that a viewer can follow it up through the relay, and a closed one could not be. It closes after `--idle-close` (24 hours); its transcript stays, and a job reopens it. A restarted daemon reopens what it had open. (First version: open sessions and a 30 minute close, which cut people off from their own sessions.)
14. **Each session gets a fresh worktree of a per-repository cache, or a fresh clone** if asked. Worktrees are cheap for many sessions of one repository, and the cache is per worker, so one user's objects never serve another's. Workspaces are removed after `--keep-workspaces` (7 days) with the session closed.
15. **A fresh repository is an untrusted folder.** Its hooks, settings and MCP servers don't run until someone trusts the folder on that session, as on a laptop.
16. **Away work is handed over when a host reports, not by a server timer.** A pending auto-fix job whose session's machine has been offline longer than `HARNESS_HOST_AWAY_GRACE_S` (two minutes), or which has no machine, goes to the user's away host. No host up means nothing to do, so the check runs on `ReportAgentHost`. The relay command waiting for the laptop is withdrawn so that a job runs once. A user's personal host takes this by default; a team host only if they chose it (`SetAwayHost`), or they turn it off.
17. **An @mention on a PR with no session** becomes work for the commenter's away host when the commenter is known (`vcs_user_token.login` on that connection), write access having been checked by the webhook as before. Someone unknown gets nothing, as before.
18. **Handed-over work starts a new session on the PR's head branch** and the session link moves to it, so later jobs follow. When the old session's transcript is on the server it forks that transcript, so the agent keeps the conversation.
19. **Schedules name an away host** (`Schedule.away_host_id`, `repository_key`). The host takes a schedule whose own machine has been away past the grace. A server-side claim per schedule and due time (`ClaimScheduleRun`) keeps the machine's catch-up and the host from both running one slot. If the claim can't be reached, the machine runs its own schedule and the host doesn't. Consent: a daemon allows these schedules without asking, because anyone who can edit the document can start any session on it anyway; `bypassPermissions` stays refused.
20. **Policy.** Workers read the managed policy like any client, for their user. On top the server refuses work when policy requires the sandbox and the host reports no driver, and `personal_hosts_disabled` confines people to the hosts an admin registered.

## Moving a session to a host

A session open on the laptop goes on, on one of the user's hosts, from the same conversation and the same work. It is the
hand-over of decision 18, started by the user: the work goes up as a branch, the host takes it from the queue like any other
work, and forks the transcript the server has.

21. **What moves is a branch and the server's transcript.** The laptop writes the session's working tree as one commit on top
    of `HEAD`, through a temporary index (`git add -A` into it, so `.gitignore` holds), and pushes that commit to
    `harness/<session id>`. The user's checkout, its index and its branch are not touched: what they had stays as it was.
    Unpushed commits of the branch go up with it, being the parents. The push never forces, never names the base branch, and
    runs with the hardened git of decision 12 (`packages/client/hostgit`). Then the uploader is flushed, so that the server has
    the whole transcript, and the work is queued.
22. **The laptop pushes with the credentials the host would get.** `IssueMoveCredentials` mints them for one move: the work
    must be the caller's, of kind `MOVE` and still `AWAITING_PUSH`, and they follow decision 11 for the target host (the
    user's own account first, the connection's identity only where that host's `allowed_repositories` say so). It is a method
    of `HostAgentService`, the service that is never proxied to a frontend, so no screen can ask for a token. Any device of
    the user may call these two methods; the rest of the service stays for hosts. The laptop's own `git` sign-in is not used:
    it may reach another remote, or none, and the host is going to fetch from the connection anyway.
23. **A move is work with one more state.** `StartHostWork` with `kind: MOVE`, the branch and `fork_session_id` makes a row
    in `AWAITING_PUSH`, which no daemon claims. `QueueMovedWork` puts it in `QUEUED` once the push is done; a push that fails
    cancels it. A row still waiting after 30 minutes expires. The daemon then does what it does for an auto-fix job: checks out
    the existing branch, forks the transcript, and does not send a prompt unless the user wrote one for the host. Landing is a
    branch by default (each turn's end pushes `harness/<session id>` again); the user can ask for a draft pull request.
24. **The laptop's copy becomes read-only.** A `movedTo` record goes into its transcript (host, work, branch), the session
    closes, and when it opens again it is read-only: messages, shell commands, compaction and rewinds are refused, and
    `SessionInfo.moved` and the `SessionMoved` event tell the screens, which show where it went and a link. The link follows
    the work row to the host's session, as for any host work (decision 2). The record names the session it belongs to, so
    the fork on the host, which copies the transcript, is not read-only too.
25. **Refused, with the reason, before anything is written:** a folder that is not in git; a repository without an `origin` on
    a code host, or with a configuration that rewrites remote URLs; a session in the middle of a turn, unless the user said to
    stop it; files that would go up and that a deny rule on `Read` covers, or that are well-known secret files (`.env`, keys,
    `.netrc`, `.npmrc` and the like): those are listed, and nothing moves until they are gone or ignored; a submodule with
    changes or commits of its own, which the host could not fetch (decision on submodules below); changes of the session
    outside the repository it is in (another working directory, a file outside the checkout), which a branch can't carry; a
    session with no transcript on the server (sync off); a folderless chat; a session of another machine, or one that moved
    already. `CheckSessionMove` gives the same answer without doing anything, for the confirmation.
26. **The user confirms first.** From the session menu, or `/move`, which only opens the confirmation (`ACTION_OPEN_MOVE`).
    The confirmation shows the host, the branch, how many files and commits go up, the mode the host session gets, and the
    reasons it can't, if any.
27. **The host's mode is never more than the session's.** The session's permission mode goes with it, `bypassPermissions`
    excepted: a bypass session moves in `auto` if the policy allows auto, and in `default` otherwise, and the confirmation says
    so. The server refuses bypass for any work as before, the host reads the managed policy for the user, and the fresh
    checkout is an untrusted folder (decision 15), so the project's hooks and MCP servers don't run there until the user
    trusts it on that session.

## Best-of-N

One prompt, N attempts (2 to 5), each in a checkout and on a branch of its own, side by side; the user keeps one.

28. **A group on the server, attempts as host work or local sessions.** `StartAttempts` makes an `attempt_group` with N
    `attempt` rows. On a host, each attempt is host work of kind `ATTEMPT` (`harness/<work id>`, landing `BRANCH`), queued,
    claimed and landed like any work; the group is how they are found again. Locally, the client makes N sessions, each in a
    worktree of its own (`harness/attempt-<group>-<n>`), and reports each one's session and branch to the group. So there is one
    record for both, from any device, and no queue apart from the host queue.
29. **Limits.** At most 5 attempts. The managed policy can lower that (`max_attempts`), cap how many run at once
    (`max_parallel_attempts`) and what one attempt may spend (`attempt_budget_usd`). The user's own settings
    (`bestOfNParallel`, `bestOfNBudgetUsd`) choose within those. The server applies the caps when the group is made, and the
    group carries what applies. An attempt that reaches the budget stops as a headless run does (`--max-budget-usd`). How
    many run at once counts attempts that are working: on a host the claim leaves an attempt queued while that many of its
    group are claimed or working (an attempt that finished its turn reports so and frees its place); locally the client
    starts the next one when one finishes. A host's own capacity (decision 13) applies on top.
30. **Test results.** The group may name a test command. When an attempt's turn ends, its session runs it in its own
    checkout, in the OS sandbox where there is a driver, refused if a deny rule covers it, and reports the exit code and the
    end of the output to the group (`ReportAttempt`), and to the session's viewers (`AttemptChecked`). The same code does it on
    a laptop and on a host, because a host runs the same agent server.
31. **Diffs are the Changes pane's.** The compare view shows each attempt's Changes pane, which reads `GetSessionDiff` as for
    any session. For a session on a host that call goes through the relay, as a query like `ListCheckpoints`
    (`get_session_diff`, `get_session_file_diff`), so the Changes pane of any session of another machine shows what is on its
    disk now, not only what the transcript's edits did.
32. **Keeping one deletes the others only when the user says so.** `KeepAttempt` marks the one kept. `DiscardAttempts`, after
    a confirmation that names what goes, removes the others: a local attempt's session closes, its worktree and its branch
    are removed by the client; a host attempt still queued is cancelled, and one with a session is handed to its daemon through
    `ReportAgentHost` (`discard_work_ids`), which closes the session, removes the checkout and deletes its `harness/` branch on
    the code host, then reports it done. A daemon deletes only a branch that it made for that work and that starts with
    `harness/`. The kept attempt goes on as an ordinary session. So that a daemon need not wait for its next report, its
    claim answers at once, with no work, while something of it is to be discarded; the daemon then reports straight away.
    Frontends start and discard through the client's `LocalAttemptsService`, which passes a host group to the server; a
    local attempt's branch is `harness/attempt-<group>-<n>`, the only kind of local branch that discarding deletes.
33. **What a group shows is its owner's.** Groups and attempts are the caller's alone: other people, admins included, get
    NOT_FOUND, as for sessions.

## Isolation between users

Only team hosts have more than one user. What separates them, on a host that reports `uid`:

| Thing | Separated? |
|---|---|
| Files: workspaces, transcripts, repository caches, worker `HARNESS_HOME` | Yes: each user has a uid and a 0700 home |
| Other users' processes (signals, ptrace, `/proc/<pid>/environ`, memory) | Yes: different uid |
| A worker's own device token | From other users, yes. From that user's own agent: not in its environment, not on disk, and the worker is not dumpable on Linux; the OS sandbox is what stops the rest. |
| Server access | Yes: the worker's token is that user's alone; the relay and every RPC treat other users' things as not found |
| Temporary files | `TMPDIR` is private, but `/tmp` itself is shared unless the container or the sandbox driver mounts its own; compose gives each container its own tmpfs, not each user |
| **Loopback and network** | **No.** All users share the host's network namespace: a dev server one agent binds on `localhost` can be reached by another user's agent. Bind tests to a random port and don't serve secrets on them. |
| **Process list** | **Not by default.** `ps` shows others' command lines unless `/proc` is mounted with `hidepid`. |
| **CPU, memory, disk** | **No quotas per user** beyond the session limits. One agent can slow the rest down or fill the disk. |
| **The kernel** | **Shared.** A kernel bug is a way across; a uid is not a VM. |

Plainly: uid separation keeps honest mistakes and a prompt-injected agent away from other users' files and tokens. It is not a boundary against someone determined with a local kernel exploit. For users who must not trust each other, give each a personal host, in its own container or VM. That is the default, and the only option where a host can't show uid separation.

Sessions of the *same* user on one host are not separated from each other, same as on a laptop: same uid, one worker. Decisions 5 and 6 were chosen over a uid per session because a session reopens and is followed up, and its schedules, transcripts and auto-memory belong to the user, not to one session.

The agent's commands run under the OS sandbox where a driver exists (macOS today, a Linux driver is on its way) and without one elsewhere. `sandbox_driver` shows which in the admin view, and a policy requiring the sandbox refuses a host without one. On a personal host without a driver an agent command runs as the daemon's user and could read a token file there, and call the server as that user. That is the same as a laptop.

## Flows

**New session.** Environment menu lists `ListAgentHosts`. Pick a host, name the repository (`host/owner/name`) and the prompt. `StartHostWork` queues it. The daemon claims it, gets credentials, makes the checkout on a new branch `harness/<work id>`, creates the session, sends the prompt, says `RUNNING` with the session id. The viewer opens it through the relay. It keeps running when the laptop sleeps.

**Team host.** Same, except the supervisor sees the member in `ListWantedWorkers`, gets a token, starts the worker; the worker claims.

**Auto-fix and mention.** See decisions 16 to 18. Delivery to a session already on the host is unchanged: relay command `deliver_auto_fix_job`, which reopens a closed session. The daemon fetches and lands again when the turn ends.

**Failure.** A claim holds a five-minute lease, renewed by updates. A daemon that dies while preparing loses it and the work goes back to the queue, three times at most. A daemon that dies with a session open leaves the session waiting for connection, as for any machine.

## What is not there

- A move back from a host to the laptop. "Continue here" forks the host's session into a folder of the laptop, and
  `git fetch` there brings its branch.
- A move of an ACP agent's session, or of one with subagents still running: the move waits for, or stops, the turn, and
  agents that were still running are not carried.
- Local attempts of a group that had not started when the client quit stay waiting; the group shows them so.
- An attempt's budget applies to the run of the client or daemon that started it: a session reopened later runs without it.
- The web console reaches the server directly, so it has neither moves (a move is the laptop's) nor local groups.
- Best-of-N across several hosts, or hosts and the laptop at once: a group runs on one host, or locally.
- Network policy per session and environment setup scripts.
- Per-user quotas for CPU, memory and disk on a team host, and a network namespace per user. Decided against for now: they need a cgroup and namespace setup that a container doesn't get without more privileges than the rest of this avoids.
- Submodules: checkouts don't fetch them, because fetching what a repository names runs before anyone trusted it.
- A mention of a session that was already handed over reaches the host session twice when the old session's metadata still matches the branch. It reads as the same comment twice.
- More than one replica of the server: the long poll wakes through in-process state, so another replica's waiter hears of new work at its next poll, within 25 seconds.
