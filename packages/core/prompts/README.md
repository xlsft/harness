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

Trailing whitespace of each file is trimmed. This README is not part of the bundle.

Server-side prompts live next to the bundle files but are never sent to clients:

| Path | Used by |
|---|---|
| `decision/system.md` | system prompt of the `llm` decision provider (`harness.decision.v1.DecisionService`, the client's `auto` mode) |
| `decision/classify/description.md` + `decision/classify/schema.json` | the `classify` tool the classifier must call (`{decision: allow\|deny\|ask, reason, categories[], confidence}`) |

They follow the same per-model override rules as everything else (e.g. `overrides/qwen-*/decision/system.md`).

All texts are written from scratch (clean-room); never copy Claude Code's prompts.

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
| `todo_nudge` | — | several turns without TodoWrite on a multi-step task |
| `plan_mode` | `plan_file_path` | plan mode is active |
| `plan_approved` | `plan_file_path` | the user approved the plan (ExitPlanMode) |
| `plan_rejected` | `feedback` | the user asked to keep planning |
| `background_task_done` | `task_id`, `exit_code`, `output_path` | a background Bash command finished |
| `compacted` | — | first message after a compaction summary |
| `attribution` | `commit`, `pr` | commit / PR attribution lines |

## Tools

| Tool | Input | Notes |
|---|---|---|
| `Read`, `Write`, `Edit`, `Glob`, `Grep` | see `schema.json` | M0 file tools |
| `Bash` | `command`, `timeout`, `description`, `run_in_background` | background: returns a task id + output file path at once; a foreground command that hits its timeout is moved to the background; completion arrives as `background_task_done` |
| `TaskStop` | `task_id` | stops a background task |
| `TodoWrite` | `todos: [{content, activeForm, status}]` | full replacement; exactly one `in_progress` |
| `AskUserQuestion` | `questions: [{question, header (≤12), options: [{label, description}] (2–4), multiSelect}] (1–4)` | |
| `EnterPlanMode` | `{}` | |
| `ExitPlanMode` | `plan` (Markdown) | the client writes it to the plan file and asks the user to approve |

The execution semantics are implemented by the client and must match these descriptions.

## Per-model overrides

`overrides/<glob>/` mirrors the layout above. `<glob>` is matched (case-insensitive, `*` and `?` wildcards)
against the model alias **and** the upstream model id, e.g. `overrides/gpt-*/`, `overrides/deepseek-*/`,
`overrides/claude-sonnet/`.

- Every matching override directory is applied on top of the base files, from least to most specific
  (fewer wildcards and longer literal text = more specific); an exact alias match wins last.
- A file in an override replaces the file with the same relative path, or adds it (a new section, tool or reminder).
- An **empty** file deletes the base file (e.g. an empty `tools/Grep/description.md` removes the Grep tool for that model).
