# Prompt files

The server builds the prompt bundle (`harness.prompts.v1.PromptService/GetBundle`) from this directory
(`HARNESS_PROMPTS_DIR`, default `./prompts`). Files are re-read on every request, so edits apply without a restart.
The bundle `version` is a content hash: it changes exactly when the resolved texts change.

| Path | Bundle field |
|---|---|
| `system/static/NN-<id>.md` | static system sections, ordered by file name; `NN-` is an optional sort prefix and is stripped from the id |
| `system/dynamic/NN-<id>.md` | dynamic sections, placed after the dynamic boundary marker; may contain `{{placeholders}}` |
| `tools/<Name>/description.md` + `tools/<Name>/schema.json` | `tools[<Name>]` (schema is JSON Schema draft-07) |
| `reminders/<id>.md` | `reminders[<id>]` |
| `compaction/instruction.md` | `compaction_instruction` |
| `title/instruction.md` | `title_instruction` |
| `output-styles/<id>.md` | `output_styles[<id>]` (see below) |

Trailing whitespace of each file is trimmed. This README is not part of the bundle.

The behavior every file must implement is specified in `docs/design/prompt-requirements.md`; change the spec
first, then the prompt.

Server-side prompts live next to the bundle files but are never sent to clients:

| Path | Used by |
|---|---|
| `decision/system.md` | system prompt of the `llm` decision provider (`harness.decision.v1.DecisionService`, the client's `auto` mode) |
| `decision/classify/description.md` + `decision/classify/schema.json` | the `classify` tool the classifier must call (`{decision: allow\|deny\|ask, reason, categories[], confidence}`) |
| `decision/typed/question.json` | the question the `decision_model` provider (typed decision models such as TypeSafe's Jev) sends as `questions.decision`: `{type: "choice", instructions, criteria: {allow, ask, deny}}`; the labels must be exactly allow, ask and deny |

They follow the same per-model override rules as everything else (e.g. `overrides/qwen-*/decision/system.md`).
The typed question is matched against the configured decision model id, which may contain a slash, so use a
wildcard such as `overrides/*jev-1.13*/decision/typed/question.json`.

All texts are written from scratch (clean-room); never copy Claude Code's prompts.

## Output styles

`output-styles/<id>.md` defines a built-in style. The file starts with frontmatter, followed by the style text:

```markdown
---
name: Explanatory
description: Does the task and explains the implementation choices along the way
keep-coding-instructions: true
---
Style instructions in Markdown...
```

`name` defaults to the capitalized id, `description` to empty, `keep-coding-instructions` to `true` (when `false`,
the client drops the `doing_tasks` and `tool_policy` sections while the style is active). Built-ins: `default`
(no text, adds nothing), `proactive`, `concise`, `explanatory`, `learning`. The client sends the active style's
text through the `output_style` reminder, so switching styles keeps the prompt cache. User and project styles
(sync, `.claude/output-styles/*.md`) use the same frontmatter but are resolved by the client, not here. Styles
follow the per-model override rules (an empty override file removes the style for that model).

## Placeholders

Clients fill `{{name}}` placeholders; an unknown placeholder becomes empty (and the client logs a warning).

**Dynamic system sections** (`system/dynamic/*`): `cwd`, `platform`, `shell`, `os_version`, `date`, `is_git`,
`git_status`, `model_id`, `model_display_name`, `additional_directories`, `scratchpad_dir`.

**Reminders** (`reminders/<id>.md`, ids and placeholders as listed in `proto/harness/prompts/v1/prompts.proto`):

| id | Placeholders | When the client sends it |
|---|---|---|
| `project_instructions` | `content` | CLAUDE.md / AGENTS.md / user memory, in the first user message |
| `file_changed` | `path`, `diff` | a file the model read changed on disk |
| `empty_file` | `path` | Read of an empty file |
| `interrupted` | — | user message after an interruption |
| `interrupted_tool_use` | — | tool_result of a call cancelled by an interruption |
| `interrupt_reply` | — | not stored: appended to the last user message of one extra request (session model, no thinking, `tool_choice: none`, `auxiliary`, ~150 tokens) right after an interruption; the reply becomes the assistant message that answers the stop |
| `todo_nudge` | — | several turns without TodoWrite on a multi-step task |
| `plan_mode` | `plan_file_path` | plan mode is active |
| `auto_mode` | — | auto mode is active: once per stretch of auto mode and again after a compaction; allows shell reading, searching and mechanical edits, with Read/Edit/Write as the fallback |
| `plan_approved` | `plan_file_path` | the user approved the plan (ExitPlanMode) |
| `plan_rejected` | `feedback` | the user asked to keep planning |
| `background_task_done` | `task_id`, `command`, `status`, `tail`, `output_path` | a background Bash command finished: `status` is "exited with code N" or how it was killed, `tail` its last lines ("(no output)" when empty). Rides on the next model request (the next turn when idle; it never starts a turn); skipped when a TaskOutput or Wait result already reported the finish |
| `compacted` | — | first message after a compaction summary |
| `attribution` | `commit`, `pr` | commit / PR attribution lines |
| `output_style` | `name`, `content` | the active output style, at session start and when it changes |
| `deferred_tools` | `tools` | MCP tools are deferred behind ToolSearch: their names, one per line, at session start |
| `environment_changed` | `directories`, `variables` | the user edited the session's environment while it runs (AgentService UpdateEnvironment / AddSessionDirectory): the working directories now, one per line, and the names of the variables set (never values; "(none)" when empty). Rides on the next model request |
| `user_shell_command` | `cwd`, `command`, `exit_code`, `output`, `status` | the user ran a command in the composer's shell mode (AgentService RunShellCommand) and it ended: the directory and command line, the exit code (-1 when it did not exit by itself), stdout then stderr (head and tail kept past 10000 characters; "(no output)" when empty, wrapped in newlines) and `status`, one sentence on how it ended. Command, directory and output are escaped so they cannot close the `<user-shell-...>` tags or open a `<system-reminder>`. Rides on the next model request (the next request of a running turn, or the next turn; it never starts a turn). Not wholly wrapped in `<system-reminder>`: the command and output come first, as the user's |
| `usage_limit_wrap_up` | `provider`, `detail`, `resets` | the provider behind the session is nearly out of usage (a rate-limit window ≥ 97% used, almost no balance left, or ≥ 97% of the monthly budget spent; `harness.agent.v1.UsageLimit` WRAP_UP): finish the piece in hand, start nothing large, leave things consistent, end with a summary of what is done and what is left. `provider` is the provider's name, `detail` what ran out (e.g. "5h: 98% used, resets Tue 11:00 PM CET" or "$0.20 left"), `resets` "the limit resets at …" (or "the limit resets"). Sent once per turn, after a model response that has tool calls (the turn goes on); the client has a fallback text |
| `usage_limit_returned` | `provider` | the session has "continue when limits return" on (`SetAutoContinue`), the limit that cut the last turn off has reset, and the client starts the turn itself: pick up the interrupted work where it stopped, without repeating what is done. Messages the limit held are the input of that turn and this rides along; with none it is the turn's only input (`Driver.Wake`). `provider` is the provider's name. The client has a fallback text |
| `pr_activity` | `number`, `url`, `events`, `external` | the session's monitored PR had a CI failure, a conflict or new comments (starts a turn when idle, `Driver.Wake`), or was merged or closed (rides on the next request). `events` is the client's own text, one line each; `external` is code-host text (comments, check names, CI log tails), escaped so it cannot close `<pr-external-data>` or open a `<system-reminder>`, and marked as data with no user authority. Not wholly wrapped in `<system-reminder>`. At most 6 waking notes per hour; the client has a fallback text |
| `pr_detected` | `number`, `url` | a shell command opened a PR for the session's branch and none is bound: the model may offer to follow it. Rides on the next request. The client has a fallback text |
| `webfetch_summary` | `url`, `prompt`, `content` | not a reminder: the WebFetch summarization prompt. The client fills it with the fetched page (as Markdown) and the tool's `prompt`, sends it as the only user message of an `auxiliary` request to the small-slot model and returns the reply as the tool result. Not wrapped in `<system-reminder>` |
| `bash_background_user` | `task_id`, `output_path` | not a reminder: the Bash tool_result when the user moved the running command to the background (AgentService BackgroundToolCall, reason USER). Not wrapped in `<system-reminder>` |
| `bash_background_queued_message` | `task_id`, `output_path` | not a reminder: the Bash tool_result when the running command was moved to the background so a message the user sent meanwhile is delivered now (SendUserMessage DELIVERY_NOW, or BackgroundToolCall with reason QUEUED_MESSAGE) |
| `bash_background_agent` | `task_id`, `output_path` | not a reminder: the Bash tool_result of a `run_in_background` call |
| `bash_background_timeout` | `timeout`, `task_id`, `output_path` | not a reminder: the Bash tool_result when a foreground command hit its timeout (e.g. "2m0s", "300ms") and was moved to the background |
| `side_chat` | — | not a reminder: the system prompt of side chats (AgentService AskSideChat). The client sends it as the first system block of a tool-less `auxiliary` request, followed by a block with a read-only rendering of the main session inside `<main_session>`, then the side chat's history and the question (with its quoted excerpt). Not wrapped in `<system-reminder>` |

Each placeholder appears once per reminder (the tests compare the list of placeholders). `interrupted` must stay
exactly `[Request interrupted by user]`, and `interrupted_tool_use` must start with
`[Request interrupted by user for tool use]`.

## Tools

| Tool | Input | Notes |
|---|---|---|
| `Read`, `Write`, `Edit`, `Glob`, `Grep` | see `schema.json` | M0 file tools |
| `Bash` | `command`, `timeout`, `description`, `run_in_background` | background: returns a task id + output file path at once; a foreground command that hits its timeout is moved to the background; completion arrives as `background_task_done` |
| `TaskStop` | `task_id` | stops a background task |
| `TaskOutput` | `task_id`, `offset`, `block`, `pattern`, `timeout` | new output since the last TaskOutput read of that task (or from `offset`), with the status; `block` waits for the finish, `pattern` for a new matching line; timeout default 30 s, max 10 min; over 30000 characters keeps the end with a note |
| `Wait` | `task_id` \| `port` (+`host`) \| `path` (+`changed`), `pattern`, `timeout` (required, max 10 min) | waits for a task to finish or print a matching line, a TCP port to accept connections, or a file to exist / change / match; a timeout is a normal result. Read-only, except a port on a host other than this machine (normal permission check); a path outside the working directories is checked like Read |
| `TodoWrite` | `todos: [{content, activeForm, status}]` | full replacement; exactly one `in_progress` |
| `AskUserQuestion` | `questions: [{question, header (≤12), type: single\|multi\|text\|yes_no, options: [{label, description?, recommended?}] (2–6, single/multi only), min?, max? (multi), allowOther? (default true)}] (1–4)` | |
| `WrapUp` | `recap`, `ask`, `actions` (1–2) | closes a turn: the box under the last message; a button sends its label as the user's reply |
| `PrStatus` | `pr?` (number or URL) | a PR/MR through gh or glab: state, checks, mergeable, review decision, unresolved threads; defaults to the bound PR, then the branch's. Read-only |
| `PrBind`, `PrUnbind` | `pr`; `{}` | attach or detach the PR this session follows (device-local, `HARNESS_HOME/pr-watch`; SessionInfo.pull_request). No prompt: they change no code-host state |
| `PrMonitor` | `enabled` | the client polls the bound PR and wakes the session (`pr_activity`) on a CI failure, a conflict or a new comment; stops at merge, close or session close, resumes after a client restart. Normal permission check; the UI's auto-fix switch is `SetPrMonitor` |
| `PrAutoMerge` | `enabled`, `method?` (merge, squash, rebase) | sets auto-merge; always asks the user (AlwaysAsk: no rule, mode or auto-mode decision can approve it) |
| `EnterPlanMode` | `{}` | |
| `ExitPlanMode` | `plan` (Markdown) | the client writes it to the plan file and asks the user to approve |
| `ToolSearch` | `query`, `max_results` | offered only while MCP tools are deferred; `select:a,b` or keywords; returns full definitions and makes those tools callable |
| `WebSearch` | `query`, `allowed_domains`, `blocked_domains` | runs on the server (`harness.search.v1.SearchService`); the client formats title, URL, snippet and date and adds a reminder to cite sources; the domain lists cannot be combined |
| `WebFetch` | `url`, `prompt` | fetched locally (http upgraded to https except for private hosts, same-host redirects followed, a cross-host redirect returned as a notice, 10 MB / 60 s limits, HTML converted to Markdown, binary refused, 15-minute cache per URL); the answer comes from the small-slot model with `reminders/webfetch_summary.md` |
| `Show` | `target` (`file` \| `diff` \| `url`), `path`, `line`, `end_line`, `url` | opens the target in the UI's Files, Diff or Browser pane (`ShowRequested` event); read-only, no permission; without a UI that can open panes it reports that nothing was shown |
| `Visual` | `id`, `title`, `html` | renders a self-contained HTML document in the transcript (sandboxed frame, app colours as CSS variables, `harness.send()` posts a `[from visual <id>]` user message); the same `id` adds a version; read-only, no permission; without a UI that renders it reports that nothing was shown |
| `BrowserNavigate` | `url` \| `action` (`back` \| `forward` \| `reload`) | the session's headless Chromium (packages/client/browser), shared with the Browser pane; waits for the load (30 s); returns tab, URL and title. Action: per-site permission (localhost and files allowed; a new site asks; `Browser(domain:<host>)` rules) |
| `BrowserSnapshot` | `ref`, `selector`, `max_chars` | outline of the visible page with stable `[ref_N]` element refs, sensitive fields marked, cut at 24000 characters by default, wrapped in `<untrusted-page-content>`; read-only |
| `BrowserClick` | `ref` \| `x`+`y`, `button`, `double`, `modifiers` | real mouse events at the element's center; reports a covering element, the navigation or new tab it caused; action |
| `BrowserType` | `ref`, `text`, `clear` (default true), `submit` | inserts text (chooses an option on a select); password, one-time-code, payment and identity fields are refused unless the user approves that call (user-only prompt, whatever the mode); action |
| `BrowserKey` | `keys` (space-separated combos such as `ctrl+a Backspace`), `repeat` | key events to the focused element; action |
| `BrowserScroll` | `direction`, `amount`, `ref`, `x`, `y` | wheel scroll (page or the element under a point) or `ref` into view; reports the scroll position; action |
| `BrowserScreenshot` | `full_page`, `ref` | image block (PNG viewport/element, JPEG full page cut at 12000 px), scaled to ≤1568 px; read-only |
| `BrowserEval` | `expression`, `timeout` (ms, ≤60000) | JavaScript in the page's main world (top-level await), JSON result cut at 20000 characters, untrusted; asks on every site unless `BrowserEval` / `BrowserEval(domain:...)` is allowed |
| `BrowserTabs` | `action` (`list` \| `new` \| `switch` \| `close`), `tab_id`, `url` | tab ids `t1`...; list is read-only |
| `BrowserConsole` | `level`, `pattern`, `limit` (≤200), `clear` | console messages, exceptions and browser log entries of the active tab (last 500 kept); read-only |
| `BrowserNetwork` | `filter`, `type`, `status` (`failed` \| `error` \| `2xx`...), `limit`, `clear` | requests of the active tab (last 500 kept), no headers or bodies; read-only |
| `BrowserWait` | one of `text`, `text_gone`, `selector`, `url`, `navigation`; `timeout` (ms, ≤60000) | a timeout is a normal result; read-only |
| `McpAuthenticate` | `{}` | template of `mcp__<server>__authenticate` for a remote MCP server that needs authorization; `{{server}}` in the description is filled by the client |

The execution semantics are implemented by the client and must match these descriptions.

## Per-model overrides

`overrides/<glob>/` mirrors the layout above. `<glob>` is matched (case-insensitive, `*` and `?` wildcards)
against the model alias **and** the upstream model id, e.g. `overrides/gpt-*/`, `overrides/deepseek-*/`,
`overrides/claude-sonnet/`.

- Every matching override directory is applied on top of the base files, from least to most specific
  (fewer wildcards and longer literal text = more specific); an exact alias match wins last.
- A file in an override replaces the file with the same relative path, or adds it (a new section, tool or reminder).
- An **empty** file deletes the base file (e.g. an empty `tools/Grep/description.md` removes the Grep tool for that model).
