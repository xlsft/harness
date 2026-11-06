# Prompt flow: when and how Claude Code injects its prompt fragments

Status: reference for the harness prompt bundle (`packages/core/prompts`). Source corpus: the public
Piebald-AI/claude-code-system-prompts extraction, Claude Code v2.1.316, **1057 fragment files**
(`system-prompts/*.md`). This document only names fragments by file id; it describes their purpose in
our own words and never reproduces their text (clean-room rule, see `packages/core/prompts/README.md`).

Machine-readable classification: [`prompt-flow.json`](prompt-flow.json) (one object per file:
`file, group, channel, trigger, model, status`).

## How to read this

Every fragment is classified on four axes.

| Axis | Values |
|---|---|
| **channel** | `system-static` (cached system prompt section) · `system-conditional` (system section added only for some env/config/mode) · `tool-desc` / `tool-param` (tool description or parameter text) · `reminder` (`<system-reminder>` attached to a user message or a tool result) · `utility` (separate one-off model call: titles, summaries, composers) · `classifier` (separate call that returns a verdict) · `subagent` (system prompt of a spawned agent) · `skill` (slash command or skill body, loaded on invocation) · `data` (reference blob loaded on demand) · `non-model` (never sent to a model: SDK typings, settings schema, stderr, HTML pages) |
| **trigger** | session start, every request, a specific tool call, a mode change, compaction, idle/away, a hook, a slash command, an MCP event, a background job, a product surface |
| **model** | `main` (the session model) · `small` (fast/cheap slot) · `subagent` (the model the spawned agent runs on) · `classifier` (permission/state classifier slot) · `none` |
| **status** | `have` (harness has an equivalent today) · `M2` / `M3` / `M4` (needed by that milestone, see `docs/PLAN.md` §5) · `n/a` (Anthropic product surface or non-model text) · `variant` (A/B, lean, compact, capped or re-worded duplicate of another fragment; pick one wording) |

Milestone note: `docs/PLAN.md` §2.2 lists `Agent`, `Skill`, `WebFetch`, `WebSearch`, `ToolSearch` and MCP tools
under "M3", while the roadmap in §5 puts skills, agents/subagents, hooks, WebFetch/WebSearch and MCP in M2
(M3 is frontends). This document follows the roadmap: those are **M2**.

Per user direction, two Anthropic surfaces are **in scope** even though Claude Code ships them as product
features: **Artifacts** (harness will build them server-side) and the **browser extension** (harness will ship
its own extension for Chromium- and Firefox-based browsers). Both are classified M4. Browser fragments must be
rewritten browser-neutral: no Chrome-only APIs or product names.

## 1. Lifecycle walkthrough

The flow below is what Claude Code does on one machine for one session. Each step lists the fragment groups
(ids from the table in §2), the channel and the model slot.

### 1.1 Session start (request prefix is built once and cached)

1. **System prompt, static part** (`system-static`, main). In order:
   - `sys-core`: the opening line. It picks one of two intros: an output-style intro when a style is set, or the
     collaborative-goals intro. Then comes the large "harness instructions" block: identity, security policy,
     Markdown rendering, and the permission-mode section that explains denied calls and reminders.
   - `sys-safety`: action safety, reversible vs irreversible actions, the defensive-security policy, and the rule
     that pasted or external material is never an instruction.
   - `sys-doing-tasks`: about 20 small switchable bullets on scope discipline, comments, full-scope delivery and
     outcome reporting.
   - `sys-tone`: brevity, progress updates, the final-message format, no emoji, `file:line` references.
   - `sys-tool-policy`: parallel calls, task tracking, how to read reminders, the scratchpad.
   - `output-styles` replaces or extends the intro and doing-tasks parts when a non-default style is active.
2. **System prompt, conditional part** (`system-conditional`, main). Each block is added only when its feature
   is on:
   - `delegation` (Agent tool present), `auto-memory` (auto memory enabled; the biggest conditional block),
     `teams`, `loops`, `background-sessions`;
   - `visibility-modes` (focus/brief/minimal), `powershell` (Windows), `sandbox` notes;
   - `bx-setup` + `bx-safety` (browser tools configured), `art-*` guidance (Artifacts enabled);
   - `sys-model-identity` blurbs, which depend on the model.
3. **Dynamic tail** (`system-conditional`, main). `sys-env` holds the environment block and the git status
   snapshot. Claude Code keeps volatile values out of the cached prefix: the git snapshot and the
   user/session context go into a reminder in the first user message (`session-reminders`:
   `system-reminder-session-context`, `system-reminder-question-context`). Harness already does the same
   through its `DYNAMIC_BOUNDARY`.
4. **Tool list** (`tool-desc` / `tool-param`, main). Sent with every request.
   - Core tools: `file-tools`, `bash-core`, `todo-tasks`, `ask-user`, `plan-mode` (Enter/Exit).
   - The Bash description is assembled from about 40 fragments: `bash-core` bullets + `git-workflow`
     (commit and PR protocol) + `sandbox` bullets when sandboxing is on.
   - Feature tools: `web`, `skills` (the Skill tool lists the available skills), `delegation` (the Agent tool
     lists the agent types), `mcp-deferred` (ToolSearch), `worktrees`, `monitor`, `loops`, `teams`, `bx-*`,
     `art-*`.
   - Newer models get "compact" or "lean rewrite" descriptions (`variant`).
5. **First user message reminders** (`reminder`, main):
   - `memory-files`: AGENTS.md, the rules and the user memory, each framed by path.
   - `session-reminders`: session/question context, and provider context when not on Anthropic's API.
   - `output-styles`: the "style active" reminder.
   - `mcp-deferred`: the deferred-tool announcement and MCP servers that are still connecting or failed to
     connect.
   - `proactivity`: the autonomy level.

### 1.2 Each user turn

- The user message gets reminders attached for whatever changed since the last turn:
  - `memory-files`: a memory refresh, or a nested instruction file found under a directory the model touched;
  - `file-read-notices` and the harness `file_changed` reminder: files that changed on disk;
  - `ide-lsp`: IDE selection, opened file, new diagnostics;
  - `todo-tasks`: a reminder after several turns without TodoWrite;
  - `session-reminders`: token/USD budget, queued notifications, `/btw`;
  - `hooks`: `UserPromptSubmit` hook output (additional context, blocking error);
  - `delegation`: @agent mentions.
- Mode reminders arrive while a mode is active. `plan-mode` sends a "plan mode is active" reminder every turn,
  in one of three variants: simple, 5-phase, or subagent. `auto-mode-main` and `visibility-modes`
  (brief mode) work the same way. Modes are signalled through reminders rather than system-prompt edits, so
  switching a mode never invalidates the cached prefix.
- The model request is: cached system blocks, then the tool list, then the conversation with reminders inline.

### 1.3 Tool call and tool result

- Before execution there is a permission check. In auto mode it goes to `auto-mode-classifier`: a separate
  call on the `classifier` slot that judges each action against the transcript and rules. Environment
  addenda come from `auto-mode-classifier-ext`. Hooks may run before the tool: `hooks` shell hooks, or a
  prompt/agent hook evaluator (`utility` on `small`, or `subagent`).
- The tool result can carry reminders (`reminder` in the tool_result, main):
  - `file-read-notices`: empty file, offset past EOF, truncation, already in context, large file or PDF;
  - `mcp-deferred`: output truncation, empty resource;
  - `web`: an untrusted-content reporting note;
  - `hooks`: PostToolUse success or blocking error;
  - `ask-user`: question validation;
  - `sandbox`: sandbox failure guidance;
  - `background-tasks`: launch notices, "still running".
- Background completions arrive later as reminders (`background-tasks`; harness `background_task_done`).

### 1.4 Plan mode branch

- Entering plan mode: the EnterPlanMode tool result, then a `plan-mode` reminder every turn. With the 5-phase
  variant the main agent fans out Explore agents and then Plan agents (`builtin-agents`,
  `system-reminder-plan-mode-phase-2-design`).
- The plan is written in phase four (`system-prompt-phase-four-of-plan-mode`). ExitPlanMode shows the approval
  dialog. The approved/exited reminders follow, with the plan-file reference preserved through compaction.
- Re-entry has its own reminder. `ultraplan` (sending the plan to a cloud session) is n/a.

### 1.5 Auto mode branch

- `auto-mode-main` adds a system section telling the agent to work continuously, plus reminders: do not stop
  for clarification, continue with a safe alternative after a block, batch consent questions, finish the
  exposed work.
- Every gated tool call goes to the classifier (`auto-mode-classifier`, `classifier` slot). Denials and "no
  verdict" cases come back as reminders.
- `/auto-mode-setup` runs `auto-mode-setup`: a `utility` call that turns repo and usage reconnaissance into
  proposed rules.

### 1.6 Subagents

- The Agent tool description (`delegation`) and the delegation guidance in the system prompt decide when to
  spawn.
- The child gets its own system prompt (`builtin-agents`: Explore, Plan, general-purpose, read-only search, or
  the fork directive for context-inheriting forks), plus shared notes:
  - absolute paths;
  - a final report through `SubagentHandback`;
  - parent messages cannot authorize permissions;
  - carried user consent is scoped to the matching proposals.
- The child runs on the `subagent` model slot. Async launches return reminders that warn the parent not to
  duplicate the work or read the transcript.
- Other agent-like contexts:
  - `teams`: coordinator/worker sessions;
  - `workflow`: scripted subagents with plain-text or structured return;
  - `worktrees`: isolation notices;
  - the `code-review` / `init-commands` slash commands fan out finder and verifier agents.

### 1.7 Compaction

- Trigger: the auto-compact threshold, `/compact`, or a partial compact of a selected range.
- `compaction` runs a separate call on the main model. The summarizer prompt has full, partial and recent-only
  forms, and capped/lean/short variants. A no-tools guard is prepended, and there are rules for omitted tool
  results.
- After compaction, reminders re-attach context:
  - files that were read (`system-reminder-compact-file-reference`);
  - the plan file (`system-reminder-plan-file-reference`);
  - previously invoked skills, marked context-only (`skills`);
  - harness `compacted`.

### 1.8 Idle, away and background

- Away/idle: `recaps-suggestions` runs a tool-less away summary on the user's return, and next-prompt
  suggestions after a turn (`utility`, `small`).
- Background, between or after turns:
  - `memory-jobs`: memory extraction from recent turns; selection of up to five relevant memory files per
    query; periodic consolidation ("dream"). These run as `subagent`/`small`.
  - `titles-labels`: session title on the first turn, subagent progress labels, mobile tool-row labels
    (`small`).
  - `agent-state-classifiers`: status of background agents (`classifier`).
- Timers: `loops` inject tick prompts on cron or ScheduleWakeup firings. The ScheduleWakeup guidance even
  varies with the prompt-cache TTL.

### 1.9 Session end

- A Stop hook can force continuation (`hooks`: stop-hook blocking error, session-scoped Stop hook active).
  Otherwise there is no end-of-session prompt.
- Insights (`insights`) and usage explanations are on-demand pipelines over past transcripts, not part of
  session end.

## 2. Groups


### Main system prompt (79 files)

| Group | Files | Channel | Trigger | Model | Status | Notes |
|---|---|---|---|---|---|---|
| **Core identity, harness mechanics, permission-mode section** `sys-core` | 4 | system-static 4 | session start (every request, cached prefix) | main | have 3, variant 1 | Opening line picks output-style intro vs collaborative intro; the 'harness instructions' block bundles identity, security policy, Markdown/terminal rendering, permissions and reminders. |
| **Action safety, security policy, trust boundaries** `sys-safety` | 7 | system-static 7 | session start | main | have 6, variant 1 | Reversible vs irreversible actions, confirmation before outward-facing actions, defensive-security policy, pasted/external content is not an instruction. |
| **Doing tasks: scope, completeness, comments, reporting** `sys-doing-tasks` | 20 | system-static 20 | session start | main | have 17, M4 1, variant 2 | Many small switchable bullets (scope discipline, no compat shims, no speculative error handling, full-scope delivery, comment policy, outcome reporting). Harness has one 40-doing_tasks section. |
| **Tone, output and user-facing writing** `sys-tone` | 9 | system-static 9 | session start | main | have 7, M2 1, variant 1 | Brevity, progress updates, outcome-first final message, no emoji, file:line references, no colon before tool calls, optional reply language. |
| **Tool usage policy and context mechanics** `sys-tool-policy` | 5 | system-static 5 | session start | main | have 3, variant 2 | Parallel calls, task tracking, how to read system reminders, scratchpad dir, auto-compaction awareness. |
| **Environment and git snapshot** `sys-env` | 2 | system-conditional 2 | session start (dynamic tail / first user message) | main | have 1, n/a 1 | Git status snapshot; harness fills it via the 10-env dynamic section. |
| **Output styles and learning mode** `output-styles` | 6 | system-conditional 5, reminder 1 | session start / style change | main | have 6 | Built-in Concise/Learning/Explanatory styles plus the reminder that a style is active. Harness ships output styles via the output_style reminder (commit e6a11ff). |
| **Focus / brief / responsive / minimal modes** `visibility-modes` | 11 | system-conditional 6, reminder 2, tool-desc 2, tool-param 1 | mode toggle (settings, mobile/remote surfaces) | main | M4 10, variant 1 | Modes where the user sees only final text or SendUserMessage output; minimal mode skips hooks/LSP/plugins/memory. Relevant once mobile/relay views exist. |
| **Proactivity level announcements** `proactivity` | 5 | reminder 5 | setting change | main | M4 5 | Low/medium/high autonomy level injected as a reminder and withdrawn on change; maps to harness permission modes. |
| **Model self-identity blurbs** `sys-model-identity` | 2 | system-conditional 2 | session start, per model | main | n/a 2 | Claude-model-specific identity paragraphs. Harness equivalent is per-model overrides, not these texts. |
| **PowerShell tool and edition notes** `powershell` | 8 | system-conditional 4, tool-desc 2, tool-param 2 | session start on Windows | main | M4 7, variant 1 | Separate PowerShell tool with edition-specific syntax notes (5.1 vs 7+). Only needed for native Windows support. |

### Memory (37 files)

| Group | Files | Channel | Trigger | Model | Status | Notes |
|---|---|---|---|---|---|---|
| **AGENTS.md / nested instruction framing** `memory-files` | 5 | reminder 5 | first user message; when a nested instruction file is discovered; on memory refresh | main | have 1, M2 4 | Frames each instruction file by path inside the combined memory block; nested files arrive when the model touches a subdirectory. Harness has project_instructions; nested/refresh variants missing. |
| **Auto memory (persistent file memory) instructions** `auto-memory` | 24 | system-conditional 19, tool-desc 3, reminder 2 | session start when auto memory is enabled | main | M4 23, variant 1 | Memory types (user, feedback, project, reference), index file, save/skip rules, team memory variants. Largest conditional block in the main prompt. |
| **Memory extraction, selection and consolidation jobs** `memory-jobs` | 8 | subagent 4, reminder 4 | background job after turns / idle ('dream') / per query | small/main | M4 8 | Side agents that extract memories from recent turns, pick up to five relevant memory files per query, and periodically consolidate ('dream'). |

### Core tools and their notices (132 files)

