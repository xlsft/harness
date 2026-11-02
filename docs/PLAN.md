# harness — implementation plan

> A harness to replace Claude Code: the same tools, the same UX, the same architecture, any model via OpenAI- and Anthropic-compatible endpoints.
> UI stack: Nuxt 4 + Nuxt UI v4. Based on the 16 documents in `docs/research` of the xlsft/edualc repository (data as of October 2026, CC 2.1.289).

## Product model: like Claude Code + your own backend instead of Anthropic

| Claude Code | harness |
|---|---|
| `claude` runs locally: agent loop, tool execution, permissions, hooks, stdio-MCP | **`harness` (client runner)** runs locally: the same. Executes tool calls in its own filesystem |
| Login via claude.ai, models via the Anthropic API, the user has no keys | Login to the **harness server**. Models go through the server's **LLM gateway**; provider keys live only on the server |
| System prompt and tool descriptions ship with CLI releases | **Prompts live on the server** (a versioned prompt bundle) and are synced to machines |
| Account data, managed settings, sessions on claude.ai | **All user data lives on the server** (settings, memory, skills, agents, sessions…); the client pulls and caches it |
| Remote Control: CLI → relay → claude.ai/code and mobile | The same relay in the harness server + Web UI + tunnels |
| Desktop launches the CLI as a subprocess, SSH installs the CLI on the remote | The same |

**Principle:** the client stays a client. Only execution (shell, filesystem, git, stdio-MCP) and the cache live locally. Everything "business" — auth, keys, models, prompts, user data, history, usage — lives on the server. The server's Docker container is the single entry point to that part.

```
┌────────────────────────── harness server (Docker) ──────────────────────────┐
│ auth · LLM gateway (keys, routing, provider adapters, usage, quotas)        │
│ prompt registry · user data (settings, memory, skills, agents, MCP, sessions)│
│ relay / remote control · tunnels · Web UI (Nuxt)                            │
└──────▲──────────────────────▲───────────────────────────▲───────────────────┘
       │ gRPC: gateway, auth, │ relay (outbound           │ Connect
       │ sync API             │ from the client)          │
┌──────┴──────── user's machine ──────────────────────────┐   Browser/phone
│ harness: agent loop · tool execution · permissions      │   (Web UI on the server,
│         hooks · stdio-MCP · data and prompt cache       │    controls sessions
│   ▲ TUI (same process)       ▲ gRPC (local socket)      │    via relay)
│   terminal             Desktop UI (Nuxt UI in a shell)  │
│                              │ ssh (client's keys)      │
└──────────────────────────────┼───────────────────────────┘
                               ▼
                    remote: harness over stdio via SSH
```

---

## 0. Key decisions

