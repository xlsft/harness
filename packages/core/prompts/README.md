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
| `webfetch_summary` | `url`, `prompt`, `content` | not a reminder: the WebFetch summarization prompt. The client fills it with the fetched page (as Markdown) and the tool's `prompt`, sends it as the only user message of an `auxiliary` request to the small-slot model and returns the reply as the tool result. Not wrapped in `<system-reminder>` |

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
| `EnterPlanMode` | `{}` | |
| `ExitPlanMode` | `plan` (Markdown) | the client writes it to the plan file and asks the user to approve |
| `ToolSearch` | `query`, `max_results` | offered only while MCP tools are deferred; `select:a,b` or keywords; returns full definitions and makes those tools callable |
| `WebSearch` | `query`, `allowed_domains`, `blocked_domains` | runs on the server (`harness.search.v1.SearchService`); the client formats title, URL, snippet and date and adds a reminder to cite sources; the domain lists cannot be combined |
| `WebFetch` | `url`, `prompt` | fetched locally (http upgraded to https except for private hosts, same-host redirects followed, a cross-host redirect returned as a notice, 10 MB / 60 s limits, HTML converted to Markdown, binary refused, 15-minute cache per URL); the answer comes from the small-slot model with `reminders/webfetch_summary.md` |
| `Show` | `target` (`file` \| `diff` \| `url`), `path`, `line`, `end_line`, `url` | opens the target in the UI's Files, Diff or Browser pane (`ShowRequested` event); read-only, no permission; without a UI that can open panes it reports that nothing was shown |
| `Visual` | `id`, `title`, `html` | renders a self-contained HTML document in the transcript (sandboxed frame, app colours as CSS variables, `harness.send()` posts a `[from visual <id>]` user message); the same `id` adds a version; read-only, no permission; without a UI that renders it reports that nothing was shown |
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
