# Always-on hosts

How xlsft`s harness works when no laptop is awake: `harness-client daemon`, a long-lived client on a server or in a container, signed in with a device token. Contracts: `proto/harness/relay/v1/hosts.proto`. Server: `packages/core/src/relay/hosts.ts`. Client: `packages/client/daemon`, `cmd/harness-client/daemon.go`. Image: `deploy/Dockerfile.host`, `deploy/compose.host.yml`.

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
10. **The agent never holds git credentials.** The daemon asks the server for a short-lived token when it clones, fetches or pushes, and gives it to that one git process, through the environment and an inline credential helper: never in argv, a URL or `.git/config`. So the agent commits; the daemon pushes when the session settles. An agent's own `git push` or `git fetch` fails for lack of credentials; that is the intent.
11. **Credentials come from the code host connection, with the user's rights.** The user's own OAuth token on the connection comes first. The connection's system identity (GitHub App installation token, GitLab access token) is used only where an admin said so, in the team host's `allowed_repositories`, or for an admin's or a session owner's reads as `bind` already allows. Without either the work fails with "connect your account". The server does not clone anything itself and no laptop's `gh` is involved.
12. **Work lands when the session settles.** At the end of each turn the daemon commits what is uncommitted (`git add -A`, which honours `.gitignore`), pushes the session's branch without force and, for a pull request, opens one as a draft if the branch has none. It never pushes to the base branch. It reports this in `HostWorkUpdated` on the session and on the work row. Auto-committing was chosen over leaving changes in the tree because a tree on a server nobody looks at is lost work.
13. **Capacity counts open sessions**, reported by the daemon (the only one that knows, after a crash), plus work claimed and not yet running. `max_sessions` for the host, `max_sessions_per_user` so that one person can't fill a team host. A session that settled and that nobody looks at closes after `--idle-close` (30 minutes), which frees its slot; its transcript stays, and a follow-up reopens it.
14. **Each session gets a fresh worktree of a per-repository cache, or a fresh clone** if asked. Worktrees are cheap for many sessions of one repository, and the cache is per worker, so one user's objects never serve another's. Workspaces are removed after `--keep-workspaces` (7 days) with the session closed.
15. **A fresh repository is an untrusted folder.** Its hooks, settings and MCP servers don't run until someone trusts the folder on that session, as on a laptop.
16. **Away work is handed over when a host reports, not by a server timer.** A pending auto-fix job whose session's machine has been offline longer than `HARNESS_HOST_AWAY_GRACE_S` (two minutes), or which has no machine, goes to the user's away host. No host up means nothing to do, so the check runs on `ReportAgentHost`. The relay command waiting for the laptop is withdrawn so that a job runs once. A user's personal host takes this by default; a team host only if they chose it (`SetAwayHost`), or they turn it off.
17. **An @mention on a PR with no session** becomes work for the commenter's away host when the commenter is known (`vcs_user_token.login` on that connection), write access having been checked by the webhook as before. Someone unknown gets nothing, as before.
18. **Handed-over work starts a new session on the PR's head branch** and the session link moves to it, so later jobs follow. When the old session's transcript is on the server it forks that transcript, so the agent keeps the conversation.
19. **Schedules name an away host** (`Schedule.away_host_id`, `repository_key`). The host takes a schedule whose own machine has been away past the grace. A server-side claim per schedule and due time (`ClaimScheduleRun`) keeps the machine's catch-up and the host from both running one slot. If the claim can't be reached, the machine runs its own schedule and the host doesn't. Consent: a daemon allows these schedules without asking, because anyone who can edit the document can start any session on it anyway; `bypassPermissions` stays refused.
20. **Policy.** Workers read the managed policy like any client, for their user. On top the server refuses work when policy requires the sandbox and the host reports no driver, and `personal_hosts_disabled` confines people to the hosts an admin registered.

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

- A single session's move from a laptop to a host. A fork of its transcript by way of decision 18 is the nearest thing.
- Network policy per session, and environment setup scripts.
- More than one replica of the server: the long poll wakes through in-process state, so another replica's waiter hears of new work at its next poll, within 25 seconds.