| # | Decision | Choice | Why |
|---|---|---|---|
| A1 | Separation | **Client:** loop, tool execution, permissions, hooks, stdio-MCP, cache. **Server:** auth, LLM gateway with keys, prompts, all user data, relay, tunnels, Web UI | Like CC + an Anthropic backend. Keys and business logic never reach the machines |
| A2 | LLM gateway | The client talks to the server through **one canonical API** (`GatewayService`, Anthropic-Messages-shaped, server-streaming RPC). The server translates to upstream: Anthropic / OpenAI-chat / OpenAI-responses | Adapters, capability negotiation, cache breakpoints, retries and fallback live in one place. A new provider or model requires no client updates |
| A3 | Prompts | **Prompt registry on the server:** system prompt by section, tool descriptions, reminder templates, compactor prompt, bundled skills, **per-model overrides**. Versioned; the client caches by ETag | Prompt engineering for different models without a client release. A/B tests and rollbacks are possible. The client keeps working from cache during a brief offline period |
| A4 | User data | **The server is the source of truth**, the client is a cache with sync (document versions, push of local changes) | "The client pulls everything from the server." The same environment on any machine |
| A5 | Contracts and transport | **All interaction is gRPC-compatible RPC defined in protobuf** (`/proto`, buf). Transport is **ConnectRPC**: one endpoint speaks gRPC, gRPC-Web and Connect, so Go uses plain gRPC and browsers use Connect over HTTP/1.1. Only unary + server-streaming RPCs (no bidi) so every method works from a browser. Frontend ↔ client uses `AgentService` over a local socket (or the SSH stdio stream); headless `-p` text/json stays for scripting | Strict typed contracts with codegen for TS and Go, compatibility checks via `buf breaking`, one schema for server, client, CLI, desktop and web |
| A6 | Relay | Outbound client connection to the server (server-streaming RPC for downstream + unary RPCs for upstream), envelope `{seq, session_id, epoch, uuid, payload}` with `AgentService` events as the payload | Works behind NAT. CC Remote Control patterns: epoch, dedup, FlushGate, heartbeat |
| A7 | UI | One Nuxt app `packages/ui` for desktop and mobile (and web) with a `SessionTransport`: `ipc` (desktop → local Go client over stdio), `relay` (web/mobile → server → client on the machine) | All GUI clients look the same, as in CC |
| A8 | Canonical message model | **Anthropic-like content blocks** (`text`, `thinking`+`signature`, `tool_use`, `tool_result`, `image`) — in both the client and the gateway API | Reducing rich to poor is easier. tool_use/tool_result pairs and thinking signatures are respected |
| A9 | Auth | **Better Auth** on the server (login, password, optional OAuth and 2FA, roles). `harness login` — device-flow → device token (revocable) + short-lived access token with refresh | Ordinary client login, like `claude login` |
| A10 | Storage | **Server:** SQLite by default / Postgres optionally (Drizzle) + a directory for attachments. **Client:** cache in `~/.harness/` + local CC-format JSONL (offline writes, synced to the server) | `docker compose up` with no dependencies. A session is not lost when the connection drops |
| A11 | CC compatibility | Project `.claude/`, `CLAUDE.md`, `AGENTS.md`, `.mcp.json` are read from the repository. User-level `~/.claude` — a one-time **import to the server** (`harness import claude`) | The project is portable. After import, the user level lives on the server |
| A12 | Languages and distribution | **Server** — TypeScript on Nitro v3. **Client and CLI** — Go: a single static binary for all OSes and architectures (cross-compile without CGO). The CLI imports the client as a Go package. Desktop and mobile use the client as a sidecar binary | One file is easy to install and push over SSH, fast startup, solid process/pty primitives. The charm.land toolchain gives a CC-level TUI |
| A13 | Legal | Clean-room: compatible formats and protocols — yes, CC code and prompts verbatim — no | See feature-parity §17–18 |

### Repository structure

A monorepo **without monorepo tooling**: no pnpm workspaces, turbo or shared lock files. Each package is self-contained, with its own `package.json` or `go.mod`, its own build and its own CI job. The shared contract between TS and Go is **protobuf in `/proto`**. Generated code is committed into each package (`buf generate` from the root), so there is no shared runtime code and every package builds on its own.

```
docs/
  research/        # research (copy from xlsft/edualc if needed)
  protocol/        # transport conventions and behavior that is not visible in the schema
  PLAN.md
proto/             # protobuf contracts (buf module): harness.{auth,llm,prompts,agent,common}.v1
buf.gen.yaml       # codegen: Go -> packages/client/gen, TypeScript -> packages/core/src/gen
packages/
  core/            # server: Nitro v3 (nitro.build), TypeScript. Auth, LLM gateway + provider adapters,
                   #   prompt registry, user data + sync, MCP registry and proxy, relay, tunnels. Dockerfile
  client/          # universal client for frontends, Go. Agent loop, tool execution, permissions,
                   #   hooks, stdio-MCP, sessions (JSONL, checkpoints), sync cache, gateway and relay client.
                   #   Shipped as a Go package (for cli) and as the `harness-client` binary serving AgentService (for ui/desktop, SSH)
  ui/              # frontend for desktop and mobile: Nuxt 4 + Nuxt UI v4 (design to come separately)
  cli/             # CLI frontend, Go: the full charm.land toolchain (Bubble Tea, Bubbles, Lip Gloss, Glamour,
                   #   Huh, Harmonica, Log…). Truecolor with fallback to ANSI-16/256
                   #   for emulators without support. Design 1:1 with Claude Code. The `harness` binary
```

