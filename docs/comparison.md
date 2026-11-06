# xlsft\`s harness compared with Claude Code, Codex and Antigravity

This is a feature-by-feature comparison of xlsft\`s harness (open source, self-hosted) with Claude Code, OpenAI Codex (CLI, app, cloud) and Google Antigravity (app, CLI, IDE). It is also our gap list: see [What xlsft\`s harness doesn't cover yet](#what-xlsfts-harness-doesnt-cover-yet). Competitor cells come from official docs; xlsft\`s harness cells come from reading this repository's code.

**Last checked:** 2026-11-06. Versions seen: Claude Code 2.1.318 (2026-11-04); Codex CLI 0.167.0 (2026-11-06); Antigravity App 2.29.0, CLI 1.5.1. xlsft\`s harness has no tagged release yet; its column reflects the code at commit `83363db`, plus the two later merges to `main` marked below. 665 rows.

Claude Code, Codex and Antigravity are trademarks of their owners; xlsft\`s harness isn't affiliated with them.

## Legend

| Marker | Meaning |
|---|---|
| `Yes` | Present. For xlsft\`s harness: code exists on this branch. For others: official docs describe it. Notes in brackets name the tool, flag or limit. |
| `Partial: ...` | Present with the stated gap. |
| `No` | For xlsft\`s harness: no code that does this. For others: official docs say it is absent or removed, or the docs' own complete list leaves it out (hook events, surfaces). |
| `Planned` | xlsft\`s harness only: `docs/PLAN.md` names it and there is no working code. |
| `In development` | xlsft\`s harness only: built, never run for real. Covers macOS computer use, import on a real profile, and the release and update flow (commit `485f9c4` on `main`). |
| `Landing` | xlsft\`s harness only: the OS sandbox. Written on branch `worktree-agent-aac49f305a6be96df`, merged to `main` as `2e4479f` after this doc's base commit. |
| `?` | Could not confirm in official sources. Not a claim that it is missing. |
| `n/a` | Doesn't apply to that product's model. |
| `beta`, `preview`, `experimental` | The vendor's own label. |

Plan names in brackets are the lowest tier the vendor lists. Codex rows cover all its surfaces unless a surface is named. Research read official pages selectively, so a `?` often means "not found in what we read".

## Surfaces and platforms

| Capability | xlsft\`s harness | Claude Code | Codex | Antigravity |
|---|---|---|---|---|
| Terminal UI | Partial: 10 slash commands, low priority | Yes | Yes | Yes (`agy`) |
| Headless binary | Yes (`harness-client -p`) | Yes (`claude -p`) | Yes (`codex exec`) | Yes (`agy -p`) |
| Desktop app, macOS | Yes (Tauri) | Yes | Yes (ChatGPT desktop app) | Yes |
| Desktop app, Windows | Partial: bundle config only, no release | Yes (x64, ARM64) | Yes | Yes (x64, ARM64) |
| Desktop app, Linux | Partial: bundle config only, no release | Yes, beta | Yes, preview | Yes (x64, ARM64) |
| Standalone code editor | No | No | No | Yes (Antigravity IDE) |
| Web client | Partial: run from source against a local client; server image doesn't serve it | Yes (claude.ai/code) | Yes (Codex Cloud) | Yes (antigravity.google.com) |
| Mobile native app | Planned | Yes (iOS, Android) | Yes (ChatGPT app; Android parity unclear) | ? |
| Mobile browser | Partial: phone-width layouts, needs a reachable client | ? | ? | Yes, installable to home screen |
| CLI on macOS | Yes | Yes (13+) | Yes | Yes |
| CLI on Linux | Partial: compiles in CI, no release | Yes (incl. Alpine) | Yes | Yes |
| CLI on Windows native | Partial: compiles in CI, no release | Yes | Yes | Yes |
| WSL | Partial: Linux build, no release | Yes (WSL 1, 2) | Yes (WSL2) | Yes (app connects to WSL) |
| Chromebook-class device | No | ? | ? | Yes (Googlebook) |
| Chat app surface (Slack etc.) | No | Yes (Slack) | Yes (Slack, Teams) | ? |
| Issue tracker surface | No | ? | Yes (Linear) | ? |
| Separate knowledge-work mode | No | Yes (Cowork) | Yes (Work mode) | ? |
| Deep links into the app | Partial: web `?session=<id>` | Yes (`claude-cli://`) | Yes | Yes (`antigravity://`) |
| Multi-agent inbox across sessions | Partial: sidebar and session tree | Yes (`claude agents`) | Yes (`codex agents`) | Yes (App, CLI `/agents`) |
| Project coordinator over many cloud sessions | No | Yes, beta (Projects) | ? | ? |
| Message from phone that spawns a desktop session | No | Yes (Dispatch) | ? | ? |

## Install and updates

