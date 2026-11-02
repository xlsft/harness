# Prompt bundle

Prompts live on the server in the prompt registry, and the client assembles model requests from them. The client has no prompt texts of its own, except a minimal emergency fallback for a first run without network access.

## `GET /api/prompts/bundle?model=<model_id>`

- Requires `Authorization`.
- Supports `If-None-Match` and returns `304` when the bundle hasn't changed.
- The `ETag` carries the bundle version.
- Model overrides are already applied: the client receives final texts for the requested model.

```json
{
  "version": "2026-11-02.3",
  "model": "claude-sonnet",
  "system": [
    { "id": "identity", "text": "…" },
    { "id": "safety", "text": "…" },
    { "id": "style", "text": "…" },
    { "id": "doing_tasks", "text": "…" },
    { "id": "tool_policy", "text": "…" },
    { "id": "__dynamic_boundary__" },
    { "id": "env", "text": "Working directory: {{cwd}}\nPlatform: {{platform}}\nShell: {{shell}}\nOS Version: {{os_version}}\nToday's date: {{date}}\nIs a git repository: {{is_git}}\nModel: {{model_display_name}} ({{model_id}})" }
  ],
  "tools": {
    "Read":  { "description": "…", "input_schema": { … } },
    "Write": { "description": "…", "input_schema": { … } },
    "Edit":  { "description": "…", "input_schema": { … } },
    "Bash":  { "description": "…", "input_schema": { … } },
    "Glob":  { "description": "…", "input_schema": { … } },
    "Grep":  { "description": "…", "input_schema": { … } }
  },
  "reminders": {
    "project_instructions": "<system-reminder>\nAs you answer the user's questions, you can use the following context:\n{{content}}\n</system-reminder>",
    "file_changed": "…{{path}}…",
    "empty_file": "…",
    "interrupted": "[Request interrupted by user]"
  },
  "compaction": { "instruction": "…" },
  "title": { "instruction": "…" }
}
```

## Rules

- **Static and dynamic parts.** `system` sections before `__dynamic_boundary__` are static: the client puts them into `system` with `cache: true` on the last block. Sections after the marker are dynamic; the client fills in their placeholders.
- **Placeholders** use `{{name}}`. The client leaves an unknown placeholder empty and logs a warning.
- **Tools.** `tools` defines the description and schema of each tool. The client implements the **execution semantics**, which are specified in [`docs/PLAN.md`](../PLAN.md) §2.2 and must match the description text. The client doesn't expose a tool to the model unless the bundle defines it.
- **Frozen per session.** The client records `version` when a session starts and keeps using it until the session ends or is compacted. Switching mid-session breaks the prompt cache.
- **Client cache.** Bundles are cached at `~/.harness/cache/prompts/<model>/<version>.json`. If the server is unreachable, the client uses the last cached bundle. A new turn still can't run without the server, because the LLM is behind it too.
- **M0 source.** The server builds the bundle from files under `packages/core/prompts/**`. An editor and DB-backed versions arrive in M2.
- **Clean-room.** We write prompt texts ourselves and never copy Claude Code's prompts verbatim.