| Group | Files | Channel | Trigger | Model | Status | Notes |
|---|---|---|---|---|---|---|
| **File tools: Read, Write, Edit, Glob, Grep, NotebookEdit** `file-tools` | 13 | tool-desc 13 | every request (tool list) | main | have 7, M2 1, variant 5 | Full and 'compact' description variants are served per model generation. |
| **File-read result notices** `file-read-notices` | 8 | reminder 8 | Read tool result | main | have 1, M2 6, M4 1 | Empty file, offset past EOF, truncated, already in context and unchanged, large file / PDF guidance, read-before-summarize disclosure. |
| **Bash tool description and parameters** `bash-core` | 22 | tool-desc 18, tool-param 3, system-conditional 1 | every request (tool list) | main | have 21, variant 1 | Assembled from many bullets: dedicated-tool alternatives, cwd, quoting, timeouts, sleep rules, background runs, description field. |
| **Git commit / PR protocol and quick commit-PR prompts** `git-workflow` | 8 | tool-desc 5, skill 3 | every request (inside Bash description); /commit, /commit-push-pr commands | main | M2 8 | Commit and PR workflows live inside the Bash description; quick-commit/PR prompts are slash-command bodies with pre-filled git context. Harness Bash description has none of this yet. |
| **Sandbox behaviour (Bash sandbox, Windows sandbox)** `sandbox` | 34 | system-conditional 18, tool-desc 15, tool-param 1 | every request when sandboxing is on; tool result on sandbox failure | main | M4 34 | Default-to-sandbox, evidence of sandbox failures, retry-unsandboxed rules, many Windows-sandbox specific notes. |
| **TodoWrite and Task list tools** `todo-tasks` | 9 | tool-desc 7, reminder 2 | every request; reminder after N turns without use | main | have 3, M4 5, variant 1 | TodoWrite (v1) is in harness; the TaskCreate/TaskList/TaskUpdate/TaskGet family (v2, shared with teammates) is not. |
| **AskUserQuestion** `ask-user` | 8 | tool-desc 6, system-conditional 1, reminder 1 | every request; validation notice in tool result | main | have 3, M2 1, M3 3, variant 1 | Core tool exists; HTML preview field and extended-host ordering guidance are UI-dependent. |
| **Plan mode** `plan-mode` | 15 | reminder 10, tool-desc 4, system-conditional 1 | mode change (enter/exit plan), every turn while active, ExitPlanMode result | main | have 6, M2 7, variant 2 | Tool descriptions + 'plan mode is active' reminder (simple / 5-phase / subagent variants), re-entry, approved/exited notices, user-controlled exit. |
| **Background commands and task output** `background-tasks` | 8 | reminder 5, tool-desc 3 | background command finished / silent / reaped; tool result of GetTask | main | M2 3, M4 4, variant 1 | Quiet check-in nudges, memory-pressure reaping, notifications arriving alongside real user input, 'tool still running' while the user typed. |
| **Monitor tool** `monitor` | 3 | tool-desc 3 | every request when Monitor is available | main | M4 3 | Streaming stdout/WebSocket event monitor with optional push notifications. |
| **IDE integration and LSP** `ide-lsp` | 4 | reminder 3, tool-desc 1 | IDE events (file opened, selection), diagnostics after edits | main | M4 4 | IDE selection/open-file notices, new-diagnostics notice, LSP tool. |

### Extensibility (M2 core) (82 files)

| Group | Files | Channel | Trigger | Model | Status | Notes |
|---|---|---|---|---|---|---|
| **WebFetch / WebSearch and the web-reading agent** `web` | 8 | tool-desc 5, subagent 2, reminder 1 | every request; tool result; web-fetch subagent | main/subagent | M2 6, variant 2 | Full and concise variants; untrusted-content reporting reminder; a built-in web-reading agent that reads URLs in isolation. |
| **Skills: Skill tool, restored skills, skillify** `skills` | 4 | tool-desc 2, skill 1, reminder 1 | every request; after compaction (restored skill list); /skillify | main | M2 2, M4 1, variant 1 | Skill tool description lists available skills; after compaction previously invoked skills are restored as context-only. |
| **MCP status and deferred tools (ToolSearch)** `mcp-deferred` | 21 | tool-desc 11, reminder 10 | session start / MCP connect events / new tools announced | main | M2 13, M4 2, variant 4, n/a 2 | Deferred tool names are announced in a reminder and loaded by ToolSearch; MCP connecting/failed/retry notices; resource tools; output truncation warning. |
| **Hooks: feedback, prompt/agent hooks, evaluators** `hooks` | 15 | reminder 8, system-conditional 3, utility 2, subagent 1, data 1 | hook events (PreToolUse, Stop, UserPromptSubmit, ...) | main/small/subagent | M2 12, M4 2, variant 1 | Hook output arrives as reminders (success, blocking error, additional context, stop); prompt/agent hooks run a separate evaluator model. |
| **Subagent delegation guidance and Agent tool** `delegation` | 20 | system-conditional 9, tool-desc 7, reminder 3, subagent 1 | every request (system section + Agent tool description); agent launch result | main/subagent | M2 15, M4 1, variant 4 | When to delegate, fork vs fresh agent, briefing examples, async launch notices, @agent mentions. |
| **Built-in subagent system prompts** `builtin-agents` | 12 | subagent 11, tool-desc 1 | Agent tool call | subagent | M2 9, variant 3 | Explore, Plan, general-purpose, read-only search, fork directive, subagent reporting notes, handback tool, carried consent history. |
| **Repo setup: /init (AGENTS.md, skills, hooks)** `repo-setup` | 2 | skill 2 | /init | main | M2 2 | Analyze the codebase and write or improve AGENTS.md; the newer flow also proposes skills and hooks. |

### Permissions and auto mode (25 files)

| Group | Files | Channel | Trigger | Model | Status | Notes |
|---|---|---|---|---|---|---|
| **Auto mode (main-agent side)** `auto-mode-main` | 8 | reminder 6, system-conditional 2 | permission mode = auto; classifier denial / no verdict | main | M2 8 | Tells the agent it runs continuously, how to proceed after a block, not to stop for clarification, to finish the exposed work. Harness DecisionService exists but the main-side prompts do not. |
| **Auto mode classifier (security monitor)** `auto-mode-classifier` | 5 | classifier 5 | every tool call that needs a permission decision in auto mode | classifier | have 4, M4 1 | Separate model call that judges each action; rule addenda per environment. Harness has decision/system.md + classify tool. |
| **Auto mode setup proposal** `auto-mode-setup` | 2 | data 1, utility 1 | /auto-mode-setup | main/small | M2 2 | Turns repo and usage reconnaissance into a JSON proposal of auto-mode environment rules; ships with auto mode. |
| **Classifier addenda for email/Slack/hosts** `auto-mode-classifier-ext` | 10 | classifier 10 | auto mode on specific surfaces (email, Slack relay, attached machines) | classifier | n/a 10 | Surface-specific rules for the security monitor. |

### Session machinery and utilities (36 files)

| Group | Files | Channel | Trigger | Model | Status | Notes |
|---|---|---|---|---|---|---|
| **Session-state reminders** `session-reminders` | 10 | reminder 10 | per turn / event (budget, token limit, resume, queued notifications) | main | M2 10 | Token/USD budget, output-token-limit continuation, session context entries, queued notification batches, restart recovery. |
| **Compaction / summarization** `compaction` | 12 | utility 11, reminder 1 | auto-compact threshold, /compact, partial compact | main | have 3, M2 5, variant 4 | Summarizer prompt (full, partial, recent-only, capped/lean/short variants), no-tools guard, post-compact file and plan references. |
| **Titles, names and progress labels** `titles-labels` | 4 | utility 4 | first turn; /rename; subagent progress; mobile rows | small | have 1, M3 3 | Session title generator; kebab-case session name; 3-5 word agent progress label; tool-call row label. |
| **Away recap, prompt suggestions, side questions** `recaps-suggestions` | 5 | utility 3, reminder 2 | idle/away return; after a turn; /btw | small/main | M3 3, n/a 2 | Away summary on return, next-prompt suggestions, tool-less /btw side question, 'you should know' side agent. |
| **Background agent state classifier** `agent-state-classifiers` | 4 | classifier 4 | background agent transcript tail changes (sidebar status) | classifier | M4 3, n/a 1 | Classifies a background agent as working/blocked/done/failed for status display; harness needs it for the session list once background agents and the web UI exist. |
| **Statusline setup agent** `statusline-setup` | 1 | subagent 1 | statusline-setup agent / /statusline | subagent | M3 1 | Configures the CLI status line; harness equivalent belongs with the TUI. |

### Orchestration and autonomy (65 files)

| Group | Files | Channel | Trigger | Model | Status | Notes |
|---|---|---|---|---|---|---|
| **Teams / coordinator / cross-session messaging** `teams` | 17 | reminder 7, tool-desc 5, system-conditional 4, subagent 1 | coordinator or teammate mode; SendMessage; inbound peer message | main/subagent | M4 15, variant 2 | Coordinator orchestration, worker instructions, team shutdown, peer-message authority warnings, ListAgents/SendMessage. |
| **Worktree isolation** `worktrees` | 9 | system-conditional 5, tool-desc 2, subagent 1, reminder 1 | EnterWorktree/ExitWorktree; background/fork sessions | main/subagent | M4 7, variant 2 | Worktree tools plus isolation notices for workflow subagents, forks and background jobs. |
| **Workflow tool and ultracode** `workflow` | 9 | subagent 4, reminder 2, tool-desc 2, skill 1 | Workflow tool call; ultracode toggle | subagent | M4 8, variant 1 | Background scripted multi-agent orchestration; workflow subagents return plain text or structured output. |
| **Loops, cron, wakeups, push notifications** `loops` | 26 | tool-desc 8, skill 7, reminder 6, system-conditional 4, subagent 1 | /loop, cron firing, ScheduleWakeup timer tick | main/subagent | M4 23, variant 3 | Tick injections (cron vs dynamic pacing, with/without loop.md), re-arm decisions, noop ticks, notification guidance, PR follow-up cron. |
| **Background job sessions** `background-sessions` | 4 | system-conditional 3, subagent 1 | session launched as a background job / daemon | main/subagent | M4 3, variant 1 | Job temp dir, worktree isolation and persistence, keepalive rules, background job agent narration. |

### Bundled commands and skills (80 files)

| Group | Files | Channel | Trigger | Model | Status | Notes |
|---|---|---|---|---|---|---|
| **Bundled dev slash commands (/security-review, /simplify, /batch)** `init-commands` | 4 | skill 4 | slash command | main | M4 3, variant 1 | Prompt bodies of bundled review/cleanup/orchestration commands; they fan out to subagents. |
| **/code-review family** `code-review` | 33 | skill 31, tool-desc 2 | /code-review [effort] | subagent | M4 29, variant 3, n/a 1 | Effort-tiered finder angles, verification phases, output format, PR comment posting, fix application; inline fallback when agents are unavailable. |
| **Self-configuration and help skills** `self-help` | 17 | skill 13, subagent 2, data 2 | /doctor, /stuck, /update-config, guide agent, statusline setup | main/subagent | M4 17 | Harness needs its own versions describing harness, not Claude Code. |
| **Insights and usage reports** `insights` | 14 | utility 10, skill 4 | /insights, /explain-usage | main | M4 13, variant 1 | Multi-call pipeline: per-session facet extraction then aggregated sections. |
| **Run / verify app skills** `run-verify` | 9 | skill 9 | skill invocation | main | M4 9 | Templates for launching and driving the user's app to verify changes. |
| **Team onboarding guide (ONBOARDING.md)** `team-onboarding` | 3 | subagent 2, skill 1 | onboarding-guide command / team-onboarding skill | subagent/main | M4 3 | Co-authors an ONBOARDING.md for new teammates from the team's setup; optional draft share-link step. |

### Artifacts (server-side) (143 files)

| Group | Files | Channel | Trigger | Model | Status | Notes |
|---|---|---|---|---|---|---|
| **Artifacts (server-side): publish, update, watch, share** `art-publish` | 57 | tool-desc 35, tool-param 14, reminder 4, system-conditional 2, data 1, utility 1 | every request when the Artifact tool is enabled; publish/refusal results | main/small | M4 45, variant 12 | Artifact tool action reference and parameters, page contract (skeleton, theme tokens, CDN allowlist, responsive), same-response edit+republish, stale-publish refusals and merge guidance, watches and sharing. 'app-wording' copies are variants. |
| **Artifacts (server-side): comment threads** `art-comments` | 26 | system-conditional 7, utility 6, reminder 6, subagent 4, tool-desc 2, tool-param 1 | comment wake on a watched artifact; comment-list tool results | main/small/subagent | M4 25, variant 1 | Untrusted-data framing of threads, triage classifier, no-tools reply/edit composers and fast acknowledgements (small model), background thread worker, auto-reply state notices. |
| **Artifacts (server-side): database, assets, runtime capabilities** `art-data-assets` | 21 | tool-desc 10, data 6, tool-param 3, reminder 2 | Artifact tool enabled with db/assets/capabilities | main | M4 18, variant 3 | Shared per-artifact database with version preconditions and batch writes, asset store, runtime capability declarations, live rooms, connector/MCP manifests for pages. |
| **Artifacts (server-side): types, design canvas, slides** `art-types-design` | 32 | tool-desc 15, reminder 10, skill 6, data 1 | artifact type create/quickstart; type-creation slash command; design/slides surfaces | main | M4 29, variant 3 | Published artifact types and file-backed type content, type-creation command, Design canvas / Slides via a JS REPL tool, prefetched type and design-system files (trust boundaries), chart tool, claude.ai/design sync tools. |
| **Data visualization / diagramming skills** `dataviz-design` | 7 | data 5, skill 2 | skill invocation | main | M4 7 | Chart/diagram design guidance for artifact pages; ships with server-side artifacts. |

### Browser extension (Chromium + Firefox) (32 files)

| Group | Files | Channel | Trigger | Model | Status | Notes |
|---|---|---|---|---|---|---|
| **Browser extension (Chromium + Firefox): setup and connection** `bx-setup` | 14 | tool-desc 8, system-conditional 4, reminder 2 | session start when the extension is configured; enable/setup tool; extension disconnected | main | M4 12, variant 2 | Enable/offer-setup tools, deferred tool loading in one batch, choosing among several connected browsers, disconnected/unavailable/switched-off notices. Wording must be browser-neutral: no Chrome-only APIs or product names. |
| **Browser extension (Chromium + Firefox): navigation and tabs** `bx-nav-tabs` | 5 | tool-desc 5 | every request while browser tools are loaded | main | M4 5 | Navigate/back/forward, tab context, closing tabs the agent opened, batched browser actions, saved shortcuts/workflows. Wording must be browser-neutral: no Chrome-only APIs or product names. |
| **Browser extension (Chromium + Firefox): page reading, find, forms, uploads** `bx-page-read` | 4 | tool-desc 4 | every request while browser tools are loaded | main | M4 4 | Accessibility-tree read, page text extraction, natural-language element find, file-input uploads. Wording must be browser-neutral: no Chrome-only APIs or product names. |
| **Browser extension (Chromium + Firefox): computer actions and screenshots** `bx-computer` | 3 | tool-desc 2, tool-param 1 | every request while browser tools are loaded | main | M4 3 | Mouse/keyboard/scroll/screenshot/zoom actions inside the page and the overall tool-family description. Wording must be browser-neutral: no Chrome-only APIs or product names. |
| **Browser extension (Chromium + Firefox): console, network, JavaScript** `bx-devtools` | 4 | tool-desc 3, tool-param 1 | every request while browser tools are loaded | main | M4 4 | Console and network readers with filtering, in-page JavaScript execution. Wording must be browser-neutral: no Chrome-only APIs or product names. |
| **Browser extension (Chromium + Firefox): browsing rules and recordings** `bx-safety` | 2 | system-conditional 2 | session start when browser tools are present | main | M4 2 | Intro plus shared rules: record multi-step interactions as GIFs (no separate recording tool description exists in this corpus), avoid blocking JS dialogs, stop instead of looping, start from fresh tab context. Wording must be browser-neutral: no Chrome-only APIs or product names. |