**Order of work:** first `core` + `client` (the whole harness works headless, verified by tests and `harness-client -p`), then the `cli` and `ui` frontends, deployment last.

---

## 1. Server (`packages/core`, Nitro v3, Docker) — the "business part"

### 1.1 Auth and devices
- Better Auth: users, roles (admin/user), password + optional OAuth/2FA.
- **Device-flow:** `harness login https://harness.example.com` → code in the browser → device token. Revocation in the UI, device list (OS, version, last online).
- Short-lived access token for the gateway and relay, refresh without breaking the stream (401 → refresh → retry).

### 1.2 LLM gateway
- **Providers** (admin): `{type: anthropic|openai-chat|openai-responses, baseUrl, apiKey, headers}`. Keys are encrypted with the master key `HARNESS_SECRET`.
- **Model catalog:** discovery via `GET /v1/models` plus manual entries. Fields: `contextWindow`, `maxOutput`, `capabilities`, prices, visibility by user and role. Aliases and **slots** (`main`, `small`, `plan`) are configured on the server.
- **Canonical API for the client:** `POST /api/llm/messages` (stream), `POST /api/llm/count_tokens`, `GET /api/llm/models`. Request — content blocks + tools + system by section + effort/thinking + `request-class` (main | subagent | compaction | auxiliary).
- **Upstream translation:**
  - `tool_result` → `tool` role, images → into a user message.
  - `thinking` ↔ `reasoning_content` or Responses reasoning.
  - `stop_reason` ↔ `finish_reason`.
  - effort → `output_config.effort` / `reasoning_effort`.
  - reminders → user content.
  - `cache_control` breakpoints for Anthropic, stable prefix for OpenAI.
- **Capability negotiation** (retry without the rejected feature, remembered per model), retries with backoff (`retry-after`, `x-should-retry`), stream idle watchdog and ping to the client, **fallback chain** (only overloaded/unavailable).
- **Usage and quotas:** normalized usage (`input/output/cacheRead/cacheWrite`) → cost → limits per user and role, per-session budget. Usage dashboard.
- Routing by `request-class`: compaction and auxiliary go to a cheap model.
- SSRF filter for the upstream baseUrl. Private addresses require explicit admin permission (local vLLM/Ollama).

### 1.3 Prompt registry
- Bundle: system prompt by section (identity, safety, style, doing tasks, tool policy, env template), **descriptions and schemas of all tools**, system-reminder templates, compactor prompt, title prompt, WebFetch summarization, auto mode classifier, bundled skills (`/init`, `/review`, `/security-review`, `/simplify`), built-in agents (`Explore`, `Plan`, `general-purpose`), output styles.
- **Overrides per model or family** (weaker models need different wording and examples). Versions, changelog, rollback. Later — A/B by user.
- The client fetches the bundle by ETag at startup and on new sessions. **Within a session the bundle is frozen** (for the prompt cache); it updates on a new session or after compact. The bundle version is written into the session.
- Editor in the Web UI for admins with a preview of the assembled prompt for a chosen model.

### 1.4 User data (source of truth)
| Data | How the client uses it |
|---|---|
| User settings (default model, effort, mode, output style, attribution, UI) | Cached, applied as the user-level settings |
| User-level permission rules | Applied together with project rules. "Don't ask again" at user level is written to the server |
| User memory (analogue of `~/.claude/CLAUDE.md`), auto memory per project | Injected into project context |
| Skills, agents, commands, output styles (personal + shared by admin) | Synced to cache, loaded via progressive disclosure |
| User-level hooks | Synced, executed locally — only after confirmation on this machine (trust) |
| **MCP servers — all configs and all authorization** (see 1.5) | Pulled onto any device. No need to carry `.mcp.json` and `~/.claude.json` around |
| Sessions: metadata + transcript | The client writes local JSONL and streams events to the server. Session list, resume and search — from the server on any machine |
| SSH hosts (addresses and aliases only, **no keys**) | UI hints |
| Admin policies (managed settings): deny rules, allowed models, prohibitions | Highest priority |