| Capability | xlsft\`s harness | Claude Code | Codex | Antigravity |
|---|---|---|---|---|
| Tagged release exists today | No (flow on `main`, no tag cut) | Yes | Yes | Yes |
| Install script | No | Yes | Yes | Yes |
| npm package | No | Yes | Yes | ? |
| Homebrew | No | Yes | Yes | ? |
| WinGet or Windows Store | No | Yes (WinGet) | Yes | ? |
| apt, dnf, apk repositories | No | Yes | Partial: `.deb`, `.rpm` for the app | Partial: 1.x only, not updated |
| macOS DMG | In development: release flow built, on `main` | Yes | Yes | Yes |
| GitHub Release binaries | In development: macOS only, on `main` | ? | Yes | ? |
| Server container image | Partial: builds in CI; publish on tag not run | n/a (gateway in binary) | n/a | n/a |
| CLI self-update command | In development: `harness-client update`, on `main` | Yes (`claude update`) | Yes (`codex update`) | Yes (`agy update`) |
| Desktop auto-update | In development: Tauri updater, on `main` | ? | Yes | Yes |
| Update channels | Partial: pre-releases skip latest | Yes (latest, stable) | ? | ? |
| Version pin or floor by policy | No | Yes (min, max) | Yes (`features.in_app_updates`) | ? |
| Signed releases | In development: signed checksums, on `main` | Yes (GPG manifest, notarized) | Yes (sigstore) | ? |
| SBOM and provenance | In development: on `main` | ? | ? | ? |
| Health check command | No | Yes (`claude doctor`) | Yes (`codex doctor`) | ? |
| Shell completions | No | ? | Yes | ? |
| Dev container guide | No | Yes | ? | ? |
| Enterprise offline packaging | No | Yes (MDM templates) | Yes (Intune package) | ? |
| Import from another tool | In development: Claude Code only (never run on a real profile) | Yes (Codex, Gemini, Cursor) | Yes (Claude Code, Cursor) | Yes (Gemini CLI, Claude) |

## Models and providers

| Capability | xlsft\`s harness | Claude Code | Codex | Antigravity |
|---|---|---|---|---|
| Anthropic API | Yes | Yes | ? | Partial: Claude via plan; BYOK ? |
| OpenAI Responses API | Yes | No | Yes (only wire protocol) | ? |
| OpenAI Chat Completions API | Yes | No | No (not supported) | ? |
| Any OpenAI-compatible endpoint | Yes | No (non-Claude unsupported) | Partial: must speak Responses | Partial: SDK only |
| Any Anthropic-compatible endpoint | Yes | Yes (gateway format) | ? | ? |
| Google Gemini | Yes (preset) | No | ? | Yes |
| xAI | Yes (preset) | No | ? | ? |
| DeepSeek | Yes (preset) | No | ? | ? |
| Kimi | Yes (presets) | No | ? | ? |
| Z.ai | Yes (presets) | No | ? | ? |
| OpenRouter | Yes (preset) | No | ? | ? |
| Amazon Bedrock | Partial: custom endpoint only, no AWS auth | Yes | Yes | ? |
| Google Vertex or Agent Platform | Partial: Gemini preset only, no GCP auth | Yes | Yes | Partial: SDK `vertex=True`, Cloud project |
| Microsoft Foundry or Azure | Partial: custom endpoint only | Yes | Yes (example config) | ? |
| Provider preset list served without a release | Yes | n/a | n/a | n/a |
| Personal provider per user | Yes | n/a | Yes (local config) | n/a |
| Provider keys stay on the server | Yes | Partial: with gateway | Partial: with gateway | ? |
| Model discovery from provider | Yes | n/a | ? | ? |
| Per-model prices editable | Yes (admin, catalog refresh) | Yes (`modelPricing`) | n/a | n/a |
| Capability negotiation per model | Yes | n/a | n/a | n/a |
| Model slots (main, small, plan, title, classifier) | Yes | Partial: aliases, `opusplan` | Partial: separate keys | Partial: picker |
| Per-model prompt overrides | Yes (`gpt-*` system) | n/a | ? | ? |
| Local: Ollama | Yes (incl. pull, delete) | ? | Yes | Partial: SDK only |
| Local: LM Studio | Yes | ? | Yes | Partial: SDK only |
| Local: Jan | Yes | ? | ? | ? |
| Local: MLX | Yes | ? | ? | ? |
| Local: vLLM or other | Yes (custom local provider) | ? | Partial: `/v1` provider | Partial: SDK only |
| Local model policy by admin | Yes | n/a | Partial: requirements | n/a |
| Use a subscription login in the agent | Partial: via ACP agents; Kimi, Z.ai plan presets | Yes | Yes | Yes |
| Own gateway product | Yes (the server) | Yes (`claude gateway`) | ? | ? |
| ACP client (drive external agents) | Yes (registry, install, sign-in) | ? | ? | ? |
| ACP server (be driven by editors) | No | ? | ? | Yes (`agy acp`) |
| ACP agent limits shown | Partial: Claude agent only | n/a | n/a | n/a |
| Fallback model on failure | Yes (per model chain, overload only) | Yes (up to 3) | ? | ? |
| Retry with backoff | Yes | Yes | Yes | ? |
| Effort levels | Yes (low to max, per model) | Yes (low to max, auto, ultracode) | Yes (low to ultra) | Yes (low to max) |
| Effort per subagent | No | Yes | Yes | ? |
| Thinking display | Yes | Yes | Yes (summaries) | ? |
| Fast mode, faster tier | No | Yes (Opus; credits) | Yes (`/fast`, Ultrafast) | ? ("Fast" entry unexplained) |
| Second-model advisor | No | Yes, experimental | ? | ? |
| Switch model mid-session | Yes | Yes | Yes | Yes (after the turn) |
| Model allowlist by admin | Partial: per model by role | Yes | Yes (Agent Security) | ? |
| Image generation model | No | ? | Yes (GPT-Image-2) | Yes (Nano Banana 2) |
| Usage hold and auto-continue at reset | Yes | ? | ? | ? |

## Pricing, licensing, open source

| Capability | xlsft\`s harness | Claude Code | Codex | Antigravity |
|---|---|---|---|---|
| License of the agent | Yes (MIT) | Proprietary terms | Yes (Apache-2.0 CLI, SDK) | ? (SDK Apache-2.0) |
| Full source available | Yes | ? (public repo; license not confirmed) | Partial: IDE ext and cloud closed | ? |
| External contributions | Yes (`CONTRIBUTING.md`) | ? | No (issues only) | ? |
| Free tier | n/a (no fee; model costs yours) | No | Yes (limited, rollout) | Yes (weekly limits) |
| Entry paid plan | n/a | $20 (Pro) | $20 (Plus), $8 (Go) | $20 (Google AI Pro) |
| Higher plans | n/a | $100, $200 | $100, $200, $500 | $100, $200 |
| Team plan | n/a | $20-125 per seat | $20-25 per user | From $30 per seat |
| Enterprise plan | n/a | Yes | Yes | Yes |
| Pay per token | Yes (your providers) | Yes (API) | Yes (API) | Yes (Cloud project) |
| Buy extra credits | n/a | Yes | Yes | Yes (Pro, Ultra) |
| Server or seat fee | None | Per plan | Per plan | Per plan |
| Program for OSS maintainers | n/a | ? | Yes | ? |

## Agent loop and context

| Capability | xlsft\`s harness | Claude Code | Codex | Antigravity |
|---|---|---|---|---|
| Auto compaction | Yes (92% default, env override) | Yes | Yes | Yes |
| Manual compaction | Yes (`/compact` with focus) | Yes | Yes | Yes (App) |
| Idle compaction | Yes (per-user switch) | Yes | ? | ? |
| Trim old tool results | Yes | ? | ? | ? |
| Prompt caching | Yes (breakpoints, stable prefix) | Yes | Yes | Partial: reported only |
| Cache TTL controls | No | Yes | ? | ? |
| Continue last session | Yes (`-c`) | Yes | Yes | Yes |
| Resume by id | Yes | Yes | Yes | Yes |
| Session picker | Yes (UI) | Yes | Yes | Yes |
| Fork session | Yes | Yes | Yes | Yes (CLI) |
| Fork at a message | Yes (UI) | Yes | Yes (Esc Esc) | ? |
| Rewind conversation | Yes | Yes | Partial: drops turns, not files | Yes |
| Rewind code | Yes | Yes | ? (docs suggest own Git checkpoints) | ? (implied in App) |
| Summarize from a point | Yes | Yes | ? | ? |
| Automatic file checkpoints | Yes | Yes (file tools only) | ? (docs suggest own Git checkpoints) | Partial: internal, details ? |
| Context usage view | Yes (breakdown pane) | Yes (`/context`) | Yes (`/status`) | Yes (`/context`) |
| Message queue while running | Yes | Yes | Yes | Yes |
| Steer mid-turn | Yes (send now) | ? | Yes | Yes |
| Side question outside history | Yes (side chat) | Yes (`/btw`) | Yes (`/side`) | Yes (`/btw`) |
| Goal mode until condition | No | Yes (`/goal`) | Yes (`/goal`) | Yes (`/goal`) |
| Recap on return | Partial: `WrapUp` box at turn end | Yes (`/recap`) | Yes (`/recap`) | ? |
| AI session titles | Yes | ? | ? | Yes |
| Rename, pin, archive, delete sessions | Yes | Partial: rename, purge | Yes | Yes (archive, rename) |
| Session transcript export | No | Yes (`/export`) | Yes (`/export`) | Partial: JSONL on disk |
| Transcript search | Partial: titles, not full text | ? | ? | Yes (App full text) |
| Interrupt with short model reply | Yes | ? | ? | ? |
| Keep machine awake while working | Yes (tool and switch) | ? | ? | Yes |
| Chapters in the transcript | Yes (`MarkChapter`) | ? | ? | Yes (App navigator) |
| Turn-end box with buttons | Yes (`WrapUp`) | ? | ? | ? |
| Prompts served from the server, no release | Yes | ? | ? | ? |

## Built-in tools: files and search

| Capability | xlsft\`s harness | Claude Code | Codex | Antigravity |
|---|---|---|---|---|
| Read file | Yes (`Read`) | Yes | Partial: name not published | Yes (`view_file`) |
| Write file | Yes (`Write`) | Yes | Yes (`apply_patch`) | Yes (`write_to_file`) |
| Edit by string replace | Yes (`Edit`) | Yes | n/a (patch) | Yes (`replace_file_content`) |
| Multi-chunk edit | No | ? | Yes (patch) | Yes (`multi_replace_file_content`) |
| Glob | Yes | Partial: optional on Unix | ? | Yes (`find_by_name`) |
| Grep | Yes (ripgrep, Go fallback) | Partial: optional on Unix | ? | Yes (`grep_search`) |
| List directory | Partial: via Bash, `Glob` | Partial: via Bash | ? | Yes (`list_dir`) |
| Notebook edit | Yes (`NotebookEdit`) | Yes | ? | Yes (documented, name ?) |
| LSP tool | Yes (definition, references, hover, symbols) | Yes (plugin) | ? | ? |
| Errors reported after edits | Yes (LSP) | Yes (plugin) | ? | Partial: lint id argument |
| LSP servers built in | Partial: Go, TS, Python, Rust specs; binary on PATH | Partial: plugins | ? | ? |
| Custom LSP server config | Yes (`lspServers`, settings UI) | Yes (plugin) | ? | ? |

## Shell and background tasks

| Capability | xlsft\`s harness | Claude Code | Codex | Antigravity |
|---|---|---|---|---|
| Bash tool | Yes | Yes | Yes (`shell`, `exec_command`) | Yes (`run_command`) |
| PowerShell tool | No | Yes | Partial: shell on Windows | ? |
| Persistent cwd | Yes | Yes | ? | ? |
| Env snapshot from login shell | Yes | Partial: `CLAUDE_ENV_FILE` | Yes (`shell_snapshot`) | ? |
| Env filter for commands | Partial: environment file per folder | ? | Yes (`shell_environment_policy`) | ? |
| Timeouts configurable | Yes (env) | Yes (env) | ? | Partial: `WaitMsBeforeAsync` |
| Run in background | Yes | Yes | Yes | Yes |
| Auto-background on timeout | Yes | Yes | ? | ? |
| Move a running call to background | Yes (`BackgroundToolCall`) | Yes (Ctrl+B) | ? | ? |
| Task output tool | Yes (`TaskOutput`) | No (removed) | Yes (`write_stdin`, unified exec) | Yes (`manage_task`) |
| Stop task | Yes (`TaskStop`) | Yes | Yes (`/stop`) | Yes |
| Interactive stdin to a command | Partial: `TerminalRun` in a PTY tab | ? | Yes (`write_stdin`) | Yes (`send_input`) |
| Monitor tool (line stream) | Yes (`Monitor`) | Yes | ? | ? |
| Wait tool | Yes (`Wait`) | Partial: `ScheduleWakeup` | Yes (`wait_agent`) | ? |
| User shell from composer | Yes (`!`) | Yes (`!`) | Yes (`!`) | Yes (`!`) |
| Terminal tools for the agent | Yes (`Terminal*`, 5 tools) | ? | Yes (reads integrated terminal) | Yes (`RequestedTerminalID`) |
| Managed sidecar processes | Partial: `Preview*` dev servers | Yes (`.claude/launch.json`) | Yes (Local env actions) | Yes (Sidecars) |
| Memory cap per tool | No | Yes (Linux) | ? | ? |

## Web, search, browser

| Capability | xlsft\`s harness | Claude Code | Codex | Antigravity |
|---|---|---|---|---|
| Web search tool | Yes | Yes (Anthropic backend) | Yes (hosted) | Yes (`search_web`) |
| Self-hosted search bundled | Yes (SearXNG in image) | ? | ? | ? |
| Search providers: Brave, Tavily, Exa, Kagi, Perplexity | Yes (all five) | n/a (hosted) | n/a (hosted) | ? |
| Per-user search provider | Yes | n/a | n/a | n/a |
| Domain filters on search | No | Yes | Partial: modes | ? |
| Cached, live, disabled modes | No | ? | Yes | ? |
| URL fetch tool | Yes (`WebFetch`, summarized) | Yes | ? | Yes (`read_url_content`) |
| Private-network guard on fetch | Yes (netguard) | ? | Yes (proxy, private IP block) | ? |
| Agent browser tools | Yes (12 `Browser*` tools) | Yes (Chrome) | Yes (app, extension) | Yes (browser subagent) |
| Own isolated browser profile | Yes (per session) | ? | Yes | Yes (App built-in) |
| Drive the user's real signed-in browser | Planned (extension) | Yes (extension) | Yes (extension) | Yes (remote debugging) |
| Browser extension | Planned | Yes | Yes (5 browsers) | ? |
| In-app browser pane | Yes (screencast, input) | Yes (Desktop) | Yes (app) | Yes (App) |
| Take over from agent in the browser | Yes | ? | ? | ? |
| Agent activity outline in the browser | Yes | ? | ? | ? |
| Viewport presets | Yes | ? | ? | ? |
| Console and network inspection | Yes | Yes | ? | Yes |
| JS eval in page | Yes (`BrowserEval`) | ? | ? | Yes (policy) |
| Screenshots | Yes | Yes | ? | Yes |
| Recording of browser session | No | Yes (GIF) | Partial: Record & Replay is desktop-level | Yes (WebM) |
| Browser downloaded on demand | Yes (Chrome for Testing) | ? | n/a | ? |
| Comments on web pages | No | ? | Yes | Yes |
| Allow and deny URL lists | Yes (`Browser(domain:...)`, `WebFetch(domain:...)`) | Yes | Yes (admin) | Yes |
| Dev server preview tools | Yes (`Preview*`, `.harness/launch.json`) | Yes | ? | ? |
| Public tunnel to preview | Planned | ? | ? | ? |
| Code search index or panel | Partial: Files pane filter, ⌘K | ? | ? | Yes (`/codesearch`) |
| Deep research workflow | No | Yes (`/deep-research`) | ? | ? |

## Computer use

| Capability | xlsft\`s harness | Claude Code | Codex | Antigravity |
|---|---|---|---|---|
| Computer use, macOS | In development: driver, helper, 12 tools | Yes, preview | Yes (region-limited) | ? |
| Computer use, Windows | No (stub) | Yes (Desktop) | Yes (foreground) | ? |
| Computer use, Linux | No (stub) | No (CLI); ? (Desktop) | ? | ? |
| Available from CLI | Partial: via client tools; not run | Yes (macOS) | No (desktop only) | ? |
| Team and Enterprise plans | n/a | No | Yes, region-limited | ? |
| Per-app access approval | In development | Yes | Yes | ? |
| App class tiers (browser, terminal) | In development: Full, Standard, Strict profiles | Yes | ? | ? |
| Risky app classes flagged | In development | ? | ? | ? |
| Accessibility tree lookup | In development (`ComputerFind`) | ? | Yes (Computer History, Appshots) | ? |
| Menu control | In development (`ComputerMenu`) | ? | ? | ? |
| Stop control | In development | ? | ? | ? |
| Screenshots kept off the server | In development: scrubbed without object store | ? | ? | ? |
| Record a demo into a skill | No | ? | Yes (macOS) | ? |
| Mobile simulator pane | No | Yes (iOS, Desktop, beta) | ? | ? |

## Subagents and teams

| Capability | xlsft\`s harness | Claude Code | Codex | Antigravity |
|---|---|---|---|---|
| Subagent tool | Yes (`Agent`) | Yes | Yes (`spawn_agent`) | Yes (`invoke_subagent`) |
| Built-in: general, explore, plan | Yes (3) | Yes (6) | Partial: default, worker, explorer | Partial: research, self, browser |
| Custom agents from Markdown | Yes (`.claude/agents`, `.harness/agents`) | Yes | No (TOML) | Yes (`.agents/agents`) |
| Custom agent fields | Partial: name, description, model, tools, read_only | Yes (19 fields) | Partial: model, effort, skills | Yes (tools, model, MCP, skills) |
| Agents per run with own permissions | No (inherits) | Yes (`permissionMode`) | No (inherits) | Yes (`commandExecutionPolicy`) |
| Agents with own MCP servers | No | Yes | No | Yes |
| Agent per-model override | Yes | Yes | Yes | Yes |
| Background agents | Yes (always) | Yes | Yes | Yes |
| Agent stays alive after a turn interrupt | Yes | ? | ? | Yes (idle wakes) |
| Message to a running agent | Yes (`SendMessage`) | Yes | Yes (`send_input`) | Yes (`send_message`) |
| Read an agent's transcript | Yes (pane) | Yes | Yes (`/subagents`) | Yes |
| Transient agent defined on the fly | No | Yes (`--agents`) | ? | Yes (`define_subagent`) |
| Depth limit | Yes (default 2, setting) | Yes | ? | ? |
| Concurrency cap | Yes (default 8, setting) | Yes (20) | Yes | ? |
| Forked subagent with full history | No | Yes (`/subtask`) | ? | No (clean context) |
| Agent teams with shared task list | No | Yes, experimental | ? | Partial: `/boost`, `/teamwork` (paid) |
| Peer agent messaging | No | Yes (teams) | ? | Yes |
| Other sessions as agents | Yes (`Session*`, 10 tools) | Yes (cross-session) | Partial: `codex queue` | ? |
| Agent proposes a session for the user | Yes (`/proposals`, cards) | ? | ? | ? |
| Hand off task to a fork | Yes (`SessionHandoff`) | ? | Yes (worktree Handoff) | ? |
| Subagent output scanning | No | Yes | ? | ? |

## Worktrees

| Capability | xlsft\`s harness | Claude Code | Codex | Antigravity |
|---|---|---|---|---|
| Session in a new worktree | Yes (UI, RPC) | Yes (`-w`) | Yes (`--worktree`) | Yes (App) |
| Enter and exit from the agent | Yes (tools) | Yes (tools) | Yes (`/worktree`) | ? |
| Subagent isolation in a worktree | Yes | Yes | ? | Yes |
| Base branch choice | Yes | Yes | ? | Yes |
| Branch from a PR | No | Yes (`--from-pr`) | ? | ? |
| Copy gitignored files | No | Yes | Yes (`.worktreeinclude`) | ? |
| Sync with base branch tool | Yes (`SyncWithBase`) | ? | ? | ? |
| Propose a branch tool | Yes (`ProposeBranch`) | ? | ? | ? |
| Auto cleanup of worktrees | No (never removed) | Yes | ? | Yes (when subagent killed) |
| Non-git VCS | No | Yes (hooks) | ? | Partial: `/diff` Hg, Jujutsu |
| Session moves folder while running | Yes (`ChangeDirectory`, `RequestDirectory`) | Yes (`/cd`) | Yes (`/cd`) | ? |
| Environment per folder (env, dirs, defaults) | Yes (encrypted `.env`) | Yes (Desktop) | Yes (Local env) | ? |

## Workflows and orchestration

| Capability | xlsft\`s harness | Claude Code | Codex | Antigravity |
|---|---|---|---|---|
| Scripted multi-agent workflows | Yes (`Workflow`, JS in goja) | Yes (`Workflow`) | ? | ? (Markdown workflows, legacy) |
| `parallel()`, `pipeline()` | Yes | Yes | n/a | n/a |
| Resume a workflow run | Yes (`resume_from`) | Yes | n/a | n/a |
| Agent cap per workflow | Yes (default 30, setting) | Yes (16 concurrent) | n/a | n/a |
| Workflow view in UI | Yes (Tasks pane) | Yes (`/workflows`) | n/a | n/a |
| Save workflow as a command | No | Yes | Partial: `/presets` | Yes (`/workflow-name`) |
| Automatic orchestration mode | No | Yes (ultracode) | Yes (Ultra) | Yes (`/boost`) |
| Deep review or research presets | No | Yes | Partial: Security Review | Partial: `/teamwork` |
| Loop on interval | No | Yes (`/loop`) | ? | Yes (`/schedule`) |
| Interview mode | Partial: `AskUserQuestion` | ? | ? | Yes (`/grill-me`) |

## Plan mode

| Capability | xlsft\`s harness | Claude Code | Codex | Antigravity |
|---|---|---|---|---|
| Plan mode | Yes | Yes | Yes | Yes |
| Enter from the agent | Yes (`EnterPlanMode`) | Yes | ? | ? |
| Plan approval card | Yes (accept edits, ask, keep planning) | Yes | ? | Yes |
| Feedback on the plan | Yes (typed) | Yes | ? | Yes (inline comments) |
| Separate model for plan | Yes (`plan` slot) | Yes (`opusplan`) | Yes (effort key) | ? |
| Plan as an artifact file | No | ? | ? | Yes (Implementation Plan) |
| Read-only commands in plan | Yes | Yes | ? | Yes |

## Tasks and todo

| Capability | xlsft\`s harness | Claude Code | Codex | Antigravity |
|---|---|---|---|---|
| Todo tool | Yes (`TodoWrite`) | Yes (legacy) | Yes (`update_plan`) | ? |
| Task graph with dependencies | No | Yes (`Task*`) | ? | Partial: task groups, IDE |
| Todo shown in UI | Yes (Tasks pane, TUI) | Yes (Ctrl+T) | ? | Yes |
| Nudge when todo goes stale | Yes (`todo_nudge`) | ? | ? | ? |
| Walkthrough artifact at end | No | ? | ? | Yes |
| External task tracker | No (UI "soon") | ? | Yes (Linear) | ? |

## Memory and instructions

| Capability | xlsft\`s harness | Claude Code | Codex | Antigravity |
|---|---|---|---|---|
| `CLAUDE.md` | Yes | Yes | ? | ? |
| `AGENTS.md` | Yes (fallback) | Yes (fallback) | Yes | Yes |
| `GEMINI.md` | No | ? | ? | Yes |
| Managed instructions file | Yes | Yes | Yes (`additional_developer_instructions`) | ? |
| User-level instructions | Yes (synced from server) | Yes | Yes | Yes |
| Local untracked file | Yes (`CLAUDE.local.md`) | Yes | Yes (`AGENTS.override.md`) | ? |
| Parent directories loaded | Yes | Yes | Yes | Yes (to repo root) |
| Subdirectory files on demand | Yes | Yes | ? | No |
| `@path` imports | Yes | Yes | ? | Yes |
| Path-scoped rules | Yes (`.claude/rules`) | Yes | ? | Yes (glob trigger) |
| Rule triggers beyond path | No | ? | ? | Yes (model decision, manual) |
| Auto memory across sessions | Partial: reads `MEMORY.md` index; nothing writes it | Yes | Yes (off by default) | Partial: `/learn`, knowledge items ? |
| Memory synced across machines | Partial: user memory yes, auto memory local | ? | ? | ? |
| `/init` generates instructions | No | Yes | Yes | ? |
| Instructions mode setting | Yes (`claudeMdMode`) | Partial: `claudeMdExcludes` | Yes (size, fallbacks) | Yes (`rules.json`) |
| Output styles | Yes (5 built in, custom) | Yes (5 built in, custom) | ? | ? |
| Knowledge base | No (UI "soon") | ? | ? | ? |
| Reads `.claude/` project files | Yes | Yes | Partial: plugin manifests | Partial: import |

## Skills

| Capability | xlsft\`s harness | Claude Code | Codex | Antigravity |
|---|---|---|---|---|
| Skill loader and tool | No (imported docs not used; Planned) | Yes (`Skill`) | Yes | Yes |
| `SKILL.md` format | Planned | Yes | Yes | Yes |
| Project and user skill folders | Planned | Yes | Yes (`.agents/skills`) | Yes (`.agents/skills`) |
| Admin-distributed skills | Planned | Yes | Yes | ? |
| Slash invocation | Planned | Yes | Yes (`$`, `/skills`) | Yes |
| Model-invoked by description | Planned | Yes | Yes | Yes |
| Bundled skills | Planned | Yes (20+) | Yes (`openai/skills`) | Yes (Build with Google) |
| Skill diagnostics | No | Yes (`/skill-doctor`) | ? | ? |
| Skill reload command | No | Yes | ? | Yes |
| Custom slash commands | Planned (composer list is static) | Yes | Partial: `/presets`, prompts deprecated | Yes (workflows, legacy) |
| Import of skills from Claude Code | In development: stored, not used | n/a | ? | Yes (`agy plugin import`) |

## Plugins and marketplaces

| Capability | xlsft\`s harness | Claude Code | Codex | Antigravity |
|---|---|---|---|---|
| Plugin format | No (Planned, M4) | Yes | Yes | Yes |
| Official marketplace | No (Planned, M4) | Yes | Yes | Yes |
| Private marketplaces | No | Yes | Yes | ? |
| Install from Git or local | No | Yes | Yes | Yes |
| Plugin CLI commands | No | Yes (`claude plugin`) | Yes (`codex plugin`) | Yes (`agy plugin`) |
| Plugin scaffolding helper | No | Yes (`/plugin-authoring`) | Yes (`$plugin-creator`) | Yes (validate) |
| Plugin evals | No | Yes, early access | ? | ? |
| Plugins add UI panels | No | Yes (mods) | Yes (Plugin Extensions) | Yes (side panel) |
| Plugin auto-update | No | Yes | Yes (`upgrade`) | ? |
| Admin restrictions on sources | No | Yes | Yes | ? |
| Themes via plugin | No | Yes | ? | ? |
| In-process UI mods | No | Yes (mods) | ? | ? |

## Hooks

| Capability | xlsft\`s harness | Claude Code | Codex | Antigravity |
|---|---|---|---|---|
| Hook runtime | Planned (imported and detected, never run) | Yes | Yes | Yes |
| Project hook trust gate | Partial: detection only | Yes | Yes (hash review) | ? |
| Event SessionStart | No | Yes | Yes | Yes |
| Event SessionEnd | No | Yes | Yes | SDK only |
| Event Setup | No | Yes | No | No |
| Event UserPromptSubmit | No | Yes | Yes | Partial: `PreInvocation` |
| Event UserPromptExpansion | No | Yes | No | No |
| Event PreToolUse | No | Yes | Yes | Yes |
| Event PostToolUse | No | Yes | Yes | Yes |
| Event PostToolUseFailure | No | Yes | No | SDK only |
| Event PostToolBatch | No | Yes | No | No |
| Event PermissionRequest | No | Yes | Yes | No |
| Event PermissionDenied | No | Yes | No | No |
| Event Notification | No | Yes | No | No |
| Event MessageDisplay | No | Yes | No | No |
| Event SubagentStart | No | Yes | Yes | No |
| Event SubagentStop | No | Yes | Yes | No |
| Event TaskCreated, TaskCompleted | No | Yes | No | No |
| Event Stop | No | Yes | Yes | Yes |
| Event StopFailure | No | Yes | No | No |
| Event TeammateIdle | No | Yes | No | No |
| Event InstructionsLoaded | No | Yes | No | No |
| Event ConfigChange | No | Yes | No | No |
| Event CwdChanged, DirectoryAdded | No | Yes | No | No |
| Event FileChanged | No | Yes | No | No |
| Event WorktreeCreate, WorktreeRemove | No | Yes | No | No |
| Event PreCompact, PostCompact | No | Yes | Yes | SDK only |
| Event PreModelSwitch, PostModelSwitch | No | Yes | No | No |
| Event PreModelRequest, PostModelRequest | No | No | Yes | No |
| Event Elicitation, ElicitationResult | No | Yes | No | No |
| Event Interrupt | No | No | Yes | No |
| Event PostInvocation | No | No | No | Yes |
| Handler: command | Planned | Yes | Yes | Yes |
| Handler: HTTP | Planned | Yes | No | No |
| Handler: MCP tool | No | Yes | Yes | No |
| Handler: prompt | No | Yes | No (parsed, skipped) | ? (docs conflict) |
| Handler: agent | No | Yes, experimental | No (parsed, skipped) | No |
| Rewrite tool input | No | Yes | Yes | Yes |
| Managed-only hooks policy | No | Yes | Yes | ? |
| Hooks in skill or agent frontmatter | No | Yes | ? | ? |

## MCP

| Capability | xlsft\`s harness | Claude Code | Codex | Antigravity |
|---|---|---|---|---|
| MCP client | Yes | Yes | Yes | Yes |
| Transport: stdio | Yes (per-device approval) | Yes | Yes | Yes |
| Transport: Streamable HTTP | Yes (through server proxy) | Yes | Yes | Yes |
| Transport: SSE | Yes (through server proxy) | Yes (deprecated) | ? | No (removed in SDK) |
| Transport: WebSocket | Yes (through server proxy) | Yes | ? | ? |
| Server-side registry (connect once, all devices) | Yes | Partial: claude.ai connectors | Partial: ChatGPT session connectors | ? |
| Org-scope servers by role | Yes | Yes (managed) | Yes (requirements) | Partial: allowlist |
| Project `.mcp.json` | Yes | Yes | ? | Partial: `.agents/mcp_config.json` |
| OAuth flow | Yes (on the server: discovery, DCR, PKCE) | Yes | Yes (CIMD, DCR) | Yes (PKCE, CIMD, DCR) |
| Sign-in tool for the agent | Yes (`McpAuthenticate`) | ? | ? | ? |
| Secret references in config | Yes (`${secret:NAME}`) | Partial: env, `headersHelper` | Partial: env, helper | Partial: env, headers |
| Dynamic header helper | No | Yes | Yes | ? |
| Resources for the agent | Partial: server proxy RPCs, no agent tool | Yes (2 tools, `@`) | Partial: app-server RPC | ? |
| Prompts as commands | Partial: server proxy RPCs, no command | Yes | ? | ? |
| Elicitation | No (TODO in proto) | Yes (form, URL) | Yes (form, URL) | ? |
| Sampling | No | ? | ? | ? |
| Roots | No | ? | ? | ? |
| Tool search, deferred tools | Yes (`ToolSearch`, 10% default) | Yes | Yes | ? |
| Tool allow and deny per server | Partial: permission rules | Yes | Yes | Yes |
| Auto approval per tool | Yes (rules) | Yes (rules) | Yes (modes) | Yes (rules) |
| Tool timeouts | Partial: non-call requests (server env) | ? | Yes | Yes |
| Server instructions passed to model | No | ? | Yes | ? |
| Tool catalog cache | Yes | ? | ? | ? |
| MCP tasks | No | Yes, beta | ? | ? |
| MCP Apps UI | No | Partial: recognized | ? | ? |
| Agent exposed as an MCP server | No | Yes | No (removed) | ? |
| IDE MCP server | No | Yes | ? | ? |
| Channels inbound (Telegram etc.) | No | Yes, preview | ? | ? |
| MCP store or browser in UI | No | No | Partial: plugin directory | Yes (MCP Store) |
| MCP settings screen | No (import, RPC, files) | Yes (`/mcp`, Desktop) | Yes (`/mcp`) | Yes (`/mcp`, App) |
| Audit of MCP calls | Partial: written to DB, no read UI or route | ? | ? | ? |

## Permissions and modes

| Capability | xlsft\`s harness | Claude Code | Codex | Antigravity |
|---|---|---|---|---|
| Allow, ask, deny rules | Yes | Yes | Yes (`.rules`) | Yes |
| Rule syntax per tool | Yes (Claude Code shape) | Yes | No (prefix rules in Starlark-like) | Yes (`command`, `mcp(...)`) |
| Compound shell parsing | Yes | Yes | ? | Yes |
| Protected paths | Yes (`.git`, `.claude`, `.harness`...) | Yes | Yes | Yes (`~/.ssh`, `.env` in sandbox) |
| Mode: default ask | Yes | Yes | Yes (Ask for approval) | Yes (Request Review) |
| Mode: accept edits | Yes | Yes | ? | Yes (`accept-edits`) |
| Mode: plan | Yes | Yes | Yes | Yes |
| Mode: auto with classifier | Yes | Yes | Yes (Approve for me) | Yes (macOS, Linux, Windows; personal only in CLI) |
| Mode: dontAsk | Yes | Yes | ? | ? |
| Mode: bypass | Yes | Yes | Yes (Full access, `--yolo`) | Yes (Turbo, `--dangerously-skip-permissions`) |
| Shift+Tab cycle | Yes | Yes | ? | Yes |
| Classifier: local rules first | Yes (Go port of server rules) | ? | ? | ? |
| Classifier: choice of model | Yes (any, user or admin) | ? | ? | ? |
| Classifier: external HTTP service | Yes (`decision-http`) | ? | ? | ? |
| Classifier decision shown and recorded | Yes (notice, transcript) | ? | Yes (rationale) | ? |
| Classifier decision log for admin | Partial: JSON route only | ? | ? | ? |
| Mode policy by admin | Yes (server-wide disable) | Yes | Yes | Partial |
| Bypass refused from remote | Yes | Partial: not on mobile | ? | ? |
| Workspace trust | Yes | Yes | Yes (`trust_level`) | Yes |
| Per-app rules for computer use | In development | Yes | Yes | ? |
| Allow-always persistence | Yes | Yes | Yes (Smart approvals) | Yes |
| Editable scope in prompt card | No | ? | ? | Yes |
| Block reads outside working dirs | Partial: rules | Yes (setting) | Yes (deny-read paths) | Yes |
| Named permission profiles | No | ? | Yes | Partial: App presets |
| Prompt suggestions to reduce asks | No | Yes (`/fewer-permission-prompts`) | Yes (Smart approvals) | ? |
| Test a rule from CLI | No | ? | Yes (`codex execpolicy`) | ? |
| Headless deny with reasons | Yes (`permission_denials`) | ? | ? | ? |

## Sandbox

| Capability | xlsft\`s harness | Claude Code | Codex | Antigravity |
|---|---|---|---|---|
| OS sandbox for shell | Landing: on by default | Yes | Yes | Yes (App on; CLI, IDE off) |
| macOS | Landing: Seatbelt | Yes: Seatbelt | Yes: Seatbelt | Yes: Seatbelt |
| Linux | No (stub, shows unavailable) | Yes: bubblewrap, seccomp | Yes: bubblewrap, seccomp | Yes: namespaces |
| Windows native | No (stub) | No | Yes: `mxc`, elevated, unelevated | Yes: AppContainer (recent build) |
| WSL 2 | No | Yes | Yes | ? |
| Network off by default with allowlist | Landing: proxy, built-in host list | Yes (proxy, allowlist) | Yes (proxy, allow, deny) | Yes |
| Add hosts or write paths by setting | Landing | Yes | Yes | Yes |
| Credential stores closed for read | Landing | Yes (masking) | Partial: read-only `.aws`, deny-read | Yes (blocks `~/.ssh`, `.env`) |
| Git hooks and config protected | Landing | ? | Yes (`.git` read-only) | ? |
| Failure explained to the model | Landing | ? | Yes (`--log-denials`) | ? |
| Sandboxed command skips prompt (opt-in) | Landing: `autoAllowBash` | ? | ? | Yes (Default preset) |
| Escape needs a user-only prompt | Landing: `run_outside_sandbox` | Yes (`allowUnsandboxedCommands`) | Yes | Yes |
| Required by org policy | Landing | Yes (`failIfUnavailable`) | Yes | Partial: Strict mode |
| Applies to preview servers | Landing: own policy | ? | ? | ? |
| Settings UI | Landing | Yes (`/sandbox`) | Yes (`/permissions`) | Yes |
| Cloud VM isolation | No | Yes | Yes | Yes (Remote Agents) |
| Sandbox test command | No | ? | Yes (`codex sandbox`) | ? |

## Settings and managed policy

| Capability | xlsft\`s harness | Claude Code | Codex | Antigravity |
|---|---|---|---|---|
| Settings hierarchy | Yes (policy, CLI, local, project, user, org) | Yes | Yes | Partial: global, project, shared |
| User settings synced from server | Yes | Partial: server-managed, not per user | Partial: cloud defaults | ? |
| Read Claude Code settings files | Yes | Yes | ? | ? |
| Policy from central server | Yes (POLICY documents) | Yes (Team+) | Yes (Agent Security) | Partial: admin policies |
| Role-targeted policy | Yes | No (no per-group yet) | ? | ? |
| Policy via MDM | No | Yes | Yes | ? |
| Policy via system file | Partial: managed `CLAUDE.md` only | Yes | Yes (`requirements.toml`) | ? |
| Self-hosted config server | Yes (the server) | Yes (gateway) | Yes (`[config_server]`) | ? |
| Admin UI for policy | Partial: modes, local models, limits visibility | Yes | Yes | Partial |
| Agent changes settings with approval | Yes (`SettingsSet`) | Partial: `/update-config` skill | ? | ? |
| Config debug view | No | ? | Yes (`/debug-config`) | ? |
| Feature flags list | No | ? | Yes (`codex features`) | ? |
| Bare or safe startup | Partial: `HARNESS_NO_*` envs | Yes (`--bare`, `--safe-mode`) | Yes (`--ignore-user-config`) | ? |

## Auth, SSO, directory

| Capability | xlsft\`s harness | Claude Code | Codex | Antigravity |
|---|---|---|---|---|
| Password accounts | Yes (Better Auth) | n/a | Yes (email + password) | n/a |
| Device-flow login | Yes | ? | Yes (`--device-auth`) | Partial: manual code over SSH |
| Token refresh | Yes | Yes | Yes | Yes |
| Keychain credential store | Yes | ? | Yes | Yes |
| API key login | n/a (keys on server) | Yes | Yes | Yes |
| LDAP sign-in | Yes (StartTLS, group filter, admin group, test) | No (says unsupported) | ? | ? |
| Break-glass local login with LDAP | Yes | n/a | ? | ? |
| SAML SSO | No | Yes (Team+) | Yes (Business+) | Partial: Google Cloud SSO, WIF |
| OIDC SSO | No | Yes (gateway, Team+) | ? | Partial: WIF |
| Social login | No | ? | ? | Yes (Google) |
| 2FA, MFA | No | ? | Yes | ? |
| SCIM | No | Yes (Enterprise) | Yes (Enterprise) | ? |
| Roles | Partial: admin, user | Yes (custom, Enterprise) | Yes (custom, Enterprise) | Partial: Cloud IAM roles |
| Device list and revoke | Yes | Partial: Trusted Devices beta | ? | ? |
| Admin creates, disables, deletes users | Yes | n/a (via claude.ai) | n/a (via workspace) | n/a |
| Revoke a user's sessions | Yes | ? | ? | ? |
| Service accounts, access tokens | No (`HARNESS_ACCESS_TOKEN` fixed token) | Partial: `setup-token` | Yes | Partial: ADC |
| First-run admin bootstrap | Yes | n/a | n/a | n/a |
| Lock to organization | n/a (own server) | Yes | Yes | ? |

## Admin, quotas, audit, telemetry

| Capability | xlsft\`s harness | Claude Code | Codex | Antigravity |
|---|---|---|---|---|
| Spend limit per user | Partial: JSON API, no UI | Yes | Partial: not all usage | ? (vendor said "later this year" in Aug) |
| Spend limit per role or group | Partial: JSON API | Yes (group) | ? | ? |
| Per-session budget | Yes (server) | Partial: `--max-budget-usd` | ? | ? |
| Budget per provider | Yes (monthly, UI) | ? | ? | Yes (project-level) |
| Pooled budgets | No | Yes | ? | Yes |
| Rate-limit windows shown | Yes | Yes | Yes | Yes |
| Near-limit warnings | Yes (`UsageLimit`) | ? | ? | Yes |
| Visibility policy for limits | Yes (everyone, selected, admins) | ? | ? | ? |
| Object store quota per user | Yes | n/a | n/a | n/a |
| Usage report per user, admin | Yes | Yes | Yes | Yes (Cloud) |
| Usage by model and request kind | Yes | Yes | ? | ? |
| CSV export | No | Yes | ? | ? |
| Analytics API | Partial: admin JSON route | Yes (Enterprise) | Yes (Enterprise) | ? |
| PR attribution, adoption metrics | No | Yes | ? | ? |
| General audit log | No | Partial: settings changes, Compliance API | Yes (Compliance API) | Yes (toggle) |
| OpenTelemetry | No | Yes (metrics, logs, traces beta) | Yes (`[otel]`) | Partial: SDK only |
| Prometheus metrics | No | Yes | ? | ? |
| SIEM export | No | ? | Yes | ? |
| Product telemetry to vendor | No | Yes (off for clouds) | Yes (opt-out) | Yes (toggle) |
| Log streaming to user's collector | No | Yes | Yes | ? |
| Prompt management UI for admin | No (files in `HARNESS_PROMPTS_DIR`) | n/a | n/a | n/a |

## Self-hosting, residency, offline

| Capability | xlsft\`s harness | Claude Code | Codex | Antigravity |
|---|---|---|---|---|
| Self-hosted backend | Yes (one Docker image) | Partial: gateway, runners; cloud content goes to vendor | No (hosted cloud; own gateway OK) | ? |
| Bundled database | Yes (Postgres, or PGlite) | n/a | n/a | n/a |
| External Postgres | Yes | n/a | n/a | n/a |
| Object store | Yes (Garage or S3) | n/a | n/a | n/a |
| Backup guide | Yes | n/a | n/a | n/a |
| Data stays on your infrastructure | Yes, except chosen model APIs | Partial: with own cloud | Partial: local client, own gateway | ? |
| Data residency | Yes (your hosting) | Yes (API at 1.1x, cloud regions) | Yes (Enterprise, API) | Yes (global, us, eu) |
| Zero data retention | Depends on your providers | Yes (qualified Enterprise) | Yes (API org) | ? |
| HIPAA | n/a | Yes (Enterprise CLI, Desktop) | ? | ? |
| VPC service controls, private endpoints | n/a | ? | ? | Yes |
| mTLS, custom CA | Partial: proxy env only | Yes | ? | Yes (mTLS) |
| Fully air-gapped run with local models | Partial: local models possible; not tested; ACP registry and browser download need network | ? | ? | Partial: SDK local only |
| Secrets encrypted at rest | Yes (AES-256-GCM) | n/a | Yes (vault, EKM) | Yes (plugin OAuth) |
| Self-hosted cloud runners | No | Yes, beta | ? | ? |
| Works when server is briefly offline | Partial: prompts cache; models need the gateway | ? | ? | ? |
| Training on your data | No (no vendor) | Per plan terms | Per plan terms | Consumer yes, opt-out; Enterprise no |

## Session sync, remote, cloud, scheduling

| Capability | xlsft\`s harness | Claude Code | Codex | Antigravity |
|---|---|---|---|---|
| Sessions synced to server | Yes (transcripts, metadata) | ? | Partial: Work tasks | ? |
| Sessions of other machines listed | Yes | ? | ? | ? |
| Continue another machine's session here | Yes (fork its transcript) | Yes (`/teleport`) | Partial: `codex apply`, `/app` | ? |
| Remote control of a live session | Yes (relay) | Yes | Yes | Yes |
| Control from phone | Partial: browser only | Yes | Yes | Yes (mobile web) |
| Spawn a session on a remote host | Partial: `Options.Daemon`, no command | Yes | Yes | Yes (`agy remote-control`) |
| Remote hosts over SSH | No (menu says "soon") | Yes | Yes | Partial: SSH login flow, `mic-serve` |
| Remote approval of permissions | Yes | Yes | Yes | Yes |
| Push notifications to phone | No | Yes | Yes | Yes (browser push) |
| Desktop notifications | Yes | Yes | Yes | Yes |
| Agent-sent notification tool | Yes (`PushNotification`) | Yes | ? | ? |
| Cloud agents, hosted VMs | No | Yes | Yes | Yes (Remote Agents, Pro, Ultra) |
| Cloud environment setup scripts | No | Yes | Yes | ? |
| Cloud network policy | No | Yes (4 levels) | Yes | ? |
| Best-of-N attempts | No | ? | Yes (`--attempts`) | ? |
| Scheduled tasks, local | No (UI "soon") | Yes | Yes (app) | Yes (App, `/schedule`) |
| Scheduled tasks, cloud | No | Yes, preview | Yes | ? |
| Event triggers (GitHub, Slack, Gmail) | No | Yes (GitHub) | Yes | ? |
| HTTP trigger | No | Yes | ? | ? |
| Always-on agents | No | ? | Yes (Dots, rolling out) | Yes (Always on) |
| Self-paced wake-up tool | No | Yes (`ScheduleWakeup`) | ? | ? |
| Viewer presence | Yes | ? | ? | ? |
| Takeover by another device | Yes (`SessionSuperseded`) | ? | ? | ? |

## GitHub, GitLab, CI, headless, SDK

| Capability | xlsft\`s harness | Claude Code | Codex | Antigravity |
|---|---|---|---|---|
| GitHub App setup | Yes (manifest, one click) | Yes (`/install-github-app`) | Yes | ? |
| GitHub events reach the agent | Yes | Yes | Yes | ? |
| GitLab integration | Yes (incl. self-managed) | Yes, beta (CI) | Yes, beta | Partial: MCP only |
| GitHub Enterprise Server | Partial: connection takes a base URL; not tried on GHES | Yes | ? | ? |
| Local CLI based PR tracking (`gh`, `glab`) | Yes | ? | ? | ? |
| PR status on session | Yes | Yes | ? | ? |
| Create PR from UI | Partial: button not wired; agent uses `gh` | ? | Yes | ? |
| Merge from UI | Partial: `PrAutoMerge` tool, always asks; no button | Yes | ? | ? |
| Auto-fix on CI failure, comment, conflict | Yes (wakes session, rate-limited) | Yes | ? | ? |
| Auto-fix without a live session | Partial: jobs recorded, not delivered | Yes (cloud) | ? | ? |
| CI action | No | Yes | Yes | ? |
| `@mention` bot on PR or issue | No | Yes | Yes | ? |
| Automatic PR review bot | No | Yes (Team+) | Yes | ? |
| Local review command | No (no `/code-review`; `ReportFindings` tool only) | Yes | Yes (`codex review`) | ? |
| Typed findings UI | Yes (`FindingsCard`) | Yes | Yes | ? |
| Security review | No | Yes | Yes (Codex Security) | ? |
| Commit from UI | No | ? | Yes | Yes |
| Commit attribution setting | Yes | ? | ? | ? |
| Headless text, JSON, stream-JSON | Yes | Yes | Yes (JSONL) | Yes |
| Streaming input | No | Yes | ? | Yes |
| JSON schema output | No | Yes | Yes | Yes |
| Max turns | Yes | Yes | ? | ? |
| Budget flag | No | Yes | ? | ? |
| System prompt flags | No | Yes | ? | ? |
| Skip persistence | Yes | Yes | Yes | ? |
| Allowed and disallowed tools flags | Yes | Yes | ? | ? |
| Agent server RPC | Yes (`AgentService`) | Partial: SDK over CLI | Yes (app server) | Partial: ACP, `agentapi` |
| SDK: Go | Yes (library) | No | No | ? |
| SDK: TypeScript | No (generated client only) | Yes | Yes | ? |
| SDK: Python | No | Yes | Yes | Yes |
| Managed hosted agent API | No | Yes (Managed Agents) | ? | Yes, preview |

## IDE integrations

| Capability | xlsft\`s harness | Claude Code | Codex | Antigravity |
|---|---|---|---|---|
| VS Code | No | Yes | Yes | Yes |
| Cursor, Windsurf | No | ? | Yes | ? |
| JetBrains | No | Yes | Yes (vendor-provided) | Yes |
| Visual Studio | No | ? | ? | Yes |
| Xcode | No | ? | Yes (vendor-provided) | Yes, preview |
| Zed | Partial: none direct; ACP is client only | ? | ? | Yes |
| Eclipse | No | ? | ? | Yes, preview |
| Open a file in the user's editor | Yes (`OpenInEditor`) | ? | Yes | ? |
| Terminal setup helper | No | Yes | ? | ? |
| Tab completions | No | ? | ? | Yes (Tab) |
| Selection context from IDE | No | Yes | Yes | Yes |
| Inline diff in IDE | No | Yes | Yes | Yes |

## Desktop app UI

| Capability | xlsft\`s harness | Claude Code | Codex | Antigravity |
|---|---|---|---|---|
| Session sidebar by project | Yes | Yes | Yes | Yes |
| Sub-session tree | Yes | ? | ? | ? |
| Diff pane, unified and split | Yes | Yes | Yes | Yes |
| Line comments sent to the agent | Yes | ? | Yes | Yes |
| Diff bases | Yes (`ListDiffBases`) | ? | Partial: unstaged, staged | Yes (uncommitted, branch, agent) |
| Stage, revert from diff | No | ? | Yes | Yes |
| Terminal pane | Yes (real PTY, tabs, reattach) | Yes | Yes | Yes |
| Files pane | Yes (read-only viewer) | Yes (editor) | Partial: previews | Yes |
| Edit files in app | No | Yes | ? | Yes (IDE) |
| Browser pane | Yes | Yes | Yes | Yes |
| Side chat pane | Yes | Yes | Yes | Yes (`/btw`) |
| Background tasks pane | Yes | ? | Yes (Activity) | Yes |
| Agents pane | Yes | ? | Yes | Yes |
| Split pane layout | Yes (drag, split) | Yes | Yes | Yes (two conversations) |
| Two sessions side by side | No | Yes | ? | Yes |
| Command palette | Yes (⌘K) | ? | Yes | Yes |
| Artifacts: page and grid | No ("soon"; demo data only) | Yes | Partial: previews | Yes |
| Inline HTML visual in transcript | Yes (`Visual`) | ? | ? | Yes (`/generative_ui`) |
| File delivery cards | Yes (`SendUserFile`) | Yes | ? | ? |
| Open things next to chat | Yes (`Show`) | ? | ? | ? |
| Reveal in file manager | Yes (`RevealPath`) | ? | ? | ? |
| Office and slides preview | No | ? | Yes | ? |
| Mermaid viewer | No | ? | ? | Yes |
| Image viewer | Yes | ? | Yes (canvas with comments) | Yes |
| Quote selection to reply | Yes | ? | ? | Yes |
| Themes in app | Yes (4, custom, VS Code import) | ? | Yes | Yes |
| Font and size settings | Yes | ? | ? | Yes |
| Customizable app shortcuts | No | ? | Yes | Yes |
| Verbose transcript toggle | Yes (Ctrl+O) | Yes | ? | ? |
| Tray icon | No | ? | ? | Yes |
| Pets, mascot | Partial: cat mascot | ? | Yes | ? |
| Customize screen | No ("soon") | Yes | Yes | Yes |

## TUI features

| Capability | xlsft\`s harness | Claude Code | Codex | Antigravity |
|---|---|---|---|---|
| Slash commands | Partial: 10 | Yes (100+) | Yes (60+) | Yes (40+) |
| `/help`, `/clear`, `/compact`, `/model`, `/status`, `/tasks`, `/rewind`, `/exit` | Yes | Yes | Partial: no `/rewind` | Partial: no `/status` |
| `/init` | No | Yes | Yes | ? |
| `/config` | No | Yes | Yes | Yes |
| `/mcp` | No | Yes | Yes | Yes |
| `/permissions` | No | Yes | Yes | Yes |
| `/agents` | No | Yes | Yes | Yes |
| `/resume` | No (flag only) | Yes | Yes | Yes |
| `/diff` | No | Yes | Yes | Yes |
| `/review` | No | Yes | Yes | ? |
| `/usage` | Partial: `/status` | Yes | Yes | Yes |
| `/voice` | No | Yes | Yes | Yes |
| Output style picker | Yes | Yes | ? | ? |
| Custom keybindings | No | Yes | Yes (`/keymap`) | Yes |
| Chords | No | Yes | ? | ? |
| Status line script | Planned (setup agent) | Yes | Partial: built-in items | Yes |
| Terminal title script | No | ? | Yes | Yes |
| Vim mode | No | Yes | Yes | Yes |
| Themes | Partial: dark, light by detect | Yes (6, custom) | Yes (custom) | Yes (8) |
| Color-blind themes | No | Yes | ? | Yes |
| Fallback to 256, 16, no color | Yes | Yes (ANSI) | ? | ? |
| External editor | No | Yes | Yes | Yes |
| File mention `@` | Yes | Yes | Yes | Yes |
| History search | Partial: up and down only | Yes | Yes | ? |
| Multi-line input | Yes | Yes | ? | ? |
| Readline keys | Yes | Yes | ? | ? |
| Inline scrollback rendering | Yes | Yes | Yes | Yes |
| Full-screen renderer | No | Yes, preview | Yes | Yes |
| Queue shown in dock | Yes | Yes | ? | ? |

## Voice

| Capability | xlsft\`s harness | Claude Code | Codex | Antigravity |
|---|---|---|---|---|
| Dictation | Yes (UI) | Yes (CLI, VS Code) | Yes | Yes |
| Dictation engine on device | Yes (Parakeet, Whisper) | No | ? | ? |
| Dictation via server providers | Yes | n/a (Anthropic) | n/a | n/a |
| Browser speech fallback | Yes | n/a | n/a | n/a |
| Text to speech, read aloud | Yes | ? | ? | ? |
| Local TTS | Yes (Piper, macOS `say`) | ? | ? | ? |
| Cloud speech vendors | Yes (10 vendors) | ? | ? | ? |
| Live voice conversation | No | ? | Yes (GPT-Live) | Yes (Gemini Live) |
| Voice in TUI | No | Yes | Yes | Yes |
| Choice of language | Partial: provider-dependent | Yes (20) | ? | ? |

## Images, PDF, notebooks

| Capability | xlsft\`s harness | Claude Code | Codex | Antigravity |
|---|---|---|---|---|
| Read images | Yes (PNG, JPEG, GIF, WebP) | Yes | Yes (`view_image`) | Yes |
| Paste images | Yes (UI) | Yes | Yes | Yes |
| Attach files in composer | Yes (50 MB, 1 MB text) | ? | ? | Yes (20 MB) |
| Annotate an image before sending | Yes | ? | Partial: canvas | ? |
| Read PDF | Partial: whole file; `pages` not honored | Yes (page ranges) | ? | Yes |
| PDF viewer with comments | Partial: viewer only | ? | Yes | Yes |
| Read notebooks | Yes | Yes | ? | Yes |
| Edit notebooks | Yes | Yes | ? | Yes |
| Run notebooks | No | ? | ? | Yes |
| Video input | No | ? | ? | Yes (CLI) |
| Screenshot objects stored | Yes (object store) | ? | ? | ? |

## Cost and usage visibility

| Capability | xlsft\`s harness | Claude Code | Codex | Antigravity |
|---|---|---|---|---|
| Usage dashboard in app | Yes (tiles, bars, tables) | Yes | Yes | Partial |
| Dollar cost per user | Yes (metered providers) | Yes | ? | ? |
| Cost per session | Partial: budget enforced; per-turn usage | Yes (`/usage`) | ? | ? |
| Token count per turn | Yes | Yes | Yes | Yes |
| Context breakdown by source | Yes | Yes | Partial | Yes |
| Provider balance and limits | Yes | Yes (plan) | Yes (plan) | Yes (plan) |
| Local model usage counted | Yes (`ReportLocalUsage`) | n/a | ? | ? |
| Flat-rate plans counted as tokens only | Yes | n/a | n/a | n/a |
| Usage insights report | No | Yes (`/insights`) | Yes (Usage Insights) | ? |

## Languages and accessibility

| Capability | xlsft\`s harness | Claude Code | Codex | Antigravity |
|---|---|---|---|---|
| App UI languages | No (English only) | ? | Partial: IDE locale override | ? |
| Docs languages | No (English) | Yes (12) | ? | Yes (17) |
| Response language setting | No (instructions only) | Yes | ? | ? |
| Screen reader mode | No | Yes | ? | Yes (App) |
| ARIA labels in UI | Yes | ? | ? | ? |
| Reduced motion | Yes (UI, TUI env) | Yes | ? | Yes (animation off) |
| Keyboard use in main flows | Partial | Yes | ? | Yes |

## Security features

| Capability | xlsft\`s harness | Claude Code | Codex | Antigravity |
|---|---|---|---|---|
| Provider keys never reach client | Yes | Partial: with gateway | Partial: with gateway | ? |
| Device tokens, short-lived access | Yes | ? | ? | ? |
| SSRF guard on server | Yes | ? | ? | ? |
| Env secrets withheld on import | Yes | n/a | ? | ? |
| Untrusted text marked in reminders | Yes (`pr_activity`) | Yes (scanning) | Yes (guidance) | ? |
| Local UI listener: token, Origin, Host checks | Yes | ? | Yes (capability token) | ? |
| Plugin validation | No | Yes (evals) | Yes (review) | Yes (`validate`) |
| Security white paper or trust center | No | Yes | Yes | ? |
| Bug bounty | No | Yes | ? | ? |
| Dedicated cyber-safety models | No | Partial: safety fallback | Yes (Daybreak) | ? |
| Scorecard, pinned CI actions | In development: on `main` | ? | ? | ? |

## Extensibility

| Capability | xlsft\`s harness | Claude Code | Codex | Antigravity |
|---|---|---|---|---|
| Custom tools via MCP | Yes | Yes | Yes | Yes |
| Custom agents | Yes | Yes | Yes | Yes |
| Skills | Planned | Yes | Yes | Yes |
| Hooks | Planned | Yes | Yes | Yes |
| Plugins | Planned | Yes | Yes | Yes |
| Workflows as code | Yes | Yes | ? | ? |
| Protobuf contracts for every service | Yes | ? | Partial: generated schema | ? |
| Replace any frontend | Yes (`AgentService`) | ? | Yes (app server) | Partial: ACP |
| Server-side prompt edit without release | Yes | ? | ? | ? |
| Fork and self-maintain | Yes (MIT) | No | Yes (Apache-2.0) | ? |
| Agent browser extension API | No | ? | Yes (WebMCP, annotations) | ? |

## What xlsft\`s harness doesn't cover yet

Derived from the tables above, ordered by how much each matters to someone switching. Where a gap is `Planned`, the plan is no promise of a date.

1. **Skills are not loaded.** No `Skill` tool or `SKILL.md` loader; imported skills sit on the server unused. All three others have them, and teams keep real workflows in them.
2. **Hooks don't run.** Imported and detected only. Teams use hooks for policy, formatting and notification, and README and PLAN text describes them as if present.
3. **Custom slash commands don't exist.** The composer's list is static; `.claude/commands` is imported but never offered. Existing project commands stop working on switch.
4. **No tagged release or install path.** Release flow is on `main` and has never run; no package managers. Today getting started means building from source.
5. **Linux and Windows are not shipped.** CLI and desktop compile or have config only; only macOS is in the release plan. Others ship on all three.
6. **OS sandbox is macOS only.** Linux and Windows are stubs, so shell commands there rely on prompts alone.
7. **No plugins or marketplace.** Planned for M4. Others distribute skills, MCP servers and hooks this way.
8. **No IDE extension.** VS Code and JetBrains users lose inline diffs and selection context.
9. **No SSO (SAML, OIDC), SCIM, MFA or custom roles.** LDAP and admin/user roles only. Usually a blocker for larger organizations.
10. **No OpenTelemetry, SIEM export or general audit log.** MCP audit rows are written with no way to read them. Hard to pass a compliance review.
11. **No cloud or hosted agents.** Every run needs a machine of yours. Others can work while a laptop sleeps, and some run best-of-N.
12. **No scheduled tasks or event triggers.** UI shows "soon"; no cron or wake-up tool. Others offer local and cloud schedules.
13. **No CI action, `@mention` bot or PR review bot.** GitHub and GitLab connections exist, but nothing reviews a PR on its own. `Create PR` button isn't wired.
14. **No code-review command.** `ReportFindings` and its UI exist, but no `/code-review` or bundled prompt drives them.
15. **MCP gaps.** No settings screen, no elicitation, no resources or prompts for the agent, no server instructions, no header helper. Some servers won't work fully.
16. **No native mobile app, no push to a phone.** Browser control needs a reachable client; the server image doesn't serve the web UI.
17. **SSH remote hosts missing.** Menu says "soon". Remote-machine work is a common setup and is the core of the plan's architecture diagram.
18. **Goal mode, agent teams, task graph absent.** Others keep working to a condition or coordinate agents with shared tasks. `Workflow` covers scripted fan-out only.
19. **Auto memory doesn't write.** The index is read, nothing maintains it, so nothing is learned between sessions without user files.
20. **`/init` missing.** New repos get no generated instructions file.
21. **TUI is thin.** 10 slash commands; no vim mode, keybindings, status line, `/mcp`, `/permissions`, `/config`, `/resume`, `/diff`. Terminal-first users will notice first.
22. **Windows `PowerShell` tool missing.** Follows from Windows not shipping; needed once it does.
23. **No fast tier, advisor or cache TTL controls.** Effort exists; provider speed tiers aren't exposed.
24. **Headless gaps.** No `--json-schema`, streaming input, budget or system prompt flags; no TypeScript or Python SDK. Limits scripted and CI use.
25. **Cloud provider auth.** Bedrock, Vertex and Foundry work only as custom endpoints without native credentials. Enterprises often must use these.
26. **Browser: no real signed-in Chrome, no recording.** Headless own profile only; extension planned. Tasks needing the user's logins can't be done.
27. **Computer use is unproven and macOS only.** Never run for real; Windows and Linux are stubs.
28. **Artifacts, knowledge base, task tracker, customize screen absent.** UI shows "soon"; the artifacts page runs on demo data.
29. **PDF page ranges not honored.** Large PDFs are all or nothing. No `run` for notebooks.
30. **No i18n, screen reader mode or color-blind themes.** English only; ARIA labels and reduced motion exist.
31. **Live voice, image generation, MDM policy, session export, full-text transcript search, CSV usage export.** Smaller gaps, each with a clear workaround or low urgency.

Where xlsft\`s harness has something the others don't document, as far as we could confirm: any model behind three API shapes, provider keys kept off clients, a self-hosted server including search and object store, LDAP, local TTS and STT with 10 cloud speech vendors, a classifier with a choice of model or HTTP service, sessions synced to your own server, MIT license. See the tables for limits.

## Could not confirm

- **Claude Code:** local model support (no official page); CLI binary's source license; CLI UI languages; air-gapped deployment; computer use on Linux Desktop; voice output; compliance certifications (Trust Center not read); Ultra usage multiplier (pricing page and docs differ); extension compatibility with VS Code forks; analytics leaderboard.
- **Codex:** complete tool list (none published); read, grep and glob tool names; legacy SSE, MCP prompts, agent-level MCP resources; model fallback; cache controls; built-in file checkpoints; peer agent teams; LDAP; air-gapped use; Linux computer use and Linux as remote host; Android parity (remote page says yes, pricing bullet names iOS); Fast mode with an API key (speed page and plan table conflict); Zed, Neovim, ACP; notebooks; screen reader; CLI languages; per-session dollar cost; latest desktop app release.
- **Antigravity:** computer use, Chrome extension, SCIM, LDAP, product RBAC, self-hosting, air-gapped, retention periods, GitHub Action, PR review bot (no mention found, which isn't a denial); whether the IDE is a VS Code fork; MCP prompts and elicitation; meaning of the "Fast" model entry; what undo does to files; `/compact` in CLI; knowledge items status; hook handler types (docs conflict); per-user spend controls; docs.cloud.google.com pages weren't walked.
- **xlsft\`s harness:** the `In development` and `Landing` rows describe code read, not behavior observed. Nothing here ran a real account or agent. `Partial` for web and mobile reflects the image not serving the UI today.

## Sources

Official pages, read 2026-11-06.

**Claude Code** ([docs index](https://code.claude.com/docs/llms.txt), [changelog](https://code.claude.com/docs/en/changelog), [pricing](https://claude.com/pricing)): [tools](https://code.claude.com/docs/en/tools-reference), [settings](https://code.claude.com/docs/en/settings), [permissions](https://code.claude.com/docs/en/permissions), [permission modes](https://code.claude.com/docs/en/permission-modes), [sandboxing](https://code.claude.com/docs/en/sandboxing), [hooks](https://code.claude.com/docs/en/hooks), [MCP](https://code.claude.com/docs/en/mcp), [skills](https://code.claude.com/docs/en/skills), [plugins](https://code.claude.com/docs/en/plugins/overview), [subagents](https://code.claude.com/docs/en/sub-agents), [agent teams](https://code.claude.com/docs/en/agent-teams), [workflows](https://code.claude.com/docs/en/workflows), [worktrees](https://code.claude.com/docs/en/worktrees), [memory](https://code.claude.com/docs/en/memory), [checkpointing](https://code.claude.com/docs/en/checkpointing), [commands](https://code.claude.com/docs/en/commands), [keybindings](https://code.claude.com/docs/en/keybindings), [statusline](https://code.claude.com/docs/en/statusline), [CLI reference](https://code.claude.com/docs/en/cli-reference), [headless](https://code.claude.com/docs/en/headless), [Agent SDK](https://code.claude.com/docs/en/agent-sdk/overview), [GitHub Actions](https://code.claude.com/docs/en/github-actions), [GitLab CI](https://code.claude.com/docs/en/gitlab-ci-cd), [code review](https://code.claude.com/docs/en/code-review), [remote control](https://code.claude.com/docs/en/remote-control), [cloud sessions](https://code.claude.com/docs/en/claude-code-on-the-web), [routines](https://code.claude.com/docs/en/routines), [desktop](https://code.claude.com/docs/en/desktop), [VS Code](https://code.claude.com/docs/en/vs-code), [JetBrains](https://code.claude.com/docs/en/jetbrains), [Chrome](https://code.claude.com/docs/en/chrome), [computer use](https://code.claude.com/docs/en/computer-use), [voice](https://code.claude.com/docs/en/voice-dictation), [models](https://code.claude.com/docs/en/model-config), [fast mode](https://code.claude.com/docs/en/fast-mode), [gateway](https://code.claude.com/docs/en/claude-apps-gateway), [analytics](https://code.claude.com/docs/en/analytics), [costs](https://code.claude.com/docs/en/costs), [monitoring](https://code.claude.com/docs/en/monitoring-usage), [managed settings](https://code.claude.com/docs/en/managed-settings), [data usage](https://code.claude.com/docs/en/data-usage), [ZDR](https://code.claude.com/docs/en/zero-data-retention), [self-hosted runners](https://code.claude.com/docs/en/self-hosted-environments), [accessibility](https://code.claude.com/docs/en/accessibility), [setup](https://code.claude.com/docs/en/setup), [Claude Tag](https://claude.com/docs/claude-tag).

**Codex** ([docs index](https://learn.chatgpt.com/docs/llms.txt), [changelog](https://learn.chatgpt.com/docs/changelog), [repository](https://github.com/openai/codex), [pricing](https://learn.chatgpt.com/docs/pricing)): [CLI](https://learn.chatgpt.com/docs/codex/cli), [commands](https://learn.chatgpt.com/docs/developer-commands?surface=cli), [IDE](https://learn.chatgpt.com/docs/codex/ide), [cloud](https://learn.chatgpt.com/docs/cloud), [models](https://learn.chatgpt.com/docs/models), [speed](https://learn.chatgpt.com/docs/agent-configuration/speed), [config](https://learn.chatgpt.com/docs/config-file/config-advanced), [AGENTS.md](https://learn.chatgpt.com/docs/agent-configuration/agents-md), [memories](https://learn.chatgpt.com/docs/customization/memories), [skills](https://learn.chatgpt.com/docs/build-skills), [plugins](https://learn.chatgpt.com/docs/plugins), [hooks](https://learn.chatgpt.com/docs/hooks), [subagents](https://learn.chatgpt.com/docs/agent-configuration/subagents), [MCP](https://learn.chatgpt.com/docs/extend/mcp), [approvals](https://learn.chatgpt.com/docs/agent-approvals-security), [sandboxing](https://learn.chatgpt.com/docs/sandboxing), [rules](https://learn.chatgpt.com/docs/agent-configuration/rules), [computer use](https://learn.chatgpt.com/docs/computer-use), [browser](https://learn.chatgpt.com/docs/browser), [worktrees](https://learn.chatgpt.com/docs/environments/git-worktrees), [automations](https://learn.chatgpt.com/docs/automations), [remote](https://learn.chatgpt.com/docs/remote-connections), [GitHub](https://learn.chatgpt.com/docs/third-party/github), [GitLab](https://learn.chatgpt.com/docs/third-party/gitlab), [GitHub Action](https://learn.chatgpt.com/docs/github-action), [non-interactive](https://learn.chatgpt.com/docs/non-interactive-mode), [SDK](https://learn.chatgpt.com/docs/codex-sdk), [app server](https://learn.chatgpt.com/docs/app-server), [managed configuration](https://learn.chatgpt.com/docs/enterprise/managed-configuration), [governance](https://learn.chatgpt.com/docs/enterprise/governance), [voice](https://learn.chatgpt.com/docs/features/voice), [open source](https://learn.chatgpt.com/docs/open-source).

**Antigravity** ([docs index](https://antigravity.google/llms.txt), [changelog](https://antigravity.google/docs/changelog), [pricing](https://antigravity.google/pricing), [download](https://antigravity.google/download)): [overview](https://antigravity.google/docs/overview), [models](https://antigravity.google/docs/models), [plans](https://antigravity.google/docs/plans), [permissions](https://antigravity.google/docs/permissions), [sandbox](https://antigravity.google/docs/sandbox), [hooks](https://antigravity.google/docs/hooks), [MCP](https://antigravity.google/docs/mcp), [rules](https://antigravity.google/docs/rules), [skills](https://antigravity.google/docs/skills), [plugins](https://antigravity.google/docs/plugins), [subagents](https://antigravity.google/docs/subagents), [`/boost`](https://antigravity.google/docs/boost), [`/teamwork`](https://antigravity.google/docs/teamwork), [browser](https://antigravity.google/docs/commands/browser), [settings](https://antigravity.google/docs/settings), [CLI reference](https://antigravity.google/docs/cli/reference), [headless](https://antigravity.google/docs/cli/headless), [remote control](https://antigravity.google/docs/remote-control), [remote agents](https://antigravity.google/docs/remote-agents), [enterprise](https://antigravity.google/docs/enterprise), [extensions](https://antigravity.google/docs/ide/extensions), [SDK](https://antigravity.google/docs/sdk/overview), [voice](https://antigravity.google/docs/voice-mode), [features](https://antigravity.google/docs/features), [Cloud blog](https://cloud.google.com/blog/products/ai-machine-learning/expanding-google-antigravity-for-enterprise-customers).

**xlsft\`s harness:** `packages/client`, `packages/core`, `packages/ui`, `packages/cli`, `packages/desktop`, `proto/`, `deploy/`, `docs/PLAN.md`; `docs/release.md` and `packages/client/sandbox` on `main`.