### Not applicable product surfaces (218 files)

| Group | Files | Channel | Trigger | Model | Status | Notes |
|---|---|---|---|---|---|---|
| **Desktop computer use, Claude app built-in browser, account-linking flows** `browser-computer` | 9 | tool-desc 7, skill 1, reminder 1 | computer-use / desktop-app browser enabled | main | n/a 9 | Anthropic desktop app and claude.ai chat-linking flows; not part of the harness browser extension. |
| **claude.ai plugins, connectors, Cowork, onboarding, mods** `claude-ai-ecosystem` | 32 | tool-desc 17, skill 9, data 3, system-conditional 2, subagent 1 | org catalog / onboarding flows | main/subagent | n/a 32 | Anthropic product surface. |
| **Attached machines, cloud sessions, staged files** `remote-machines` | 53 | reminder 48, subagent 2, data 2, tool-desc 1 | cloud session events (machine asleep/unreachable, restart, staging) | main/subagent | n/a 53 | Anthropic cloud surface. Harness relay (M2) may need a small analog: 'machine offline' notice. |
| **claude.ai Projects, Slack relay, inbox, PR steward** `projects-inbox` | 41 | tool-desc 11, system-conditional 9, reminder 9, skill 5, subagent 4, data 1, classifier 1, tool-param 1 | Remote Control / project thread events | main/subagent/classifier | n/a 41 | Anthropic product surface. |
| **Cloud routines (/schedule, RemoteTrigger)** `cloud-schedule` | 5 | skill 4, tool-desc 1 | /schedule command | main | n/a 5 | Anthropic-hosted scheduled cloud agents. |
| **Ultraplan / remote planning sessions** `ultraplan` | 4 | reminder 4 | plan sent to a cloud planning session | main | n/a 4 | Anthropic cloud surface for remote plan refinement. |
| **Claude API skill and SDK references** `claude-api-refs` | 47 | data 40, reminder 4, skill 3 | /claude-api skill loads on demand | main | n/a 47 | Claude-branded docs (SDK references, managed agents templates). |
| **Claude Test plugin** `claude-test` | 4 | subagent 2, data 1, skill 1 | plugin agents | subagent | n/a 4 | Anthropic plugin. |
| **Self-hosted runner** `self-hosted-runner` | 17 | data 14, skill 2, tool-desc 1 | runner CLI / setup agent | main | n/a 17 | Anthropic cloud runner product; mostly operator-facing text. |
| **Misc product tools (feedback, goals, advisor, end conversation, files)** `misc-product-tools` | 6 | tool-desc 4, system-conditional 1, reminder 1 | tool list when the feature is enabled | main | M4 2, n/a 4 | Mostly Anthropic product features; Advisor (stronger reviewer model) and SendUserFile are the model-agnostic ideas worth copying later. |

### Non-model text (128 files)

| Group | Files | Channel | Trigger | Model | Status | Notes |
|---|---|---|---|---|---|---|
| **Settings schema descriptions** `nm-settings` | 24 | non-model 24 | never sent to the model (settings schema / docs) | none | n/a 24 | Useful as a reference when documenting harness settings. |
| **SDK / control protocol schema text** `nm-sdk-protocol` | 74 | non-model 74 | never sent to the model (SDK typings, control requests, events) | none | n/a 74 | Reference for harness AgentService design, not prompts. |
| **Errors, warnings, CLI help, HTML pages** `nm-errors` | 30 | non-model 26, data 2, reminder 2 | never sent to the model (stderr, UI, gateway pages) | none | n/a 30 | User/operator-facing strings. |

## 3. Counts

**Per status** (files / words of fragment text):

| Status | Files | Words |
|---|---|---|
| have | 93 | 18,270 |
| M2 | 115 | 20,064 |
| M3 | 10 | 2,725 |
| M4 | 401 | 94,390 |
| variant | 75 | 13,570 |
| n/a | 363 | 85,080 |
| **total** | 1057 | 234,099 |

**Per channel:**

| Channel | Files |
|---|---|
| tool-desc | 253 |
| reminder | 199 |
| non-model | 124 |
| system-conditional | 116 |
| skill | 109 |
| data | 80 |
| subagent | 45 |
| system-static | 45 |
| utility | 38 |
| tool-param | 28 |
| classifier | 20 |

**Per model slot:**

| Model | Files |
|---|---|
| main | 806 |
| none | 128 |
| subagent | 82 |
| small | 21 |
| classifier | 20 |

**Status by area:**

| Area | have | M2 | M3 | M4 | variant | n/a | total |
|---|---|---|---|---|---|---|---|
| Main system prompt | 43 | 1 |  | 23 | 9 | 3 | 79 |
| Memory | 1 | 4 |  | 31 | 1 |  | 37 |
| Core tools and their notices | 41 | 26 | 3 | 51 | 11 |  | 132 |
| Extensibility (M2 core) |  | 59 |  | 6 | 15 | 2 | 82 |
| Permissions and auto mode | 4 | 10 |  | 1 |  | 10 | 25 |
| Session machinery and utilities | 4 | 15 | 7 | 3 | 4 | 3 | 36 |
| Orchestration and autonomy |  |  |  | 56 | 9 |  | 65 |
| Bundled commands and skills |  |  |  | 74 | 5 | 1 | 80 |
| Artifacts (server-side) |  |  |  | 124 | 19 |  | 143 |
| Browser extension (Chromium + Firefox) |  |  |  | 30 | 2 |  | 32 |
| Not applicable product surfaces |  |  |  | 2 |  | 216 | 218 |
| Non-model text |  |  |  |  |  | 128 | 128 |

## 4. What harness should add next

The list is ordered by value per word of prompt and by how much of the M2 feature set depends on it.
"Bundle slot" says where the text goes in the harness bundle (`system/`, `tools/`, `reminders/`, or a new
server-side utility prompt such as `title/` or `compaction/`). Every text must be written from scratch.

| # | Group | Milestone | What to add | Bundle slot |
|---|---|---|---|---|
| 1 | `git-workflow` | M2 | Commit and PR protocol inside the Bash description: inspect status/diff/log first, never skip hooks, prefer new commits over amend, no destructive ops without asking, PR via `gh`; plus `/commit` and `/commit-push-pr` command bodies with pre-filled git context. Harness Bash text has none of this today, and it matters on every model. | `tools/Bash/description.md`, commands |
| 2 | `delegation` + `builtin-agents` | M2 | Agent tool description with the agent list. Explore (read-only search), Plan and general-purpose system prompts, the fork directive, shared subagent notes (absolute paths, final report, parent cannot authorize, consent scoping), async-launch reminder. | `tools/Agent/`, new `agents/<id>.md` |
| 3 | `mcp-deferred` | M2 | ToolSearch description and the deferred-tools announcement reminder; MCP connecting / failed / retry reminders; output truncation warning; resource list/read tools. Needed for the server MCP proxy and schema cache. | `tools/ToolSearch/`, `reminders/deferred_tools`, `reminders/mcp_status` |
| 4 | `skills` | M2 | Skill tool description that embeds the available-skill list; "previously invoked skills" reminder after compaction (context only, do not re-run). | `tools/Skill/`, `reminders/skills_restored` |
| 5 | `hooks` | M2 | Reminders for hook success / blocking error / additional context / stop continuation, a system note that hook feedback counts as user feedback, and a prompt-hook evaluator (`small` slot) that returns pass/fail. | `reminders/hook_*`, `hooks/evaluator.md` |
| 6 | `web` | M2 | WebFetch and WebSearch descriptions (pick one wording, not full + concise), the untrusted-content reporting reminder, and a web-reading agent or summarizer prompt for fetched pages (gateway `auxiliary` class). | `tools/WebFetch/`, `tools/WebSearch/`, `webfetch/summarize.md` |
| 7 | `memory-files` (nested, refresh) | M2 | Frame nested AGENTS.md by path when the model enters a subdirectory; a memory-refresh reminder that tells the model earlier memory reads are stale; a managed-folder "unverified" header. | `reminders/nested_instructions`, `reminders/memory_updated` |
| 8 | `session-reminders` | M2 | Output-token-limit continuation (resume mid-sentence); token/USD budget; **provider context** (harness is model-agnostic, so tell the model which features differ for this provider); external-source trust boundary; queued notification batches; dangerous-removal block. | `reminders/*` |
| 9 | `file-read-notices` | M2 | Read-result notices: offset past EOF, truncated (with retry guidance), already in context and unchanged, large file (suggest a subagent), disclose partial reads before summarizing. | `reminders/*` or tool_result text |
| 10 | `compaction` extras | M2 | Partial compaction (selected range), recent-only summary, rules for omitted or cleared tool results, post-compact file references and plan-file reference. | `compaction/partial.md`, `reminders/compact_file_ref` |
| 11 | `plan-mode` extras | M2 | Re-entry reminder, user-controlled exit, approval-tool enforcement (every plan turn ends in AskUserQuestion or ExitPlanMode), the 5-phase variant that uses Explore/Plan agents once #2 lands, and the subagent variant. | `reminders/plan_*` |
| 12 | `auto-mode-main` + `auto-mode-setup` | M2 | DecisionService already exists, but the main agent is not told about auto mode. Add the auto-mode system section, consent-flow and clarification-bias reminders, no-verdict/denied notices, and the `/auto-mode-setup` proposal generator. | `system/dynamic/auto_mode`, `reminders/auto_*`, `decision/setup.md` |
| 13 | `background-tasks` | M2 | Quiet check-in for silent background commands, marking background events that arrive alongside real user input as non-user, "tool still running" while the user typed. | `reminders/*` |
| 14 | `repo-setup` | M2 | `/init`: analyze the repo and write or improve AGENTS.md, optionally proposing skills and hooks. | `commands/init.md` |
| 15 | `recaps-suggestions` + `titles-labels` + `statusline-setup` | M3 | For the TUI and web UI: away recap on return, next-prompt suggestions, `/btw` tool-less side question, subagent progress labels, kebab-case `/rename`, statusline setup agent. All on the `small` slot. | `utility/*.md` |

After those, in M4 order of usefulness:
- `auto-memory` + `memory-jobs`: memory types, index, save rules; extraction and consolidation jobs;
  per-query memory selection.
- `teams` and `worktrees`: SendMessage, coordinator/worker, EnterWorktree/ExitWorktree and isolation notices.
- `loops`: cron, ScheduleWakeup and tick prompts; PushNotification.
- `sandbox`: Bash sandbox evidence and retry rules.
- `code-review` and `init-commands`: one effort-tiered `/code-review`; `/security-review`, `/simplify`.
- `art-*`: server-side Artifacts.
- `bx-*`: the browser extension, browser-neutral wording.
- `ide-lsp`, `monitor`, `visibility-modes`, `proactivity`, `powershell`, `self-help` (harness-specific docs),
  `insights`, `run-verify`, `team-onboarding`, `agent-state-classifiers`.

Rules of thumb drawn from how Claude Code is built:
- **Signal modes through reminders, not system edits.** Claude Code adds plan, auto, brief, proactivity,
  output style and deferred tools as reminders, so the cached prefix survives a mode switch. Harness already
  does this for output styles; keep doing it for every new mode.
- **Compose long texts from small conditional fragments.** The Bash description and the
  attached-machine notices are built from bullets and shared clauses. The harness bundle could support
  `tools/Bash/parts/*.md` with conditions, instead of hand-maintaining whole variants per model.
- **One wording per concept.** 75 files are variants: lean rewrites, compact descriptions, app wording,
  cache-TTL variants. Harness already has per-model overrides, which is the right mechanism for this.
- **Treat everything not typed by the user as data.** Many fragments fence untrusted content and spell
  out that reminders, relays, peers, hooks and fetched pages never grant consent. Carry that rule into every
  new reminder template.


## 5. Appendix: every file

Format: `file-id  group  status`. Status letters are spelled out; grep by file id or group.