- **Sync API:** `GET /api/sync?since=<version>` → changed documents, `PUT` of a document with `baseVersion` (conflict → 409 → the client merges or asks again). Live updates through the same channel as the relay.
- Import of `~/.claude` (CLAUDE.md, skills, agents, commands, output styles, settings) and CC sessions: `harness import claude`.

### 1.5 MCP via the server
Goal: connect an MCP once — it works on all devices, on the web and on SSH hosts, with no config transfer and no repeated authorization.

- **MCP registry on the server:**
  - Scopes `user` (personal) and `org` (admin distributes to everyone or to roles). The project `.mcp.json` from the repository stays as is, but its approval and secrets are also stored on the server.
  - Fields: name, transport (stdio / Streamable HTTP / SSE / WS), command or URL, args, env, headers, `alwaysLoad`, enable/disable.
  - Import from `~/.claude.json` and `.mcp.json` via `harness import claude`.
- **Authorization on the server:**
  - The OAuth flow goes through the server: discovery, callback to `HARNESS_PUBLIC_URL`, refresh before expiry, re-auth on new scopes.
  - Static tokens, headers and env secrets are encrypted with the master key.
  - The "needs auth" status is visible in all clients. Authorize once on the web or desktop — it works everywhere.
- **Remote MCP (HTTP/SSE) execution** — **via the server.** The client calls tools through the server, which injects tokens and proxies. Tokens never reach devices. Bonus: such MCPs are available even to sessions started from the web.
- **stdio MCP execution** — the process must run locally, next to the code:
  - The config is synced.
  - The client receives secrets (env/args) from the server **at process launch**: a short-lived request, memory only, never written to disk, never logged.
  - Option `secretsMode: proxy` — for servers that need the secret only for outgoing HTTP. Later.
- **Per-device applicability:**
  - A stdio server may require `npx`/`uvx`/a binary that is missing on the machine. The client checks dependencies and marks the server `unavailable on this device` with a reason.
  - Per-device or per-OS overrides are possible (a different binary path); they are also stored on the server.
- **Security:**
  - A new stdio server (which means executing a command on the machine) requires **local confirmation on each device** at first launch. The server cannot silently start a process on a machine.
  - Remote MCP with `requiresUserInteraction` always asks.
  - Org-MCP changes are shown in the UI with a diff.
- **Tool search:** the list of MCP tools and their schemas is cached on the server and served to clients. `ToolSearch` works on top of this cache; the set is frozen per session.
- MCP prompts → slash commands, resources → `@server:resource` — the same in all clients.

### 1.6 Relay, Remote Control, tunnels
- **Relay:** the client opens an outbound server-streaming RPC and sends upstream data with unary RPCs. Envelope `{seq, session_id, epoch, uuid, payload}`, payload is `AgentService` events and commands.
  - Heartbeat ~20 s, reconnect with jitter, dedup by UUID.
  - **Epoch** displaces the old connection.
  - **FlushGate:** on connect, history first, then live.
  - Batching, close codes.
- **Remote control:** enabled by default for all sessions of a logged-in client (configurable). The session is visible on the web and controlled from there. Permission prompts and questions go to every client; the first answer wins.
- **Spawn:** `harness daemon` registers the machine. From the web: "new session on machine X in folder Y" (allowed roots only).
- **Tunnels:** preview `https://<id>.preview.<host>` → a port on the client's machine via relay, accessible to the owner only. Terminal on the web — the client's PTY via relay.
- **Push:** Web Push on permission, question and turn completion, suppressed on activity (presence).
- Attachments from the web: upload to the server → the client downloads → `@` reference.

```yaml
# docker-compose.yml
services:
  harness:
    image: harness/server
    ports: ["3000:3000"]
    volumes: ["harness-data:/data"]
    environment:
      HARNESS_PUBLIC_URL: https://harness.example.com
      HARNESS_SECRET: ...
```

---

## 2. Client (`packages/client`, Go)

