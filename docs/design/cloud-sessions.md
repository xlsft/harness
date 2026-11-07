# Cloud sessions

How xlsft`s harness runs a session with no machine of the user's at all: in a one-off container on a **runner node**. It builds on always-on hosts (docs/design/hosts.md): the work queue, git credentials from the code host connection, landing at the end of each turn and the daemon that runs sessions are the same. What is new is who starts the daemon (a runner, per session, in a container) and that the container goes away when the session is idle and comes back when it is needed.

Contracts: `proto/harness/relay/v1/hosts.proto` (`CloudRunnerConfig`, `CloudSession`, `PollCloudRunner` and the other `Cloud*` messages; fields added to existing messages are numbered from 450). Server: `packages/core/src/relay/cloud.ts`. Runner: `packages/client/cloudrunner`, `harness-client runner`. In the container: `harness-client daemon --cloud` (`packages/client/daemon/cloud.go`). Image: `deploy/Dockerfile.agent`; a runner node: `deploy/Dockerfile.runner`, `deploy/compose.runner.yml`, deploy/README.md.

## Lifecycle

```
                 StartHostWork(cloud)            message to a removed session (relay command)
                        │                                     │
                        ▼                                     ▼
   ┌──────────┐   runner polls    ┌──────────┐  daemon registers  ┌─────────┐  turn ends, report 0 working  ┌──────┐
   │ STARTING │ ───────────────▶ │ STARTING │ ─────────────────▶ │ RUNNING │ ────────────────────────────▶ │ IDLE │
   │ (wanted) │  token, create,   │ container│                    └─────────┘ ◀──────────────────────────── └──────┘
   └──────────┘  start            └──────────┘                         │      next turn starts                │
        ▲   │                          │ no registration in 10 min     │ lifetime over                       │ idle > timeout (1 h)
        │   │ no runner / quota         ▼ (3 tries)                     ▼                                     ▼
        │   └───────────────▶  ┌────────┐                          ┌─────────┐ ◀──────────────────────────────┘
        │    waits, or fails   │ FAILED │                          │ REMOVED │   container and its disk removed
        │                      └────────┘                          └─────────┘
        └──────────────────────────────────────────────────────────────┘  next message: STARTING again
   work cancelled / deleted: ENDED from any state, the container is removed and never comes back