```
agent-prompt-agent-hook                                                                 hooks                     M2
agent-prompt-artifact-comment-completion-reply-and-resolution                           art-comments              M4
agent-prompt-artifact-comment-thread-analyst                                            art-comments              M4
agent-prompt-artifact-comment-thread-background-directive                               art-comments              M4
agent-prompt-artifact-editor-thread-follow-up                                           art-comments              M4
agent-prompt-artifact-type-creation-slash-command                                       art-types-design          M4
agent-prompt-artifact-type-creation-slash-command-first-party-type                      art-types-design          M4
agent-prompt-artifact-type-creation-slash-command-mcp-connector-tools                   art-types-design          M4
agent-prompt-artifact-type-creation-slash-command-mcp-connector-tools-first-party-type  art-types-design          M4
agent-prompt-artifact-type-creation-slash-command-quickstart-flow                       art-types-design          M4
agent-prompt-auto-mode-rule-reviewer                                                    auto-mode-classifier      M4
agent-prompt-away-summary-generation                                                    recaps-suggestions        M3
agent-prompt-background-agent-state-classifier                                          agent-state-classifiers   M4
agent-prompt-background-agent-state-classifier-ask-opens-message-rule                   agent-state-classifiers   M4
agent-prompt-background-agent-state-classifier-chat-thread-relay-rule                   agent-state-classifiers   n/a
agent-prompt-background-agent-state-classifier-user-merge-handoff-rule                  agent-state-classifiers   M4
agent-prompt-background-job-agent-instructions                                          background-sessions       M4
agent-prompt-batch-slash-command                                                        init-commands             M4
agent-prompt-claude-code-guide                                                          self-help                 M4
agent-prompt-claude-guide-agent                                                         self-help                 M4
agent-prompt-claude-md-creation                                                         repo-setup                M2
agent-prompt-claude-test-author                                                         claude-test               n/a
agent-prompt-claude-test-explorer                                                       claude-test               n/a
agent-prompt-code-review-gitlab-comment-posting                                         code-review               M4
agent-prompt-code-review-inline-gap-sweep-phase                                         code-review               M4
agent-prompt-code-review-minimal-mode                                                   code-review               M4
agent-prompt-code-review-part-1-base-finder-angles                                      code-review               M4
agent-prompt-code-review-part-10-reportfindings-output-format                           code-review               M4
agent-prompt-code-review-part-2-low-effort-minimum-findings-mode                        code-review               variant
agent-prompt-code-review-part-2-low-effort-mode                                         code-review               M4
agent-prompt-code-review-part-3-extra-high-and-maximum-effort-modes                     code-review               M4
agent-prompt-code-review-part-4-three-state-verification-phase                          code-review               M4
agent-prompt-code-review-part-5-recall-biased-verification-phase                        code-review               M4
agent-prompt-code-review-part-6-medium-effort-mode                                      code-review               M4
agent-prompt-code-review-part-7-high-effort-mode                                        code-review               M4
agent-prompt-code-review-part-8-github-comment-posting                                  code-review               M4
agent-prompt-code-review-part-9-fix-application                                         code-review               M4
agent-prompt-code-review-unavailable-agent-inline-mode                                  code-review               variant
agent-prompt-coding-session-title-generator                                             titles-labels             have
agent-prompt-conversation-summarization                                                 compaction                have
agent-prompt-conversation-summarization-capped-variant                                  compaction                variant
agent-prompt-conversation-summarization-lean-variant                                    compaction                variant
agent-prompt-conversation-summarization-short-variant                                   compaction                variant
agent-prompt-conversation-summarization-with-additional-instructions                    compaction                have
agent-prompt-coordinator-worker-instructions                                            teams                     M4
agent-prompt-determine-which-memory-files-to-attach                                     memory-jobs               M4
agent-prompt-dream-memory-consolidation                                                 memory-jobs               M4
agent-prompt-explore                                                                    builtin-agents            M2
agent-prompt-general-purpose                                                            builtin-agents            M2
agent-prompt-general-purpose-agent                                                      builtin-agents            variant
agent-prompt-general-task-agent                                                         builtin-agents            M2
agent-prompt-hook-condition-evaluator                                                   hooks                     M2
agent-prompt-hook-condition-evaluator-stop                                              hooks                     variant
agent-prompt-inherited-context-for-worktree-sub-agent                                   worktrees                 M4
agent-prompt-onboarding-guide-draft-share-link-workflow                                 team-onboarding           M4
agent-prompt-onboarding-guide-generator                                                 team-onboarding           M4
agent-prompt-parent-messages-and-user-authorization-lean-rewrite                        builtin-agents            variant
agent-prompt-plan-mode-enhanced                                                         builtin-agents            M2
agent-prompt-plugin-eval-pilot-trust-requirement                                        claude-ai-ecosystem       n/a
agent-prompt-pr-follow-up-cron                                                          loops                     M4
agent-prompt-pr-follow-up-cron-pr-steward-label-check                                   projects-inbox            n/a
agent-prompt-project-thread-status-card-classifier                                      projects-inbox            n/a
agent-prompt-project-thread-status-card-classifier-ask-opens-message-rule               projects-inbox            n/a
agent-prompt-prompt-suggestion-generator-v2                                             recaps-suggestions        M3
agent-prompt-pull-request-creation                                                      git-workflow              M2
agent-prompt-quick-git-commit                                                           git-workflow              M2
agent-prompt-quick-pr-creation                                                          git-workflow              M2
agent-prompt-read-only-search-agent                                                     builtin-agents            M2
agent-prompt-recent-message-summarization                                               compaction                M2
agent-prompt-remote-machine-auto-mode-rules                                             remote-machines           n/a
agent-prompt-rename-auto-generate-session-name                                          titles-labels             M3
agent-prompt-schedule-action-selection                                                  cloud-schedule            n/a
agent-prompt-schedule-local-mcp-server-limitation                                       cloud-schedule            n/a
agent-prompt-schedule-slash-command                                                     cloud-schedule            n/a
agent-prompt-security-monitor-attached-machine-call-results                             remote-machines           n/a
agent-prompt-security-monitor-claude-tag-connector-writes                               projects-inbox            n/a
agent-prompt-security-monitor-email-direct-send-example                                 auto-mode-classifier-ext  n/a
agent-prompt-security-monitor-email-group-reply-example                                 auto-mode-classifier-ext  n/a
agent-prompt-security-monitor-email-group-reply-exception                               auto-mode-classifier-ext  n/a
agent-prompt-security-monitor-email-reply-approval-scope                                auto-mode-classifier-ext  n/a
agent-prompt-security-monitor-email-reply-outbound-content-rule                         auto-mode-classifier-ext  n/a
agent-prompt-security-monitor-email-reply-outbound-recipient-rule                       auto-mode-classifier-ext  n/a
agent-prompt-security-monitor-email-sender-only-approval-scope                          auto-mode-classifier-ext  n/a
agent-prompt-security-monitor-email-sender-only-outbound-clearance-rule                 auto-mode-classifier-ext  n/a
agent-prompt-security-monitor-email-sender-turns                                        auto-mode-classifier-ext  n/a
agent-prompt-security-monitor-for-autonomous-agent-actions-first-part                   auto-mode-classifier      have
agent-prompt-security-monitor-forwarded-user-turns                                      auto-mode-classifier-ext  n/a
agent-prompt-security-monitor-host-context-line-guidance                                auto-mode-classifier      have
agent-prompt-security-review-slash-command                                              init-commands             M4
agent-prompt-session-transcript-chunk-summary                                           insights                  M4
agent-prompt-simplify-slash-command                                                     init-commands             M4
agent-prompt-simplify-unavailable-agent-inline-mode                                     init-commands             variant
agent-prompt-status-line-setup                                                          statusline-setup          M3
agent-prompt-subagent-delegation-configuration-classifier                               delegation                M4
agent-prompt-subagent-reporting-notes-lean-rewrite                                      builtin-agents            variant
agent-prompt-summarization-no-tools-guard                                               compaction                have
agent-prompt-web-fetch-agent-usage-guidance                                             web                       M2
agent-prompt-web-reading-specialist                                                     web                       M2
agent-prompt-worker-fork                                                                builtin-agents            M2
agent-prompt-workflow-script-plain-text-return-note                                     workflow                  M4
agent-prompt-workflow-script-structured-return-note                                     workflow                  M4
agent-prompt-workflow-subagent-plain-text-output                                        workflow                  M4
agent-prompt-workflow-subagent-structured-output                                        workflow                  M4
data-allowedmcpservers-setting                                                          nm-settings               n/a
data-allowmanagedpermissionrulesonly-allowed-tools-frontmatter-scope                    nm-settings               n/a
data-allowmanagedpermissionrulesonly-deny-rules-still-apply                             nm-settings               n/a
data-allowmanagedpermissionrulesonly-setting                                            nm-settings               n/a
data-artifact-ask-first-intake-form-instructions                                        art-types-design          M4
data-artifact-capability-verification-pass                                              art-data-assets           M4
data-artifact-connector-call-observation-requirement                                    art-data-assets           M4
data-artifact-connector-server-naming-guidance                                          art-data-assets           M4
data-artifact-host-mcp-server-guidance                                                  art-data-assets           M4
data-artifact-mcp-connector-guidance                                                    art-data-assets           M4
data-artifact-publish-merged-over-newer-version-file-changes-notice                     art-publish               M4
data-artifact-runtime-capability-declarations                                           art-data-assets           M4
data-attributionproject-setting                                                         nm-settings               n/a
data-auto-compact-inputs-changed-event-schema                                           nm-sdk-protocol           n/a
data-auto-mode-setup-usage                                                              auto-mode-setup           M2
data-availablemodelsmatch-setting                                                       nm-settings               n/a
data-background-tasks-changed-event-schema                                              nm-sdk-protocol           n/a
data-blockwritesoutsideworkingdirectories-setting                                       nm-settings               n/a
data-built-in-gh-stand-in-api-command-help                                              nm-errors                 n/a
data-chrome-browser-hints-control-request                                               nm-sdk-protocol           n/a
data-claude-api-reference-php                                                           claude-api-refs           n/a
data-claude-api-reference-ruby                                                          claude-api-refs           n/a
data-claude-code-agent-proxy-troubleshooting-guide                                      nm-errors                 n/a
data-claude-code-gateway-customer-routed-inference-protocol                             nm-errors                 n/a
data-claude-code-gateway-protocol                                                       nm-errors                 n/a
data-claude-code-live-documentation-sources                                             self-help                 M4
data-claude-code-recent-changes-reference                                               self-help                 M4
data-claude-gateway-landing-page                                                        nm-errors                 n/a
data-claude-platform-on-aws-reference                                                   claude-api-refs           n/a
data-claude-tag-claude-in-slack-reference                                               projects-inbox            n/a
data-claude-test-spec-file-format                                                       claude-test               n/a
data-cloud-session-unverified-remote-branch-push-notice                                 remote-machines           n/a
data-code-change-published-event-schema                                                 nm-sdk-protocol           n/a
data-code-review-startup-token-file-descriptor-error                                    nm-errors                 n/a
data-command-plugin-source-command-field                                                nm-settings               n/a
data-cowork-plugin-examples                                                             claude-ai-ecosystem       n/a
data-cowork-plugin-mcp-discovery-and-connection                                         claude-ai-ecosystem       n/a
data-cross-session-inbound-setting                                                      nm-settings               n/a
data-data-visualization-anti-patterns                                                   dataviz-design            M4
data-data-visualization-choosing-a-form                                                 dataviz-design            M4
data-data-visualization-components                                                      dataviz-design            M4
data-data-visualization-interaction                                                     dataviz-design            M4
data-data-visualization-marks-and-anatomy                                               dataviz-design            M4
data-deniedmodels-setting                                                               nm-settings               n/a
data-dialog-expiry-setting                                                              nm-settings               n/a
data-directoryadded-hook-description                                                    hooks                     M2
data-files-api-reference-c                                                              claude-api-refs           n/a
data-files-api-reference-go                                                             claude-api-refs           n/a
data-files-api-reference-java                                                           claude-api-refs           n/a
data-files-api-reference-php                                                            claude-api-refs           n/a
data-files-api-reference-python                                                         claude-api-refs           n/a
data-files-api-reference-typescript                                                     claude-api-refs           n/a
data-gateway-device-code-entry-page                                                     nm-errors                 n/a
data-gateway-ignored-x-forwarded-for-warning                                            nm-errors                 n/a
data-get-task-output-control-request                                                    nm-sdk-protocol           n/a
data-github-actions-workflow-for-claude-mentions                                        nm-errors                 n/a
data-github-app-installation-pr-description                                             nm-errors                 n/a
data-hook-classifier-context-field                                                      nm-sdk-protocol           n/a
data-hook-mcp-server-field                                                              nm-sdk-protocol           n/a
data-interrupt-cancel-queued-parameter                                                  nm-sdk-protocol           n/a
data-interrupt-receipt-cancelled-field                                                  nm-sdk-protocol           n/a
data-interrupt-receipt-still-queued-field                                               nm-sdk-protocol           n/a
data-interrupt-send-now-message-uuid-parameter                                          nm-sdk-protocol           n/a
data-interrupt-send-now-parameter                                                       nm-sdk-protocol           n/a
data-interrupt-worker-epoch-parameter                                                   nm-sdk-protocol           n/a
data-knowledge-mcp-search-strategies                                                    claude-ai-ecosystem       n/a
data-managed-agents-quickstart-bug-hunter                                               claude-api-refs           n/a
data-managed-agents-quickstart-contract-tracker                                         claude-api-refs           n/a
data-managed-agents-quickstart-data-analyst                                             claude-api-refs           n/a
data-managed-agents-quickstart-deep-researcher                                          claude-api-refs           n/a
data-managed-agents-quickstart-explainer-video-maker                                    claude-api-refs           n/a
data-managed-agents-quickstart-field-monitor                                            claude-api-refs           n/a
data-managed-agents-quickstart-incident-commander                                       claude-api-refs           n/a
data-managed-agents-quickstart-sprint-retro-facilitator                                 claude-api-refs           n/a
data-managed-agents-quickstart-structured-extractor                                     claude-api-refs           n/a
data-managed-agents-quickstart-support-agent                                            claude-api-refs           n/a
data-managed-agents-quickstart-support-to-eng-escalator                                 claude-api-refs           n/a
data-managed-agents-quickstart-watchlist-scanner                                        claude-api-refs           n/a
data-managed-settings-helper-onfailure-field                                            nm-settings               n/a
data-managed-settings-helper-retries-field                                              nm-settings               n/a
data-managed-settings-x-whenunreadable-field                                            nm-settings               n/a
data-mcp-http-client-capability-validation-rationale                                    nm-sdk-protocol           n/a
data-mcp-http-parameter-header-validation-rationale                                     nm-sdk-protocol           n/a
data-mcp-http-standard-header-validation-rationale                                      nm-sdk-protocol           n/a
data-mcp-read-resource-control-request                                                  nm-sdk-protocol           n/a
data-mcp-server-status-error-code-field                                                 nm-sdk-protocol           n/a
data-mcp-status-control-request-livereply-field                                         nm-sdk-protocol           n/a
data-message-batches-api-reference-c                                                    claude-api-refs           n/a
data-message-batches-api-reference-php                                                  claude-api-refs           n/a
data-message-batches-api-reference-python                                               claude-api-refs           n/a
data-message-batches-api-typescript                                                     claude-api-refs           n/a
data-network-path-settings-file-unread-recovery-guidance                                nm-errors                 n/a
data-opentelemetry-tracing-reference-c                                                  claude-api-refs           n/a
data-opentelemetry-tracing-reference-go                                                 claude-api-refs           n/a
data-opentelemetry-tracing-reference-java                                               claude-api-refs           n/a
data-opentelemetry-tracing-reference-php                                                claude-api-refs           n/a
data-opentelemetry-tracing-reference-python                                             claude-api-refs           n/a
data-opentelemetry-tracing-reference-ruby                                               claude-api-refs           n/a
data-opentelemetry-tracing-reference-typescript                                         claude-api-refs           n/a
data-peer-sender-display-name-field                                                     nm-sdk-protocol           n/a
data-pending-elicitation-requests-in-initialization-response                            nm-sdk-protocol           n/a
data-plugin-manifest-types-field                                                        nm-settings               n/a
data-prompt-suggestions-paused-control-request                                          nm-sdk-protocol           n/a
data-query-result-pending-command-count                                                 nm-sdk-protocol           n/a
data-rate-limit-grace-signal                                                            nm-sdk-protocol           n/a
data-rate-limit-unified-windows                                                         nm-sdk-protocol           n/a
data-remote-tools-reannounce-control-request                                            remote-machines           n/a
data-review-upload-excluded-changes-error                                               nm-errors                 n/a
data-rewind-files-skippedlinks-field                                                    nm-sdk-protocol           n/a
data-sandbox-credential-environment-no-match-setting                                    nm-settings               n/a
data-sandbox-credential-file-mask-claims-setting                                        nm-settings               n/a
data-sandbox-credential-mask-empty-injecthosts-warning                                  nm-errors                 n/a
data-sandbox-deniedresolvedaddresses-setting                                            nm-settings               n/a
data-sandbox-excludedcommands-setting                                                   nm-settings               n/a
data-sandbox-filesystem-disabled-setting                                                nm-settings               n/a
data-sandbox-network-domain-spelling-warning                                            nm-errors                 n/a
data-sandbox-project-settings-ignored-under-higher-priority-policy                      nm-errors                 n/a
data-sandbox-root-without-cap-setfcap-error                                             nm-errors                 n/a
data-sandbox-tls-termination-setting                                                    nm-settings               n/a
data-sandbox-worktrees-directory-listing-failure-warning                                nm-errors                 n/a
data-sdk-api-error-kind-field                                                           nm-sdk-protocol           n/a
data-sdk-api-error-params-field                                                         nm-sdk-protocol           n/a
data-sdk-command-lifecycle-scheduled-for-field                                          nm-sdk-protocol           n/a
data-sdk-footer-indicator-schema                                                        nm-sdk-protocol           n/a
data-sdk-frame-intake-phases-ms-field                                                   nm-sdk-protocol           n/a
data-sdk-frame-received-wall-ms-field                                                   nm-sdk-protocol           n/a
data-sdk-initialize-response-sdk-mcp-manifests-parked-field                             nm-sdk-protocol           n/a
data-sdk-mcp-server-disableautobackground-field                                         nm-sdk-protocol           n/a
data-sdk-message-stop-abandoned-blocks-field                                            nm-sdk-protocol           n/a
data-sdk-permission-check-status-event-schema                                           nm-sdk-protocol           n/a
data-sdk-read-file-anchor-as-written-kind                                               nm-sdk-protocol           n/a
data-sdk-read-file-anchor-field                                                         nm-sdk-protocol           n/a
data-sdk-result-main-stream-chunk-count-field                                           nm-sdk-protocol           n/a
data-sdk-result-model-usage-since-last-result-field                                     nm-sdk-protocol           n/a
data-sdk-result-process-model-request-turn-count-field                                  nm-sdk-protocol           n/a
data-sdk-result-process-streaming-cpu-time-field                                        nm-sdk-protocol           n/a
data-sdk-result-runtime-tuning-environment-bitmask-field                                nm-sdk-protocol           n/a
data-sdk-result-safety-stops-field                                                      nm-sdk-protocol           n/a
data-sdk-result-startup-garbage-collection-counts-field                                 nm-sdk-protocol           n/a
data-sdk-set-max-thinking-tokens-request-schema                                         nm-sdk-protocol           n/a
data-sdk-set-model-error-response-model-field                                           nm-sdk-protocol           n/a
data-sdk-set-model-only-if-model-is-current-field                                       nm-sdk-protocol           n/a
data-sdk-set-model-success-payload-model-field                                          nm-sdk-protocol           n/a
data-sdk-set-model-system-prompt-field                                                  nm-sdk-protocol           n/a
data-sdk-system-init-plugin-errors-field                                                nm-sdk-protocol           n/a
data-sdk-task-notification-handback-field                                               nm-sdk-protocol           n/a
data-sdk-thinking-duration-ms-field                                                     nm-sdk-protocol           n/a
data-sdk-turn-handoff-required-rewind-request-id-field                                  nm-sdk-protocol           n/a
data-sdk-ui-attach-client-request-capabilities-field                                    nm-sdk-protocol           n/a
data-sdk-ui-prompt-autocomplete-request-schema                                          nm-sdk-protocol           n/a
data-sdk-ui-prompt-paint-request-schema                                                 nm-sdk-protocol           n/a
data-sdk-user-message-requeue-if-interrupted-within-ms-field                            nm-sdk-protocol           n/a
data-self-hosted-runner-anthropic-git-proxy-credential-warning                          self-hosted-runner        n/a
data-self-hosted-runner-client-certificate-relay-warning                                self-hosted-runner        n/a
data-self-hosted-runner-decode-token-command-help                                       self-hosted-runner        n/a
data-self-hosted-runner-deferred-shutdown-timing-notice                                 self-hosted-runner        n/a
data-self-hosted-runner-diff-finds-nested-repository-exec-path-notice                   self-hosted-runner        n/a
data-self-hosted-runner-disabled-by-organization-fatal-message                          self-hosted-runner        n/a
data-self-hosted-runner-git-askpass-governed-hosts-warning                              self-hosted-runner        n/a
data-self-hosted-runner-git-lfs-hook-warning                                            self-hosted-runner        n/a
data-self-hosted-runner-git-ssl-cainfo-trust-bundle-warning                             self-hosted-runner        n/a
data-self-hosted-runner-git-ssl-no-verify-lifecycle-hook-exception                      self-hosted-runner        n/a
data-self-hosted-runner-git-ssl-no-verify-session-relocation-note                       self-hosted-runner        n/a
data-self-hosted-runner-orchestrator-command-help                                       self-hosted-runner        n/a
data-self-hosted-runner-session-metrics-help                                            self-hosted-runner        n/a
data-self-hosted-runner-unreadable-character-validation-guidance                        self-hosted-runner        n/a
data-server-classifier-beta-rejection-warning                                           nm-errors                 n/a
data-sshhostcommands-setting                                                            nm-settings               n/a
data-streaming-reference-c                                                              claude-api-refs           n/a
data-streaming-reference-go                                                             claude-api-refs           n/a
data-streaming-reference-java                                                           claude-api-refs           n/a
data-streaming-reference-php                                                            claude-api-refs           n/a
data-streaming-reference-ruby                                                           claude-api-refs           n/a
data-structured-tool-output-field-schema                                                nm-sdk-protocol           n/a
data-structured-usage-rate-limit-rows-field                                             nm-sdk-protocol           n/a
data-submit-feedback-control-request                                                    nm-sdk-protocol           n/a
data-syncclaudeaiplugins-setting                                                        nm-settings               n/a
data-syncclaudeaiskills-setting                                                         nm-settings               n/a
data-telemetry-variables-ignored-notice                                                 nm-errors                 n/a
data-thin-client-diff-dialog-schema                                                     nm-sdk-protocol           n/a
data-token-counting-reference                                                           claude-api-refs           n/a
data-tool-result-display-metadata-field                                                 nm-sdk-protocol           n/a
data-tool-use-display-metadata-field                                                    nm-sdk-protocol           n/a
data-tool-use-reference-c                                                               claude-api-refs           n/a
data-tool-use-reference-ruby                                                            claude-api-refs           n/a
data-turn-handoff-available-event-schema                                                nm-sdk-protocol           n/a
data-turn-handoff-file-names-field                                                      nm-sdk-protocol           n/a
data-turn-handoff-hydrates-carried-lines-field                                          nm-sdk-protocol           n/a
data-turn-handoff-memory-context-field                                                  nm-sdk-protocol           n/a
data-ultrathink-availability-in-session-initialization                                  nm-sdk-protocol           n/a
data-untrusted-folder-environment-variables-ignored-warning                             nm-errors                 n/a
data-upload-device-hook-template-request                                                nm-sdk-protocol           n/a
data-vcs-state-changed-branch-field                                                     nm-sdk-protocol           n/a
data-vcs-state-changed-event-schema                                                     nm-sdk-protocol           n/a
data-windows-protected-path-resolution-denial                                           nm-errors                 n/a
data-windows-transcript-read-ebadf-notice                                               nm-errors                 n/a
data-working-tree-upload-refusal-for-duplicated-withheld-file                           nm-errors                 n/a
data-working-tree-upload-refusal-for-git-dubious-ownership                              nm-errors                 n/a
data-working-tree-upload-refusal-for-misplaced-git-entry                                nm-errors                 n/a
data-working-tree-upload-refusal-for-missing-git-objects-folders                        nm-errors                 n/a
data-working-tree-upload-refusal-for-private-git-directory-setup-failure                nm-errors                 n/a
data-working-tree-upload-refusal-for-unread-git-config-environment-variable             nm-errors                 n/a
data-worktree-creation-submodule-on-demand-fetch-disabled-error                         nm-errors                 n/a
data-worktree-creation-unsafe-promisor-remote-error                                     nm-errors                 n/a
skill-artifact-diagramming                                                              art-types-design          M4
skill-claude-api-deprecated-or-retired-models-notice                                    claude-api-refs           n/a
skill-claude-api-managed-agents-onboarding-from-url-unavailable                         claude-api-refs           n/a
skill-claude-api-managed-agents-onboarding-source-tier                                  claude-api-refs           n/a
skill-claude-code-configuration-guide                                                   self-help                 M4
skill-claude-guide-unavailable-reference-fallback                                       self-help                 M4
skill-claude-test-sign-in                                                               claude-test               n/a
skill-code-review-angle-b-removed-behavior-auditor                                      code-review               M4
skill-code-review-angle-c-cross-file-tracer                                             code-review               M4
skill-code-review-angle-d-language-pitfall-specialist                                   code-review               M4
skill-code-review-angle-e-wrapper-proxy-correctness                                     code-review               M4
skill-code-review-conventions-dimension                                                 code-review               M4
skill-code-review-correctness-finder-angles                                             code-review               M4
skill-code-review-efficiency-dimension                                                  code-review               M4
skill-code-review-inline-medium-high-template                                           code-review               M4
skill-code-review-inline-xhigh-mode                                                     code-review               M4
skill-code-review-low-effort-expanded-findings-mode                                     code-review               variant
skill-code-review-output-findings-json-array                                            code-review               M4
skill-code-review-phase-0-gather-the-diff                                               code-review               M4
skill-code-review-phase-2-verify-3-state                                                code-review               M4
skill-code-review-phase-2-verify-recall-biased                                          code-review               M4
skill-code-review-phase-3-sweep-for-gaps                                                code-review               M4
skill-computer-use-mcp                                                                  browser-computer          n/a
skill-data-visualization                                                                dataviz-design            M4
skill-data-visualization-description                                                    dataviz-design            M4
skill-debugging                                                                         self-help                 M4
skill-doctor-prompt-audit-configuration-scope                                           self-help                 M4
skill-doctor-slash-command                                                              self-help                 M4
skill-doctor-slash-command-description                                                  self-help                 M4
skill-doctor-usage-flagged-check                                                        insights                  M4
skill-dynamic-pacing-loop-execution                                                     loops                     M4
skill-dynamic-pacing-loop-execution-re-arm-decision                                     loops                     M4
skill-explain-usage-measured-usage-slash-command                                        insights                  M4
skill-explain-usage-slash-command                                                       insights                  M4
skill-generate-permission-allowlist-from-transcripts                                    self-help                 M4
skill-import-to-claude-code                                                             claude-ai-ecosystem       n/a
skill-init-claude-md-and-skill-setup-new-version                                        repo-setup                M2
skill-insights-report-output                                                            insights                  M4
skill-loop-cloud-first-scheduling-offer                                                 cloud-schedule            n/a
skill-loop-local-runtime-note                                                           loops                     M4
skill-loop-self-pacing-mode                                                             loops                     M4
skill-loop-self-pacing-mode-re-arm-decision                                             loops                     M4
skill-loop-slash-command-dynamic-mode                                                   loops                     M4
skill-mod-github-sharing-consent                                                        claude-ai-ecosystem       n/a
skill-mod-plugin-packaging-any-plan                                                     claude-ai-ecosystem       n/a
skill-mod-plugin-packaging-consent                                                      claude-ai-ecosystem       n/a
skill-mod-plugin-packaging-organization-plans                                           claude-ai-ecosystem       n/a
skill-mod-plugin-packaging-personal-plans                                               claude-ai-ecosystem       n/a
skill-plugin-eval-authoring-interview                                                   claude-ai-ecosystem       n/a
skill-run-app                                                                           run-verify                M4
skill-run-browser-driven-web-app-example                                                run-verify                M4
skill-run-cli-tool-example                                                              run-verify                M4
skill-run-library-sdk-example                                                           run-verify                M4
skill-run-skill-template                                                                run-verify                M4
skill-run-tui-interactive-terminal-app-example                                          run-verify                M4
skill-run-web-server-api-example                                                        run-verify                M4
skill-schedule-recurring-cron-and-run-immediately                                       loops                     M4
skill-send-to-project-brief-drafting-guidance                                           projects-inbox            n/a
skill-send-to-project-message-preload-description                                       projects-inbox            n/a
skill-send-to-project-message-slash-command                                             projects-inbox            n/a
skill-send-to-project-work-brief-preload-description                                    projects-inbox            n/a
skill-send-to-project-work-brief-slash-command                                          projects-inbox            n/a
skill-setup-cowork                                                                      claude-ai-ecosystem       n/a
skill-setup-cowork-role-selection                                                       claude-ai-ecosystem       n/a
skill-stuck-background-daemon-diagnostics                                               self-help                 M4
skill-stuck-slash-command                                                               self-help                 M4
skill-team-onboarding-guide                                                             team-onboarding           M4
skill-update-claude-code-config                                                         self-help                 M4
skill-update-config-7-step-verification-flow                                            self-help                 M4
skill-update-config-description                                                         self-help                 M4
skill-update-config-settings-file-locations                                             self-help                 M4
skill-verify-cli-changes-example-for-verify-skill                                       run-verify                M4
skill-verify-server-api-changes-example-for-verify-skill                                run-verify                M4
skill-workflow-authoring-reference                                                      workflow                  M4
system-prompt-act-when-ready                                                            sys-doing-tasks           have
system-prompt-action-safety-and-truthful-reporting                                      sys-safety                have
system-prompt-advisor-tool-instructions                                                 misc-product-tools        M4
system-prompt-agent-intro-collaborative-goals                                           sys-core                  have
system-prompt-agent-summary-generation                                                  titles-labels             M3
system-prompt-agent-thread-notes                                                        builtin-agents            M2
system-prompt-analysis-instructions-for-full-compact-prompt-recent-messages             compaction                M2
system-prompt-artifact-comment-decision-reformat-retry                                  art-comments              M4
system-prompt-artifact-comment-edit-composer                                            art-comments              M4
system-prompt-artifact-comment-fast-acknowledgement                                     art-comments              M4
system-prompt-artifact-comment-fast-acknowledgement-selection                           art-comments              M4
system-prompt-artifact-comment-list-framing                                             art-comments              M4
system-prompt-artifact-comment-presence-state-guidance                                  art-comments              M4
system-prompt-artifact-comment-reply-composer                                           art-comments              M4
system-prompt-artifact-comment-result-guidance                                          art-comments              M4
system-prompt-artifact-comment-thread-framing                                           art-comments              M4
system-prompt-artifact-comment-thread-triage                                            art-comments              M4
system-prompt-artifact-commenter-access-guidance                                        art-comments              M4
system-prompt-artifact-page-owned-comment-thread-guidance                               art-comments              M4
system-prompt-artifact-patch-turn-edit-composer                                         art-publish               M4
system-prompt-auto-mode                                                                 auto-mode-main            M2
system-prompt-auto-mode-setup-proposal-generator                                        auto-mode-setup           M2
system-prompt-auto-mode-slack-message-provenance                                        projects-inbox            n/a
system-prompt-autonomous-loop-check                                                     loops                     M4
system-prompt-autonomous-loop-notification-guidance                                     loops                     M4
system-prompt-autonomous-loop-persistence-guidance-claude-code-loop-persistent          loops                     M4
system-prompt-autonomous-loop-tick                                                      loops                     M4
system-prompt-autonomous-loop-tick-dynamic-pacing                                       loops                     M4
system-prompt-autonomous-operation-guidelines                                           auto-mode-main            M2
system-prompt-avoiding-unnecessary-sleep-commands-part-of-powershell-tool-description   powershell                M4
system-prompt-background-agent-session-keepalive-guidance                               background-sessions       M4
system-prompt-background-session-instructions                                           background-sessions       M4
system-prompt-background-session-instructions-lean-rewrite                              background-sessions       variant
system-prompt-background-session-worktree-isolation-lean-rewrite                        worktrees                 variant
system-prompt-background-session-worktree-persistence-guidance                          worktrees                 M4
system-prompt-background-worktree-isolation-guidance                                    worktrees                 M4
system-prompt-background-worktree-persistence-lean-rewrite                              worktrees                 variant
system-prompt-censoring-assistance-with-malicious-activities                            sys-safety                have
system-prompt-chrome-browser-mcp-tools                                                  bx-setup                  variant
system-prompt-claude-fable-5-1-model-identity                                           sys-model-identity        n/a
system-prompt-claude-fable-5-model-identity                                             sys-model-identity        n/a
system-prompt-claude-in-chrome-browser-automation                                       bx-safety                 M4
system-prompt-claude-in-chrome-browser-automation-non-deferred-tools                    bx-safety                 M4
system-prompt-claude-in-chrome-browser-selection-instructions                           bx-setup                  M4
system-prompt-claude-in-chrome-deferred-tool-loading                                    bx-setup                  M4
system-prompt-claude-in-chrome-task-specific-tool-loading                               bx-setup                  M4
system-prompt-combined-memory-index-pointer-instructions                                auto-memory               M4
system-prompt-comment-what-and-task-context-avoidance                                   sys-doing-tasks           have
system-prompt-comment-why-only-guidance                                                 sys-doing-tasks           have
system-prompt-communication-style                                                       sys-tone                  have
system-prompt-compaction-omitted-tool-result-handling                                   compaction                M2
system-prompt-concise-output-style                                                      output-styles             have
system-prompt-context-compaction-summary                                                compaction                variant
system-prompt-context-management-lean-rewrite                                           sys-tool-policy           variant
system-prompt-coordinator-cross-session-peer-guidance                                   teams                     M4
system-prompt-coordinator-mode-orchestration                                            teams                     M4
system-prompt-correction-restraint                                                      sys-doing-tasks           have
system-prompt-delivering-requested-work-lean-rewrite                                    sys-doing-tasks           variant
system-prompt-delivering-work-at-full-scope                                             sys-doing-tasks           have
system-prompt-deny-rule-circumvention-classifier-guidance                               auto-mode-classifier      have
system-prompt-description-part-of-memory-instructions                                   auto-memory               M4
system-prompt-doing-tasks-ambitious-tasks                                               sys-doing-tasks           have
system-prompt-doing-tasks-help-and-feedback                                             sys-doing-tasks           have
system-prompt-doing-tasks-no-compatibility-hacks                                        sys-doing-tasks           have
system-prompt-doing-tasks-no-unnecessary-additions                                      sys-doing-tasks           have
system-prompt-doing-tasks-no-unnecessary-error-handling                                 sys-doing-tasks           have
system-prompt-doing-tasks-security                                                      sys-doing-tasks           have
system-prompt-doing-tasks-software-engineering-focus                                    sys-doing-tasks           have
system-prompt-dream-claude-md-memory-reconciliation                                     memory-jobs               M4
system-prompt-dream-team-memory-handling                                                memory-jobs               M4
system-prompt-emoji-avoidance                                                           sys-tone                  have
system-prompt-executing-actions-with-care                                               sys-safety                have
system-prompt-explain-code-review-ultra                                                 code-review               n/a
system-prompt-exploratory-questions-analyze-before-implementing                         sys-doing-tasks           have
system-prompt-feedback-memory-body-structure                                            auto-memory               M4
system-prompt-feedback-memory-save-guidance                                             auto-memory               M4
system-prompt-finish-the-request-instead-of-stopping-early                              sys-doing-tasks           have
system-prompt-focus-mode-lean-rewrite                                                   visibility-modes          variant
system-prompt-focus-mode-long-form                                                      visibility-modes          M4
system-prompt-focus-mode-short-form                                                     visibility-modes          M4
system-prompt-fork-usage-guidelines                                                     delegation                M2
system-prompt-forked-agent-guidance                                                     delegation                M2
system-prompt-forked-conversation-worktree-isolation-guidance                           worktrees                 M4
system-prompt-fresh-subagent-delegation-example                                         delegation                M2
system-prompt-frontend-browser-verification                                             sys-doing-tasks           M4
system-prompt-git-status                                                                sys-env                   have
system-prompt-handling-corrections-lean-rewrite                                         sys-doing-tasks           variant
system-prompt-harness-instructions                                                      sys-core                  have
system-prompt-harness-instructions-lean-rewrite                                         sys-core                  variant
system-prompt-hook-evaluator-truncated-transcript-note                                  hooks                     M2
system-prompt-hook-feedback-handling                                                    hooks                     M2
system-prompt-hooks-configuration                                                       hooks                     M4
system-prompt-how-to-use-the-sendusermessage-tool                                       visibility-modes          M4
system-prompt-insights-at-a-glance-summary                                              insights                  M4
system-prompt-insights-friction-analysis                                                insights                  M4
system-prompt-insights-interaction-style                                                insights                  M4
system-prompt-insights-memorable-moment                                                 insights                  M4
system-prompt-insights-on-the-horizon                                                   insights                  M4
system-prompt-insights-session-facets-extraction                                        insights                  M4
system-prompt-insights-suggestions                                                      insights                  M4
system-prompt-insights-summary-at-a-glance                                              insights                  variant
system-prompt-insights-what-works                                                       insights                  M4
system-prompt-interactive-agent-intro-output-style-active                               output-styles             have
system-prompt-interactive-agent-intro-output-style-conditional                          output-styles             have
system-prompt-irreversible-action-caution-lean-rewrite                                  sys-safety                variant
system-prompt-learning-mode                                                             output-styles             have
system-prompt-learning-mode-insights                                                    output-styles             have
system-prompt-loop-tick-loop-md-absent-dynamic-pacing                                   loops                     M4
system-prompt-loop-tick-loop-md-tasks                                                   loops                     M4
system-prompt-loop-tick-loop-md-tasks-dynamic-pacing                                    loops                     M4
system-prompt-memory-description-of-user-feedback                                       auto-memory               M4
system-prompt-memory-hygiene-lean-rewrite                                               auto-memory               variant
system-prompt-memory-index-pointer-instructions                                         auto-memory               M4
system-prompt-memory-instructions                                                       auto-memory               M4
system-prompt-memory-persistence-scope                                                  auto-memory               M4
system-prompt-memory-save-exclusions                                                    auto-memory               M4
system-prompt-minimal-mode                                                              visibility-modes          M4
system-prompt-monitor-fallback-heartbeat-guidance                                       loops                     M4
system-prompt-moved-artifact-comment-thread-guidance                                    art-comments              M4
system-prompt-non-fork-subagent-delegation-examples                                     delegation                M2
system-prompt-one-of-six-rules-for-using-sleep-command                                  bash-core                 have
system-prompt-option-previewer                                                          ask-user                  M3
system-prompt-outcome-first-communication-style                                         sys-tone                  have
system-prompt-outcome-first-communication-style-lean-rewrite                            sys-tone                  variant
system-prompt-parallel-tool-call-note-part-of-tool-usage-policy                         sys-tool-policy           have
system-prompt-partial-compaction-instructions                                           compaction                M2
system-prompt-pasted-content-instruction-trust-boundary                                 sys-safety                have
system-prompt-permission-classifier-strict-review-guidance                              auto-mode-classifier      have
system-prompt-personal-project-memory-description                                       auto-memory               M4
system-prompt-phase-four-of-plan-mode                                                   plan-mode                 have
system-prompt-plan-sent-to-ultraplan                                                    ultraplan                 n/a
system-prompt-plan-vs-memory-guidance                                                   auto-memory               M4
system-prompt-powershell-edition-for-5-1                                                powershell                M4
system-prompt-powershell-edition-for-7                                                  powershell                M4
system-prompt-powershell-edition-unknown                                                powershell                M4
system-prompt-pr-slack-notification-step                                                projects-inbox            n/a
system-prompt-pr-steward-handoff-guidance                                               projects-inbox            n/a
system-prompt-prefer-editing-existing-files                                             sys-doing-tasks           have
system-prompt-project-coordinator-relay-trust-boundary                                  projects-inbox            n/a
system-prompt-project-coordinator-reply-consent                                         projects-inbox            n/a
system-prompt-project-member-session-user-message-provenance                            projects-inbox            n/a
system-prompt-project-memory-body-structure                                             auto-memory               M4
system-prompt-project-memory-save-guidance                                              auto-memory               M4
system-prompt-project-skill-upkeep-for-feedback-memory                                  auto-memory               M4
system-prompt-project-thread-in-thread-message-provenance                               projects-inbox            n/a
system-prompt-project-timeline-user-message-provenance                                  projects-inbox            n/a
system-prompt-published-artifact-inline-edits-and-large-change-republish                art-publish               M4
system-prompt-published-artifact-same-response-edit-and-republish                       art-publish               M4
system-prompt-remote-plan-mode-ultraplan                                                ultraplan                 n/a
system-prompt-remote-planning-session                                                   ultraplan                 n/a
system-prompt-reporting-outcomes                                                        sys-doing-tasks           have
system-prompt-respond-in-configured-language                                            sys-tone                  M2
system-prompt-responsive-mode                                                           visibility-modes          M4
system-prompt-sandbox-task-resource-boundary                                            sandbox                   M4
system-prompt-saving-skills-via-file-delivery                                           claude-ai-ecosystem       n/a
system-prompt-self-hosted-runner-doctor                                                 self-hosted-runner        n/a
system-prompt-self-hosted-runner-setup                                                  self-hosted-runner        n/a
system-prompt-send-to-project-coordinator-handoff                                       projects-inbox            n/a
system-prompt-session-scratchpad-temporary-file-guidance                                sys-tool-policy           have
system-prompt-shared-git-stash-safety                                                   sys-safety                have
system-prompt-skill-save-permission-note                                                claude-ai-ecosystem       n/a
system-prompt-skillify-current-session                                                  skills                    M4
system-prompt-subagent-carried-user-consent-history                                     builtin-agents            M2
system-prompt-subagent-delegation-cost-guidance                                         delegation                M2
system-prompt-subagent-delegation-examples                                              delegation                M2
system-prompt-subagent-delegation-restraint                                             delegation                M2
system-prompt-system-reminder-interpretation-lean-rewrite                               sys-tool-policy           variant
system-prompt-system-section                                                            sys-core                  have
system-prompt-task-approval-continuity                                                  sys-doing-tasks           have
system-prompt-tasks-vs-memory-guidance                                                  auto-memory               M4
system-prompt-team-memory-index-pointer-instructions                                    auto-memory               M4
system-prompt-team-project-memory-description                                           auto-memory               M4
system-prompt-teammate-communication                                                    teams                     M4
system-prompt-tone-and-style-code-references                                            sys-tone                  have
system-prompt-tone-and-style-concise-output-short                                       sys-tone                  have
system-prompt-tool-call-colon-avoidance                                                 sys-tone                  have
system-prompt-tool-call-summary-label                                                   titles-labels             M3
system-prompt-tool-usage-subagent-guidance                                              delegation                M2
system-prompt-tool-usage-task-management                                                sys-tool-policy           have
system-prompt-troubleshooting-confirmation-policy                                       sys-safety                have
system-prompt-user-memory-usage-guidance                                                auto-memory               M4
system-prompt-verified-slack-human-relay-consent                                        projects-inbox            n/a
system-prompt-windows-sandbox-certificate-recovery                                      sandbox                   M4
system-prompt-windows-sandbox-git-credentials-unavailable                               sandbox                   M4
system-prompt-windows-sandbox-git-folder-above-working-directory                        sandbox                   M4
system-prompt-windows-sandbox-localhost-access-local-binding-off                        sandbox                   M4
system-prompt-windows-sandbox-localhost-access-local-binding-on                         sandbox                   M4
system-prompt-windows-sandbox-localhost-retry-guidance                                  sandbox                   M4
system-prompt-windows-sandbox-localhost-user-alternatives                               sandbox                   M4
system-prompt-windows-sandbox-localhost-workarounds-local-binding-off                   sandbox                   M4
system-prompt-windows-sandbox-package-cache-limits                                      sandbox                   M4
system-prompt-windows-sandbox-placeholder-file-protection                               sandbox                   M4
system-prompt-windows-sandbox-profile-tool-retry-guidance                               sandbox                   M4
system-prompt-windows-sandbox-profile-tool-user-guidance                                sandbox                   M4
system-prompt-windows-sandbox-pytest-cache-hang                                         sandbox                   M4
system-prompt-windows-sandbox-repository-creation-prohibition                           sandbox                   M4
system-prompt-windows-sandbox-schannel-tls-retry-guidance                               sandbox                   M4
system-prompt-windows-sandbox-schannel-tls-user-alternatives                            sandbox                   M4
system-prompt-windows-sandbox-server-reachability-local-binding-on                      sandbox                   M4
system-prompt-worker-instructions                                                       teams                     M4
system-prompt-writing-for-the-user                                                      sys-tone                  have
system-prompt-writing-subagent-prompts                                                  delegation                M2
system-prompt-wsl-managed-settings-double-opt-in                                        sys-env                   n/a
system-prompt-you-should-know-understood-topic-skip-guidance                            recaps-suggestions        n/a
system-reminder-agent-mention                                                           delegation                M2
system-reminder-app-tool-server-retrying-connection-failure                             mcp-deferred              n/a
system-reminder-appifactrepl-design-canvas-workflow                                     art-types-design          M4
system-reminder-appifactrepl-slides-deck-workflow                                       art-types-design          M4
system-reminder-artifact-auto-replies-resumed                                           art-comments              M4
system-reminder-artifact-capabilities-contract-unavailable                              art-data-assets           M4
system-reminder-artifact-capability-declaration-revocation-warning                      art-data-assets           M4
system-reminder-artifact-comment-reply-activation-failure                               art-comments              M4
system-reminder-artifact-comment-reply-session-collision                                art-comments              M4
system-reminder-artifact-comment-wakes-stopped-by-user                                  art-comments              M4
system-reminder-artifact-file-path-refusal-for-earlier-wrong-path-publication           art-publish               M4
system-reminder-artifact-file-path-refusal-for-served-path-claimed-by-another-file      art-publish               M4
system-reminder-artifact-file-path-refusal-for-unexpected-new-served-path               art-publish               M4
system-reminder-artifact-page-owned-comment-reply-refusal                               art-comments              M4
system-reminder-artifact-published-source-edit-guidance                                 art-publish               M4
system-reminder-artifact-type-instructions-trust-boundary                               art-types-design          M4
system-reminder-artifact-type-page-untrusted-content-warning                            art-types-design          M4
system-reminder-askuserquestion-minimum-options-validation                              ask-user                  M2
system-reminder-async-agent-launched                                                    delegation                M2
system-reminder-async-agent-launched-metadata                                           delegation                M2
system-reminder-attached-device-stopped-offering-tools                                  remote-machines           n/a
system-reminder-attached-machine-asleep-command-paused                                  remote-machines           n/a
system-reminder-attached-machine-asleep-command-withdrawn                               remote-machines           n/a
system-reminder-attached-machine-away-recheck-on-request-ending                         remote-machines           n/a
system-reminder-attached-machine-away-until-reachable-condition                         remote-machines           n/a
system-reminder-attached-machine-could-not-be-reached                                   remote-machines           n/a
system-reminder-attached-machine-could-not-be-reached-asleep                            remote-machines           n/a
system-reminder-attached-machine-could-not-be-reached-busy                              remote-machines           n/a
system-reminder-attached-machine-could-not-be-reached-cause-unknown                     remote-machines           n/a
system-reminder-attached-machine-could-not-be-reached-oversized-call                    remote-machines           n/a
system-reminder-attached-machine-could-not-be-reached-signed-off                        remote-machines           n/a
system-reminder-attached-machine-could-not-be-reached-went-silent                       remote-machines           n/a
system-reminder-attached-machine-idle-disconnect-notice                                 remote-machines           n/a
system-reminder-attached-machine-not-answering                                          remote-machines           n/a
system-reminder-attached-machine-not-answering-retry-shortly                            remote-machines           n/a
system-reminder-attached-machine-not-answering-stop-calling                             remote-machines           n/a
system-reminder-attached-machine-reply-not-received                                     remote-machines           n/a
system-reminder-attached-machine-signed-off-likely-causes                               remote-machines           n/a
system-reminder-attached-machine-signed-off-notice                                      remote-machines           n/a
system-reminder-attached-machine-stopped-answering                                      remote-machines           n/a
system-reminder-attached-machine-stopped-answering-return-not-noted                     remote-machines           n/a
system-reminder-attached-machine-stopped-answering-return-noted                         remote-machines           n/a
system-reminder-attached-machine-untrusted-attachments-refusal                          remote-machines           n/a
system-reminder-attached-machine-went-to-sleep                                          remote-machines           n/a
system-reminder-attached-machine-went-to-sleep-return-not-noted                         remote-machines           n/a
system-reminder-attached-machine-went-to-sleep-return-noted                             remote-machines           n/a
system-reminder-auto-mode-clarification-bias                                            auto-mode-main            M2
system-reminder-auto-mode-consent-flow                                                  auto-mode-main            M2
system-reminder-auto-mode-deny-rules-without-safety-result                              auto-mode-main            M2
system-reminder-auto-mode-finish-exposed-work                                           auto-mode-main            M2
system-reminder-auto-mode-no-verdict-after-another-hook-rewrite                         auto-mode-main            M2
system-reminder-auto-mode-no-verdict-after-hook-input-rewrite                           auto-mode-main            M2
system-reminder-background-command-quiet-check-in                                       background-tasks          M2
system-reminder-background-command-stopped-under-memory-pressure                        background-tasks          M4
system-reminder-background-mcp-call-stopping-session-warning                            background-tasks          M4
system-reminder-background-task-notification-with-concurrent-user-input                 background-tasks          M2
system-reminder-bound-conversation-activity-authority-warning                           projects-inbox            n/a
system-reminder-brief-mode-toggle                                                       visibility-modes          M4
system-reminder-brief-mode-user-facing-output                                           visibility-modes          M4
system-reminder-browser-extension-not-connected                                         bx-setup                  M4
system-reminder-browser-extension-not-connected-mobile-app                              browser-computer          n/a
system-reminder-btw-side-question                                                       recaps-suggestions        M3
system-reminder-claude-doc-comment-wake-guidance                                        art-comments              M4
system-reminder-cloud-agent-launched                                                    remote-machines           n/a
system-reminder-cloud-sandbox-sustained-cpu-limit-stop                                  remote-machines           n/a
system-reminder-cloud-session-device-tools-disabled                                     remote-machines           n/a
system-reminder-cloud-session-device-tools-not-yet-available                            remote-machines           n/a
system-reminder-cloud-session-settings-edit-not-applied                                 remote-machines           n/a
system-reminder-compact-file-reference                                                  compaction                M2
system-reminder-conversation-activity-awareness-and-authority-warning                   projects-inbox            n/a
system-reminder-cross-session-message-held-notice                                       teams                     M4
system-reminder-cross-session-peer-message-authority-warning                            teams                     M4
system-reminder-cross-session-peer-message-authority-warning-legacy-wording             teams                     variant
system-reminder-cross-session-peer-message-authority-warning-note                       teams                     M4
system-reminder-dangerous-removal-blocked                                               session-reminders         M2
system-reminder-deferred-tools-available                                                mcp-deferred              M2
system-reminder-end-conversation-background-fork-no-op                                  misc-product-tools        n/a
system-reminder-exited-plan-mode                                                        plan-mode                 have
system-reminder-external-source-trust-boundary                                          session-reminders         M2
system-reminder-file-already-in-context                                                 file-read-notices         M2
system-reminder-file-exists-but-empty                                                   file-read-notices         have
system-reminder-file-opened-in-ide                                                      ide-lsp                   M4
system-reminder-file-shorter-than-offset                                                file-read-notices         M2
system-reminder-file-summary-completeness-disclosure                                    file-read-notices         M2
system-reminder-file-truncated                                                          file-read-notices         M2
system-reminder-hook-additional-context                                                 hooks                     M2
system-reminder-hook-blocking-error                                                     hooks                     M2
system-reminder-hook-blocking-reason-git-bash-not-found-exit-code-127                   hooks                     M4
system-reminder-hook-stopped-continuation                                               hooks                     M2
system-reminder-hook-stopped-continuation-prefix                                        hooks                     M2
system-reminder-hook-success                                                            hooks                     M2
system-reminder-large-file-full-content-reading-guidance                                file-read-notices         M2
system-reminder-large-pdf-read-guidance                                                 file-read-notices         M4
system-reminder-lines-selected-in-ide                                                   ide-lsp                   M4
system-reminder-managed-agents-ant-command-refused-altered-transform-command            claude-api-refs           n/a
system-reminder-managed-agents-ant-command-refused-local-id-form-or-placement           claude-api-refs           n/a
system-reminder-managed-agents-ant-command-refused-local-servers-unsupported            claude-api-refs           n/a
system-reminder-managed-agents-ant-command-refused-staging-vrtx-or-bdrk-id              claude-api-refs           n/a
system-reminder-mcp-output-truncation-warning                                           mcp-deferred              M2
system-reminder-mcp-resource-no-content                                                 mcp-deferred              M2
system-reminder-mcp-server-connection-retry-guidance                                    mcp-deferred              M2
system-reminder-mcp-servers-connecting                                                  mcp-deferred              M2
system-reminder-mcp-servers-connecting-without-toolsearch                               mcp-deferred              variant
system-reminder-mcp-servers-failed-to-connect                                           mcp-deferred              M2
system-reminder-mcp-servers-failed-to-connect-guidance                                  mcp-deferred              M2
system-reminder-memory-consolidation-tool-constraints                                   memory-jobs               M4
system-reminder-memory-extraction-recent-context-only                                   memory-jobs               M4
system-reminder-memory-extraction-tool-constraints                                      memory-jobs               M4
system-reminder-memory-extraction-turn-budget                                           memory-jobs               M4
system-reminder-memory-file-contents                                                    memory-files              have
system-reminder-memory-file-contents-unverified-managed-folder                          memory-files              M2
system-reminder-memory-index-capacity-warning                                           auto-memory               M4
system-reminder-memory-sync-mass-deletion-guard                                         auto-memory               M4
system-reminder-memory-updates                                                          memory-files              M2
system-reminder-nested-instruction-file-contents                                        memory-files              M2
system-reminder-nested-memory-contents                                                  memory-files              M2
system-reminder-new-design-canvas-parallel-appifactrepl-workflow                        art-types-design          M4
system-reminder-new-design-canvas-two-step-appifactrepl-workflow                        art-types-design          M4
system-reminder-new-diagnostics-detected                                                ide-lsp                   M4
system-reminder-new-slides-deck-parallel-appifactrepl-workflow                          art-types-design          M4
system-reminder-new-slides-deck-two-step-appifactrepl-workflow                          art-types-design          M4
system-reminder-no-attached-machine-request-guidance                                    remote-machines           n/a
system-reminder-output-style-active                                                     output-styles             have
system-reminder-output-token-limit-continuation                                         session-reminders         M2
system-reminder-plan-approved                                                           plan-mode                 have
system-reminder-plan-awaiting-team-lead-approval                                        teams                     M4
system-reminder-plan-file-reference                                                     plan-mode                 M2
system-reminder-plan-mode-approval-tool-enforcement                                     plan-mode                 M2
system-reminder-plan-mode-is-active                                                     plan-mode                 have
system-reminder-plan-mode-is-active-5-phase                                             plan-mode                 M2
system-reminder-plan-mode-is-active-subagent                                            plan-mode                 M2
system-reminder-plan-mode-phase-2-design                                                plan-mode                 M2
system-reminder-plan-mode-re-entry                                                      plan-mode                 M2
system-reminder-prefetched-artifact-type-instruction-files                              art-types-design          M4
system-reminder-prefetched-design-system-file-reading-guidance                          art-types-design          M4
system-reminder-previously-invoked-skills                                               skills                    M2
system-reminder-proactivity-level-high                                                  proactivity               M4
system-reminder-proactivity-level-low                                                   proactivity               M4
system-reminder-proactivity-level-medium                                                proactivity               M4
system-reminder-proactivity-setting-announcement-header                                 proactivity               M4
system-reminder-proactivity-setting-withdrawn                                           proactivity               M4
system-reminder-project-document-rewrite-without-current-text-refusal                   projects-inbox            n/a
system-reminder-project-document-too-large-to-rewrite-refusal                           projects-inbox            n/a
system-reminder-project-document-version-conflict-recovery                              projects-inbox            n/a
system-reminder-project-memory-disconnected                                             projects-inbox            n/a
system-reminder-provider-context                                                        session-reminders         M2
system-reminder-question-context                                                        session-reminders         M2
system-reminder-queued-notifications-delivery                                           session-reminders         M2
system-reminder-queued-notifications-read-time-clock-note                               session-reminders         M2
system-reminder-read-truncation-retry-guidance                                          file-read-notices         M2
system-reminder-remote-chrome-browser-extension-not-connected                           bx-setup                  M4
system-reminder-remote-machine-branch-transfer-review                                   remote-machines           n/a
system-reminder-remote-machine-git-and-credential-routing                               remote-machines           n/a
system-reminder-remote-machine-only-resources-routing                                   remote-machines           n/a
system-reminder-remote-machine-separate-project-copies                                  remote-machines           n/a
system-reminder-reserved-address-mcp-server-refusal                                     mcp-deferred              M4
system-reminder-resume-agents-interrupted-by-container-restart                          remote-machines           n/a
system-reminder-scheduled-task-automated-firing                                         loops                     M4
system-reminder-send-to-project-gathered-context-trust-boundary                         projects-inbox            n/a
system-reminder-send-to-project-interrupted-result-recovery                             projects-inbox            n/a
system-reminder-send-to-project-memory-brief-trust-boundary                             projects-inbox            n/a
system-reminder-session-context                                                         session-reminders         M2
system-reminder-session-continuation                                                    remote-machines           n/a
system-reminder-session-moved-to-new-runner-resume-prompt                               remote-machines           n/a
system-reminder-session-stop-hook-active                                                hooks                     M2
system-reminder-staged-file-still-copying                                               remote-machines           n/a
system-reminder-staged-files-failed-to-arrive                                           remote-machines           n/a
system-reminder-staged-files-still-copying                                              remote-machines           n/a
system-reminder-still-running-tool-call                                                 background-tasks          M2
system-reminder-stop-hook-blocking-error                                                hooks                     M2
system-reminder-subagent-write-blocked-pending-worktree-isolation                       worktrees                 M4
system-reminder-task-tools-reminder                                                     todo-tasks                M4
system-reminder-team-coordination                                                       teams                     M4
system-reminder-team-shutdown                                                           teams                     M4
system-reminder-todowrite-reminder                                                      todo-tasks                have
system-reminder-token-usage                                                             session-reminders         M2
system-reminder-tool-calls-lost-to-session-restart                                      remote-machines           n/a
system-reminder-ultracode-enabled                                                       workflow                  M4
system-reminder-ultraplan-mode                                                          ultraplan                 n/a
system-reminder-unreachable-attached-machines                                           remote-machines           n/a
system-reminder-unreachable-attached-machines-call-only                                 remote-machines           n/a
system-reminder-unreachable-attached-machines-rechecked                                 remote-machines           n/a
system-reminder-unreachable-attached-machines-retry-by-call-guidance                    remote-machines           n/a
system-reminder-unreachable-attached-machines-return-noted                              remote-machines           n/a
system-reminder-usd-budget                                                              session-reminders         M2
system-reminder-user-controlled-plan-mode-exit                                          plan-mode                 M2
system-reminder-web-fetch-untrusted-content-reporting-guidance                          web                       M2
system-reminder-workflow-isolated-worktree                                              workflow                  M4
system-reminder-you-should-know-side-request                                            recaps-suggestions        n/a
tool-description-agent-delegation-summary-lean-rewrite                                  delegation                variant
tool-description-agent-delegation-tradeoffs-lean-rewrite                                delegation                variant
tool-description-agent-explicit-spawn-restriction                                       delegation                M2
tool-description-agent-result-visibility-lean-rewrite                                   delegation                variant
tool-description-agent-usage-notes                                                      delegation                M2
tool-description-agent-when-to-launch-subagents                                         delegation                M2
tool-description-agent-when-to-launch-subagents-lean-rewrite                            delegation                variant
tool-description-appifactrepl                                                           art-types-design          M4
tool-description-artifact-action-reference                                              art-publish               M4
tool-description-artifact-action-reference-concise-app-wording                          art-publish               variant
tool-description-artifact-asset-read-result                                             art-data-assets           M4
tool-description-artifact-asset-upload-guidance                                         art-data-assets           M4
tool-description-artifact-assets-guidance-app-wording                                   art-data-assets           variant
tool-description-artifact-browser-storage-guidance                                      art-data-assets           M4
tool-description-artifact-comments-guidance                                             art-comments              M4
tool-description-artifact-comments-guidance-app-wording                                 art-comments              variant
tool-description-artifact-database-guidance                                             art-data-assets           M4
tool-description-artifact-database-version-pinning-guidance                             art-data-assets           M4
tool-description-artifact-design-fallback-requirements                                  art-types-design          M4
tool-description-artifact-design-skill-loading-guidance-app-wording                     art-types-design          variant
tool-description-artifact-external-resource-allowlist                                   art-publish               M4
tool-description-artifact-html-document-skeleton                                        art-publish               M4
tool-description-artifact-known-version-resubmission-guidance                           art-publish               M4
tool-description-artifact-line-diff-reading-legend                                      art-publish               M4
tool-description-artifact-live-room-guidance                                            art-data-assets           M4
tool-description-artifact-live-room-guidance-app-wording                                art-data-assets           variant
tool-description-artifact-multi-file-edit-and-republish-guidance                        art-publish               M4
tool-description-artifact-nested-runtime-cleanup-error                                  art-publish               M4
tool-description-artifact-page-authoring-and-html-skeleton-app-wording                  art-publish               variant
tool-description-artifact-page-implementation-requirements-app-wording                  art-publish               variant
tool-description-artifact-preview-action                                                art-publish               M4
tool-description-artifact-preview-action-in-session-viewer                              art-publish               M4
tool-description-artifact-profiles-action-guidance                                      art-data-assets           M4
tool-description-artifact-publish-edits-resend-clause                                   art-publish               M4
tool-description-artifact-publish-edits-resubmission-merge-guidance                     art-publish               M4
tool-description-artifact-publishing-and-update-guidance                                art-publish               M4
tool-description-artifact-publishing-introduction-app-wording                           art-publish               variant
tool-description-artifact-quickstart-type-guidance                                      art-types-design          M4
tool-description-artifact-quickstart-type-guidance-app-wording                          art-types-design          variant
tool-description-artifact-responsive-page-contract                                      art-publish               M4
tool-description-artifact-resubmission-refetch-confirmation-clause                      art-publish               M4
tool-description-artifact-runtime-capabilities-guidance-app-wording                     art-data-assets           variant
tool-description-artifact-runtime-verification-guidance                                 art-publish               M4
tool-description-artifact-same-response-edit-and-publish-guidance                       art-publish               M4
tool-description-artifact-share-action-reference                                        art-publish               M4
tool-description-artifact-sharing-guidance                                              art-publish               M4
tool-description-artifact-small-change-edit-and-republish-guidance                      art-publish               M4
tool-description-artifact-stale-publish-saved-source-guidance                           art-publish               M4
tool-description-artifact-theme-aware-styling                                           art-publish               M4
tool-description-artifact-title-and-description-guidance                                art-publish               M4
tool-description-artifact-title-and-description-guidance-app-wording                    art-publish               variant
tool-description-artifact-type-creation-guidance                                        art-types-design          M4
tool-description-artifact-type-discovery-guidance                                       art-types-design          M4
tool-description-artifact-type-discovery-guidance-app-wording                           art-types-design          variant
tool-description-artifact-type-file-backed-content-detection-guidance                   art-types-design          M4
tool-description-artifact-type-file-backed-content-update-guidance                      art-types-design          M4
tool-description-artifact-type-staged-first-publish-sequence                            art-types-design          M4
tool-description-artifact-watch-approval-explanation                                    art-publish               M4
tool-description-askuserquestion                                                        ask-user                  have
tool-description-askuserquestion-decision-guidance                                      ask-user                  have
tool-description-askuserquestion-extended-host-guidance                                 ask-user                  M3
tool-description-askuserquestion-lean-rewrite                                           ask-user                  variant
tool-description-askuserquestion-preview-field                                          ask-user                  M3
tool-description-askuserquestion-without-plan-mode-note                                 ask-user                  have
tool-description-background-monitor-push-notification-guidance                          monitor                   M4
tool-description-background-monitor-streaming-events                                    monitor                   M4
tool-description-background-monitor-websocket-source                                    monitor                   M4
tool-description-bash-alternative-communication                                         bash-core                 have
tool-description-bash-alternative-content-search                                        bash-core                 have
tool-description-bash-alternative-edit-files                                            bash-core                 have
tool-description-bash-alternative-file-search                                           bash-core                 have
tool-description-bash-alternative-read-files                                            bash-core                 have
tool-description-bash-alternative-write-files                                           bash-core                 have
tool-description-bash-attached-machines                                                 remote-machines           n/a
tool-description-bash-built-in-tools-note                                               bash-core                 have
tool-description-bash-git-avoid-destructive-ops                                         git-workflow              M2
tool-description-bash-git-commit-instructions                                           git-workflow              M2
tool-description-bash-git-never-skip-hooks                                              git-workflow              M2
tool-description-bash-git-prefer-new-commits                                            git-workflow              M2
tool-description-bash-maintain-cwd                                                      bash-core                 have
tool-description-bash-overview                                                          bash-core                 have
tool-description-bash-pr-creation-instructions                                          git-workflow              M2
tool-description-bash-prefer-dedicated-tools                                            bash-core                 have
tool-description-bash-prefer-dedicated-tools-bullet                                     bash-core                 have
tool-description-bash-quote-file-paths                                                  bash-core                 have
tool-description-bash-sandbox-default-to-sandbox                                        sandbox                   M4
tool-description-bash-sandbox-evidence-access-denied                                    sandbox                   M4
tool-description-bash-sandbox-evidence-list-header                                      sandbox                   M4
tool-description-bash-sandbox-evidence-network-failures                                 sandbox                   M4
tool-description-bash-sandbox-evidence-operation-not-permitted                          sandbox                   M4
tool-description-bash-sandbox-evidence-unix-socket-errors                               sandbox                   M4
tool-description-bash-sandbox-explain-restriction                                       sandbox                   M4
tool-description-bash-sandbox-failure-evidence-condition                                sandbox                   M4
tool-description-bash-sandbox-local-port-binding-eperm-no-override                      sandbox                   M4
tool-description-bash-sandbox-local-port-binding-eperm-override-available               sandbox                   M4
tool-description-bash-sandbox-no-sensitive-paths                                        sandbox                   M4
tool-description-bash-sandbox-per-command                                               sandbox                   M4
tool-description-bash-sandbox-response-header                                           sandbox                   M4
tool-description-bash-sandbox-retry-without-sandbox                                     sandbox                   M4
tool-description-bash-sandbox-tmpdir                                                    sandbox                   M4
tool-description-bash-sleep-keep-short                                                  bash-core                 have
tool-description-bash-sleep-no-polling-background-tasks                                 bash-core                 have
tool-description-bash-sleep-run-immediately                                             bash-core                 have
tool-description-bash-sleep-use-check-commands                                          bash-core                 have
tool-description-bash-timeout                                                           bash-core                 have
tool-description-bash-verify-parent-directory                                           bash-core                 have
tool-description-browserbatch                                                           bx-nav-tabs               M4
tool-description-chrome-browser-automation                                              bx-computer               M4
tool-description-claude-ai-project                                                      projects-inbox            n/a
tool-description-claude-in-chrome-bridge-disconnect-error                               bx-setup                  M4
tool-description-claude-in-chrome-find                                                  bx-page-read              M4
tool-description-claude-in-chrome-get-page-text                                         bx-page-read              M4
tool-description-claude-in-chrome-javascript-tool                                       bx-devtools               M4
tool-description-claude-in-chrome-read-console-messages                                 bx-devtools               M4
tool-description-claude-in-chrome-read-network-requests                                 bx-devtools               M4
tool-description-claude-in-chrome-read-page                                             bx-page-read              M4
tool-description-claude-in-chrome-shortcuts-execute                                     bx-nav-tabs               M4
tool-description-claude-in-chrome-switch-browser                                        bx-setup                  M4
tool-description-claude-in-chrome-switched-off-and-unlinked-chat-guidance               browser-computer          n/a
tool-description-claude-in-chrome-switched-off-chat-guidance                            bx-setup                  M4
tool-description-claude-in-chrome-tabs-context                                          bx-nav-tabs               M4
tool-description-claude-in-chrome-unavailable-stub-result                               bx-setup                  M4
tool-description-claudedesign                                                           art-types-design          M4
tool-description-code-review-command                                                    code-review               M4
tool-description-computer                                                               bx-computer               M4
tool-description-computer-use-enable-stub-guidance                                      browser-computer          n/a
tool-description-computer-use-unlinked-chat-guidance                                    browser-computer          n/a
tool-description-cowork-onboarding-role-picker                                          claude-ai-ecosystem       n/a
tool-description-cowork-plugin-creation                                                 claude-ai-ecosystem       n/a
tool-description-croncreate                                                             loops                     M4
tool-description-designsync                                                             art-types-design          M4
tool-description-durable-artifact-watch-and-status-guidance                             art-publish               M4
tool-description-easel-chart                                                            art-types-design          M4
tool-description-edit                                                                   file-tools                have
tool-description-edit-minimal-old-string-guidance                                       file-tools                have
tool-description-edit-single-replacement                                                file-tools                variant
tool-description-enable-claude-app-browser                                              browser-computer          n/a
tool-description-enable-claude-app-browser-shared-description                           browser-computer          n/a
tool-description-enable-claude-in-chrome                                                bx-setup                  M4
tool-description-enable-claude-in-chrome-shared-description                             bx-setup                  variant
tool-description-enable-computer-use                                                    browser-computer          n/a
tool-description-enable-computer-use-shared-description                                 browser-computer          n/a
tool-description-endconversation                                                        misc-product-tools        n/a
tool-description-enterplanmode                                                          plan-mode                 have
tool-description-enterplanmode-ambiguous-tasks                                          plan-mode                 variant
tool-description-enterworktree                                                          worktrees                 M4
tool-description-exitplanmode                                                           plan-mode                 have
tool-description-exitplanmode-diskless-session                                          plan-mode                 variant
tool-description-exitworktree                                                           worktrees                 M4
tool-description-fetchinboxmessage                                                      projects-inbox            n/a
tool-description-fetchinboxmessage-project-thread-wake-envelope                         projects-inbox            n/a
tool-description-gettask                                                                background-tasks          M4
tool-description-gettask-default-completion-guidance                                    background-tasks          M4
tool-description-gettask-stopping-session-completion-guidance                           background-tasks          variant
tool-description-glob                                                                   file-tools                have
tool-description-glob-compact                                                           file-tools                variant
tool-description-grep                                                                   file-tools                have
tool-description-grep-compact                                                           file-tools                variant
tool-description-grep-compact-lean-rewrite                                              file-tools                variant
tool-description-invoke-skill                                                           skills                    M2
tool-description-invoke-skill-lean-rewrite                                              skills                    variant
tool-description-list-connected-browsers-browser-picker-guidance                        bx-setup                  M4
tool-description-listagents                                                             teams                     M4
tool-description-listagents-no-sendmessage-tool                                         teams                     variant
tool-description-listconnectors                                                         claude-ai-ecosystem       n/a
tool-description-listmcpresourcestool                                                   mcp-deferred              M2
tool-description-listmcpresourcestool-prompt                                            mcp-deferred              variant
tool-description-live-artifact-watch-guidance-app-wording                               art-publish               variant
tool-description-lsp                                                                    ide-lsp                   M4
tool-description-mcp-connector-sign-in-recheck                                          mcp-deferred              n/a
tool-description-memory-list-prompt                                                     auto-memory               M4
tool-description-memory-write-prompt                                                    auto-memory               M4
tool-description-memory-write-update-triggers-and-timing                                auto-memory               M4
tool-description-navigate                                                               bx-nav-tabs               M4
tool-description-navigate-tab-cleanup-guidance                                          bx-nav-tabs               M4
tool-description-new-file-backed-artifact-type-content-guidance                         art-types-design          M4
tool-description-notebookedit                                                           file-tools                M2
tool-description-offer-claude-in-chrome-setup                                           bx-setup                  M4
tool-description-plugin-organization-submission-and-replacement                         claude-ai-ecosystem       n/a
tool-description-poll                                                                   projects-inbox            n/a
tool-description-powershell                                                             powershell                M4
tool-description-powershell-git-guidance                                                powershell                M4
tool-description-prohibited-artifact-publishing-app-wording                             art-publish               variant
tool-description-project-local-file-read-only-workflow                                  projects-inbox            n/a
tool-description-proposegoal                                                            misc-product-tools        n/a
tool-description-publish-audience-facing-deliverables-app-wording                       art-publish               variant
tool-description-publishplugin-file-listing-and-consent-flow                            claude-ai-ecosystem       n/a
tool-description-pushnotification                                                       loops                     M4
tool-description-readfile                                                               file-tools                have
tool-description-readfile-compact                                                       file-tools                variant
tool-description-readmcpresourcedirtool-prompt                                          mcp-deferred              M4
tool-description-readnotifications                                                      projects-inbox            n/a
tool-description-refreshmcptools                                                        mcp-deferred              M2
tool-description-refreshmcptools-prompt                                                 mcp-deferred              variant
tool-description-remote-artifact-watch-guidance                                         art-publish               M4
tool-description-remotetrigger-prompt                                                   cloud-schedule            n/a
tool-description-report-code-review-findings                                            code-review               M4
tool-description-schedulewakeup                                                         loops                     M4
tool-description-schedulewakeup-delay-guidance-1-hour-cache-ttl                         loops                     variant
tool-description-schedulewakeup-delay-guidance-5-minute-cache-ttl                       loops                     variant
tool-description-schedulewakeup-delay-guidance-unknown-cache-ttl                        loops                     variant
tool-description-schedulewakeup-noop-state-guidance                                     loops                     M4
tool-description-schedulewakeup-reason-field-guidance                                   loops                     M4
tool-description-searchmcpregistry                                                      claude-ai-ecosystem       n/a
tool-description-searchplugins-examples                                                 claude-ai-ecosystem       n/a
tool-description-searchplugins-purpose                                                  claude-ai-ecosystem       n/a
tool-description-searchplugins-result-handling                                          claude-ai-ecosystem       n/a
tool-description-searchplugins-up-front-guidance                                        claude-ai-ecosystem       n/a
tool-description-searchskills                                                           claude-ai-ecosystem       n/a
tool-description-self-hosted-runner-requeue-session                                     self-hosted-runner        n/a
tool-description-sendfeedback-drafting-guidance                                         misc-product-tools        n/a
tool-description-sendfile                                                               projects-inbox            n/a
tool-description-sendmessage                                                            teams                     M4
tool-description-sendmessage-cross-session-guidance                                     teams                     M4
tool-description-sendtoproject-message                                                  projects-inbox            n/a
tool-description-sendtoproject-work-brief                                               projects-inbox            n/a
tool-description-sendtoprojectcontext                                                   projects-inbox            n/a
tool-description-senduserfile                                                           misc-product-tools        M4
tool-description-sendusermessage                                                        visibility-modes          M4
tool-description-sendusermessage-verbatim                                               visibility-modes          M4
tool-description-session-artifact-watch-lifecycle                                       art-publish               M4
tool-description-showonboardingrolepicker                                               claude-ai-ecosystem       n/a
tool-description-skill-proposal-rendering                                               claude-ai-ecosystem       n/a
tool-description-skill-scoped-git-push-refusal                                          claude-ai-ecosystem       n/a
tool-description-subagenthandback                                                       builtin-agents            M2
tool-description-subscribepr                                                            projects-inbox            n/a
tool-description-suggestconnectors                                                      claude-ai-ecosystem       n/a
tool-description-suggestplugininstall                                                   claude-ai-ecosystem       n/a
tool-description-suggestskills-proactive-guidance                                       claude-ai-ecosystem       n/a
tool-description-task-get                                                               todo-tasks                M4
tool-description-taskcreate                                                             todo-tasks                M4
tool-description-tasklist                                                               todo-tasks                M4
tool-description-tasklist-teammate-workflow                                             teams                     M4
tool-description-taskupdate                                                             todo-tasks                M4
tool-description-todowrite                                                              todo-tasks                have
tool-description-todowrite-compact                                                      todo-tasks                variant
tool-description-todowrite-proactive-update-guidance                                    todo-tasks                have
tool-description-toolsearch-input-validation-note                                       mcp-deferred              M2
tool-description-toolsearch-lean-rewrite                                                mcp-deferred              variant
tool-description-toolsearch-opening                                                     mcp-deferred              M2
tool-description-toolsearch-second-part                                                 mcp-deferred              M2
tool-description-toolsearch-unfetched-tool-note                                         mcp-deferred              M2
tool-description-updating-existing-artifacts-app-wording                                art-publish               variant
tool-description-upload-image                                                           bx-page-read              M4
tool-description-webfetch                                                               web                       M2
tool-description-webfetch-concise                                                       web                       variant
tool-description-webfetch-private-url-warning                                           web                       M2
tool-description-websearch                                                              web                       M2
tool-description-websearch-concise                                                      web                       variant
tool-description-workflow                                                               workflow                  M4
tool-description-workflow-lean-rewrite                                                  workflow                  variant
tool-description-write                                                                  file-tools                have
tool-description-write-read-existing-file-first                                         file-tools                have
tool-parameter-artifact-auto-open-timing                                                art-publish               M4
tool-parameter-artifact-auto-open-timing-app-wording                                    art-publish               variant
tool-parameter-artifact-comment-actions-guidance                                        art-comments              M4
tool-parameter-artifact-database-access-level                                           art-data-assets           M4
tool-parameter-artifact-database-batch-writes                                           art-data-assets           M4
tool-parameter-artifact-database-version-precondition                                   art-data-assets           M4
tool-parameter-artifact-force-overwrite-guidance                                        art-publish               M4
tool-parameter-artifact-force-page-overwrite-warning                                    art-publish               M4
tool-parameter-artifact-force-supporting-files-preservation                             art-publish               M4
tool-parameter-artifact-preview-action                                                  art-publish               M4
tool-parameter-artifact-root-directory                                                  art-publish               M4
tool-parameter-artifact-supporting-files-with-cross-artifact-sources                    art-publish               M4
tool-parameter-artifact-supporting-files-with-cross-artifact-sources-app-wording        art-publish               variant
tool-parameter-artifact-title                                                           art-publish               M4
tool-parameter-artifact-unread-path-overwrite-acknowledgement                           art-publish               M4
tool-parameter-artifact-url-guidance                                                    art-publish               M4
tool-parameter-artifact-url-guidance-app-wording                                        art-publish               variant
tool-parameter-artifact-watch-actions-guidance                                          art-publish               M4
tool-parameter-bash-command-description                                                 bash-core                 have
tool-parameter-bash-run-in-background-guidance                                          bash-core                 have
tool-parameter-bash-run-in-background-guidance-completion-notices-disabled              bash-core                 variant
tool-parameter-claude-in-chrome-javascript-code                                         bx-devtools               M4
tool-parameter-computer-action                                                          bx-computer               M4
tool-parameter-matched-ask-rule                                                         nm-sdk-protocol           n/a
tool-parameter-powershell-run-in-background-note                                        powershell                M4
tool-parameter-powershell-run-in-background-note-completion-notices-disabled            powershell                variant
tool-parameter-sandboxed-command-allowed-domains                                        sandbox                   M4
tool-parameter-sendtoproject-proposal                                                   projects-inbox            n/a
tool-parameter-sendusermessage-attachments                                              visibility-modes          M4
tool-parameter-set-cwd-needs-trust-directory                                            nm-sdk-protocol           n/a
```