### 2.1 Agent loop
- prompt → `gateway.messages` (stream) → tool_use → permission → **local execution** → tool_result → repeat.
- Read-only tools run in parallel, mutating ones sequentially. The tool_use/tool_result pair is mandatory. `maxTurns`, budget (limits are also checked by the server).
- **Queue and interrupts**, 1:1 semantics from interaction-flows:
  - Enter during a turn queues the message; it is merged in after tool calls.
  - Ctrl+Enter — "now".
  - Esc cancels the tool (kill) and writes `[Request interrupted by user]`.
  - Immediate commands (`/model`, `/effort`, `/status`, `/usage`, `/tasks`, `/rename`, `/btw`) bypass the queue.
- **Context assembly:**
  - Prompt bundle (from the server) + tools.
  - Project context: CLAUDE.md/AGENTS.md from the repo + user memory from the server.
  - Conversation.
  - `DYNAMIC_BOUNDARY`: machine env and the git snapshot go into the first user message.
- **Compaction:** first trimming tool outputs, then summarization via the gateway (`request-class: compaction`), anti-thrashing.
- **Reminders** from bundle templates: CLAUDE.md, skills, agents, todo, file-changed, attribution, hooks, output style.
- **Offline:** without the server there is no LLM, so no new turn. History, `/resume`, viewing and code rewind work from the local cache. A drop mid-turn → gateway-client retries, then the session goes into "waiting for connection".

### 2.2 Tools (executed locally; descriptions come from the prompt bundle)

| Wave | Tools |
|---|---|
| **M0** | `Read`, `Write`, `Edit`, `Bash` (cwd survives calls, env does not; shell snapshot; 2/10 min; output over 30K characters → file + preview; exit 1 for `grep/rg/find/diff/test/git diff` is not an error), `Glob`, `Grep` |
| **M1** | `AskUserQuestion`, `EnterPlanMode`/`ExitPlanMode`, `TodoWrite`, background Bash + `TaskStop` |
| **M3** | `Agent` (subagents, `model` — any model from the server catalog), `Skill`, `WebFetch` (fetch locally, summarization via the gateway), `WebSearch` (via the server), `ToolSearch`, MCP tools, `NotebookEdit` |
| **M4** | `EnterWorktree`/`ExitWorktree`, `Monitor`, `Cron*`/`ScheduleWakeup`, `SendMessage`/`ListAgents`, `LSP`, `PushNotification`, `SendUserFile` |

### 2.3 Permissions (decided by the client; the server cannot bypass them)
- Modes `default`, `acceptEdits`, `plan`, `dontAsk`, `bypassPermissions`. Later `auto` (classifier via the gateway).
- `Tool(spec)` rule engine: deny → ask → allow, parsing compound Bash, stripping wrappers, gitignore globs.
- Sources by priority: **admin policies (server)** → CLI flags → `.claude/settings.local.json` → `.claude/settings.json` → **user rules (server)**.
- Read-only allowlist, protected paths, workspace trust (project hooks and `.mcp.json` apply only after confirmation on this machine).
- Remote control and spawn from the web do not bypass permissions. Bypass mode cannot be enabled remotely.

### 2.4 Sessions
- Local CC-format JSONL (`~/.harness/projects/<slug>/<id>.jsonl`) + event stream to the server. The server keeps a copy for the list, search, the web and resume on another machine (if the same repository is available).
- `--continue`/`--resume`, `/branch`, fork.
- `/rewind`: code+conv / conv / code / summarize. File checkpoints are **local** (`~/.harness/file-history/`) — this is data about the machine's filesystem.
- AI titles via the gateway (`request-class: auxiliary`).

### 2.5 Extensibility
- Memory, skills, agents, commands, output styles, hooks: **user level from the server**, **project level from the repository** (`.claude/`, `.harness/`, CLAUDE.md, AGENTS.md, rules with `paths`, `@import`).
- **Hooks:** `command` ones run locally, `http` ones from the client.
- **MCP:** configs and authorization — from the server (section 1.5). Remote MCPs are called through the server proxy. stdio runs locally with secrets issued at launch, after local confirmation on the device. Own `ToolSearch` on top of the server schema cache.
- **Plugins/marketplace** — M4, catalog on the server.