```

Each turn: the daemon in the container fetches at the start, and at the end commits what is uncommitted and pushes the session's branch `harness/<work id>` (hosts decision 12). The transcript is on the server already (sync). So a removed container loses nothing that a new one can't get back: clone, check out the branch, download the transcript, resume the session under the same id.

## Decisions

Numbered so later changes can refer to them. A decision that changes is edited in place, with what it replaced.

1. **A runner is an agent host of a third scope, `CLOUD`.** It is registered by an admin (`hosts.write`) like a team host, has a group (empty: everyone) whose members may use it, a capacity (`max_sessions`: containers at once) and a `CloudRunnerConfig` (image, runtime, limits, timeouts, caches). Reusing `agent_host` keeps one admin list, one registration and the same `allowed_repositories` for the connection's own identity.
2. **`harness-client runner` is its own command, not a mode of the daemon.** A runner holds a token that can only call `HostAgentService` (device kind `host_runner`, like a supervisor's), talks to the container engine and runs the egress proxy. It runs no agent and reads no repository. The daemon is the thing that runs sessions; it runs inside each container (`daemon --cloud`).
3. **One container per session, one device per session.** The container's daemon signs in with a device of kind `host_cloud`, made for that cloud session alone and owned by the user (decision 8 of hosts: no role). Its token is rotated every time a container is created, so a removed container's token is dead. The relay sees the session on that device, wherever the container runs; a session that comes back on another runner is still the same device.
4. **The server decides, the runner obeys and reports.** `PollCloudRunner` is a long poll that carries the runner's list of its containers (by label) and returns what to start and what to remove. That one call is the reconciler: on runner start, and every 30 s at most, the server compares the list with its rows. A container the server doesn't want (unknown, ended, idle past the timeout, past its lifetime, or a duplicate) is removed with its writable layer; a row whose container is gone is marked so. Timeouts live on the server, so an admin's change applies to running containers at once and survives restarts of either side.
5. **States** are `STARTING`, `RUNNING`, `IDLE`, `REMOVED`, `FAILED` and `ENDED`, in `cloud_session`. `RUNNING` and `IDLE` come from the container's daemon's reports (working sessions above zero or not); the idle timer starts with the last report that had work. A `STARTING` row whose container has not registered within 10 minutes is retried, three times, then `FAILED`.
6. **Removed means removed.** Idle for the timeout (default 1 hour), the container is stopped and removed together with its writable layer, which is where the checkout is: there is no per-session volume to forget. Max lifetime (default 24 hours, counted from the container's start) removes it even mid-turn; the turn reads as interrupted, the branch has the last turn's work.
7. **A message wakes a removed session.** A relay command for a session whose device is a cloud session's is accepted even though no machine has it open, and puts the cloud session back to `STARTING`, on the runner with the most room among those the user may use (its old one if it has room). The command waits in the relay queue (ten minutes by default) and is delivered when the new container registers the session.
8. **Resume, not fork.** The container's daemon gets the same work back from `ClaimHostWork` (with `session_id` set), makes a fresh checkout of the existing branch (or a new one from the base, when the first turn pushed nothing), writes the server's transcript of the session to the local store unchanged, and resumes it under the same id. The upload of the transcript back is idempotent by entry uuid.
9. **Landing is never `NONE` in the cloud.** The branch is where the state lives, so a cloud session with landing `NONE` pushes its branch anyway (as `BRANCH`); a pull request is opened only when asked.
10. **Credentials reach only the processes that need them.** The runner copies the container's device token into the created, not yet started, container as `/run/harness/token` (the engine's archive API, 0600, owned by the agent user); the daemon reads it, deletes it and keeps it in memory (non-dumpable on Linux). It is never in the image, the container's environment or `docker inspect`. Git credentials are asked for per git command, as on hosts (decision 10 there). The agent's shell has neither.
11. **Network: one way out.** Containers are on an internal network (`harness-cloud`, no route out). The only thing they reach is the runner's egress proxy, an allowlist proxy (the sandbox's, `HTTPS_PROXY`), which knows which container is asking by its address and applies that user's allowlist: the policy's `cloud.egressAllow`, or by default the package registries and the code hosts of the server's connections, plus the server's own public address (only its host and port). Private, loopback and link-local addresses are refused at connect time unless an entry names the address itself. So: no runner host network, no core internals, no cloud metadata.
12. **Limits per container** (CPU, memory with no extra swap, pids, disk) come from the runner's config. Memory, CPU and pids are the engine's. Disk: with `storage_opt_size` the engine enforces a size on the writable layer (overlay2 on xfs with pquota, or btrfs/zfs); otherwise the runner measures each container's writable layer every five minutes and removes one that is over its limit (`FAILED`, "over its disk limit"). The containers run as uid 10001, with all capabilities dropped, `no-new-privileges` and no Docker socket. The root file system stays writable: the checkout, the home and `/tmp` are all in the writable layer, which is the session's disk and goes with the container.
13. **Quotas** are in the managed policy, enforced by the server: `cloud.maxSessionsPerUser` and `cloud.maxSessionsPerGroup` count containers that exist (`STARTING`, `RUNNING`, `IDLE`). Over a quota, new work is refused and a wake waits (the message stays queued) until something is removed. `cloud.disabled` turns cloud sessions off for whom the policy targets.
14. **Bypass permissions stays off in the cloud** unless the policy says `cloud.allowBypassPermissions`. The server refuses it at `StartHostWork` and the container's daemon starts its sessions with a mode policy that refuses it, so a relay command can't turn it on either.
15. **Caches are shared per user, read-write, with a size cap**, not read-only across users. The pnpm store, the Go module and build caches and the uv cache are bind-mounted from the runner (`/cache/pnpm`, `/cache/go`, `/cache/uv`). Read-only caches would have to be filled by someone: a cache filled by one user's agent and read by another's is a way to plant code (an extracted Go module or a uv wheel is trusted once it is in the cache). So the default scope is the user; an admin may set `cache_scope: runner` for a runner whose users trust each other. The runner keeps each cache under `cache_max_bytes` by removing the least recently used files, only while no container of that scope runs.
16. **gVisor is an option of the runner**, `runtime: runsc`, recommended. What it adds and doesn't is below.
17. **Driver interface.** `cloudrunner.Driver` is what the runner needs of an engine: ping, network, create/start/stop/remove, list by label, inspect, writable layer size. `cloudrunner/docker.go` implements it on the Docker Engine API over a unix socket or TCP, which Podman serves too (`podman system service`). MicroVMs (Firecracker, Kata) and Kubernetes would be other drivers; none is written.
18. **`.harness/worktree.json` is not run yet.** The setup file is not on main; the container's daemon has the hook (`Options.Setup`) where it will run, after the checkout and before the session, as the untrusted-folder rules allow.

## Security model

What separates a cloud session from everything else:

| From | By |
|---|---|
| Other users' sessions | A container each, its own writable layer, uid 10001, no capabilities; caches per user (decision 15) |
| The runner host | No host network, no Docker socket, no host paths but the caches; with gVisor, a user-space kernel |
| The core server's internals | The egress proxy lets the container reach only the server's public host and port, and the network has no other route |
| The internet | The allowlist; DNS of the internal network is the engine's (names of the internal network resolve, nothing else answers) |
| The user's tokens | The device token is per session, rotated per container, read once from a file the daemon deletes; git tokens per git command; nothing in env, image or argv |

What it does not stop: an agent can use what is allowed. A registry or code host on the allowlist can carry data out (publish a package, push to a fork, if it has a token for it it brought along). The agent can call the server's public API as that session's device, which is the user. A kernel exploit in a container without gVisor reaches the runner host and through it other containers.

### What gVisor adds, and what it doesn't

gVisor (`runsc`) puts a user-space kernel between the container and the host kernel: the container's system calls are served by gVisor's Sentry, which uses a small, filtered set of host system calls. So a bug in the Linux kernel's handling of system calls, file systems or network stacks is mostly not reachable from the agent. That is the main way out of a plain container, and the reason it is recommended.

It doesn't change what the container is allowed to do: the same network (the proxy is still the only way out), the same mounts (the caches are as writable as without it), the same limits. It doesn't protect the agent from itself or other sessions of the same user that share a cache. It costs: slower system calls and file I/O (builds and installs are noticeably slower), and some programs that need unusual kernel features don't run. A bug in gVisor itself is a way out too, a smaller one.

## Admin view

`ListAgentHosts(all)` shows each runner with its config and counts of containers by state; `ListCloudSessions` (per runner for admins, the caller's own otherwise) lists the cloud sessions with user, state, image, disk use, start and last activity. An admin can remove a container at once (`StopCloudSession`); the session comes back on its next message.

## What is not there

- `.harness/worktree.json` setup on container start (decision 18).
- Drivers other than Docker/Podman.
- A pre-warmed shared cache filled by the runner itself (decision 15 says why not by agents).
- An image per session from a list of the user's: the runner's image, or one of its `allowed_images` chosen at start.
- More than one replica of the server, as for hosts.
- Egress to arbitrary ports: the proxy speaks HTTP CONNECT and plain HTTP, so `git://` and SSH remotes don't work from the agent's shell (the daemon's git uses HTTPS through it).