### 2.6 Entry points
The `harness-client` binary (from `packages/client`) is the headless core. The `harness` binary (from `packages/cli`) is the TUI on top of the same Go package and the same subcommands.
- `harness login <url>` / `logout` / `whoami`.
- `harness` — TUI in cwd. `harness -p … [--output-format text|json]` (or `harness-client -p …`) — headless mode for scripts.
- `harness-client serve [--socket path | --stdio]` — serves `AgentService` for frontends: desktop over a local socket, SSH sessions over stdio.
- `harness daemon` — the machine is available for spawn from the web.
- `harness ssh user@host:/path` — upload or update the binary, session over stdio via SSH. The remote gets a separate, short-lived token bound to the session.
- `harness import claude`, `harness mcp …`, `harness doctor`.

---

## 3. Desktop and mobile (shells around `packages/ui`)
- **Desktop:** a native shell (Tauri 2 or Electron) + `packages/ui` with the `ipc` transport. The Go client ships as a sidecar binary. Same login (device-flow or embedded browser).
- **Mobile:** the same `packages/ui`. No local execution, only the `relay` transport: viewing and controlling sessions, answering permissions and questions, push.
- **Client process:** the shell runs `harness-client serve --socket …` (one process with a session per subprocess-like worker, or a process per session) and the UI talks `AgentService` to it. Health check, a single stdio-MCP registry per host.
- **SSH:** client keys (`~/.ssh/config`, agent, ProxyJump) → bootstrap the binary on the remote → stdio over the SSH channel. The remote runner reaches the gateway with the session token issued by desktop.
- Native: folder picker, notifications, tray, deep links, auto-update, open in editor.

## 4. UI (`packages/ui`, Nuxt 4 + Nuxt UI v4) — shared by desktop, mobile and web (design to be provided separately)

The skeleton is `UDashboardGroup` + `UDashboardSidebar` + resizable `UDashboardPanel`. Chat — `UChatMessages` / `UChatPrompt`, markdown — MDC + Shiki.

- **Sidebar** (data from the server, the same on any machine):
  - `+ New session`, Pinned/Recents, filters by project, machine and status.
  - Row: `repo / title`, status, machine, `+N −M`, PR badge, unread.
- **New session:** where (this machine / SSH host / machine with daemon) → folder (+ worktree) → model (server catalog) → mode.
- **Transcript:**
  - Collapsed tool calls, Normal/Thinking/Verbose (Ctrl+O), "Run in background".
  - Inline diffs, clickable paths, hover: copy/rewind/fork.
- **Cards:**
  - permission ("Yes / Yes, don't ask again for `<pattern>` / No" + comment, "2 of 5");
  - AskUserQuestion;
  - plan;
  - limit, quota or retry;
  - machine offline;
  - inter-session message.
- **Pinned task list. Prompt:**
  - Queue with ✕, Stop.
  - `@` files, `/` menu, attachments.
  - Model/Effort/Mode pickers, context ring, usage and quota.
- **Panels:** Tasks, Diff (comments, Review, Create PR), Plan, Subagent, Terminal (the PTY is opened by the Go client; desktop — via ipc, web/mobile — via relay), File, Preview (tunnel), split view, side chat, status footer.
- **Account (user):** profile, devices and tokens, settings, memory, skills, agents, commands, output styles, MCP connectors, usage.
- **Admin:** users and roles, providers and keys, model catalog and slots, **prompt registry** (editor, versions, per-model overrides, preview), policies, quotas, usage by user.

---

## 5. Roadmap

Order: **core (`core` + `client`) → frontends (`cli`, `ui`) → deployment.** Until the frontends exist, everything is verified by tests and the headless `harness-client -p` mode.

### M0 — Skeleton of core + client (≈3 weeks)
1. Contracts in `/proto` (done): `DeviceAuthService`, `GatewayService`, `PromptService`, draft `AgentService`; codegen via buf.
2. **core (Nitro v3):** Better Auth (admin + users), device-flow, providers with key encryption, model catalog, Connect services for auth, gateway (`StreamMessage` server streaming) and prompts, with Anthropic and OpenAI-chat adapters. **Contract tests on fixtures** (Anthropic, OpenAI, DeepSeek, OpenRouter, vLLM/Ollama). Prompt registry v0: bundle from files, served by ETag.
3. **client (Go):** `login` (device-flow), prompt bundle loading, agent loop via the gateway, `Read/Write/Edit/Bash/Glob/Grep`, simple permissions, local JSONL, headless `-p` (text/json), `serve` exposing `AgentService`.

**Done when** core runs locally (`pnpm dev`), an admin has added Claude keys and keys for any OpenAI-compatible provider, and `harness-client -p "fix the test"` in a project folder fixes the test on both models. No keys are on the machine.

### M1 — Core parity + sync (≈4 weeks)
- client: permissions engine, modes, trust, plan mode, AskUserQuestion, TodoWrite, queue and interrupts (via control requests), background Bash.
- client: context assembly, reminders, compaction, checkpoints/rewind, resume/fork.
- core: slots, effort, capability negotiation, fallback, usage and quotas.
- core + client: **sync API** (settings, user memory, user rules, sessions), `import claude`.

### M2 — Extensibility + relay (≈4 weeks)
- Skills, agents/subagents, commands, output styles (server + repo), hooks, WebFetch/WebSearch.
- **MCP via the server:** registry, OAuth and secrets, proxy for remote, secret issuance for stdio, per-device confirmation, schema cache + ToolSearch.
- Relay and remote control in core and client: session mirroring, permission answers from any connection, spawn via `daemon`, tunnels (preview, PTY).

### M3 — Frontends
- **cli (Go, charm.land toolchain):** TUI 1:1 with Claude Code. Before development — visual research: live `claude` sessions in a terminal, snapshots of all states (stream, tool calls, permission prompts, queue, `/` menu, status line, diffs, spinners), truecolor and ANSI fallback.
- **ui (Nuxt UI):** per the supplied design. Sidebar, transcript, cards, prompt, panels. Desktop shell with the sidecar client, mobile.
- Admin screens: providers, models, prompt registry, users, policies, usage.

### M4 — Deployment and advanced
- Core Docker image, compose, backups, client updates (binary self-update, auto-update on SSH hosts).
- Git panels and PR/CI, worktrees, sandbox runtime, auto mode, Monitor/Cron, `SendMessage`, auto memory, plugins/marketplace, LSP, prompt A/B.

---

## 6. Risks

| Risk | Mitigation |
|---|---|
| The server is a single point of failure (no LLM without it) | Simple deployment and volume backups. The client survives short drops (retries, waiting for connection). Local history is available offline |
| Latency and streaming through the gateway | The gateway is a thin streaming proxy with no buffering, with ping, and the server sits close to the providers |
| Server compromise → control of machines | Permissions are decided by the client. Bypass cannot be enabled remotely. Spawn — only in allowed roots. Hooks and MCP from the server require local trust. Device revocation |
| A prompt update breaks behavior | Versions, bundle frozen within a session, rollback, preview, an eval task set before publishing |
| Zoo of "OpenAI-compatible" endpoints | Capability profiles on the server, contract tests, negotiation, per-model prompt overrides |
| stdio-MCP secrets on the device (the process needs them in plaintext) | Issued only at launch, memory only, never to disk. Device revocation → revocation of issuance. Where possible, remote MCP through the server proxy instead of stdio |
| Sync conflicts (edits from two machines) | Document versions, 409 + merge; for memory — last write wins with version history |
| Prompt cache breaks → expensive | Bundle frozen per session, stable prefix, deferred MCP, cache-hit metric |
| Legal | Clean-room, own prompts, compatibility of formats and protocols only |

## 7. Open questions (defaults chosen)

1. ~~Local provider bypassing the server~~ — **decided: no, all LLM calls go only through the server gateway.** The client cannot talk to providers directly. Local models (vLLM/Ollama) connect as a provider on the server: they must be reachable from the server's network, and the admin explicitly allows private addresses.
2. ~~Transcript storage~~ — **decided: the server stores full transcripts and metadata.**
3. Desktop + mobile shell: **Tauri 2** (default: one shell for desktop and iOS/Android, Go client as a sidecar) or Electron + Capacitor.
4. ~~OpenAI Responses API~~ — **decided: implemented in M1, before the first real-model tests.**
5. WebSearch backend on the server (Brave, Tavily, SearXNG, provider-native).
