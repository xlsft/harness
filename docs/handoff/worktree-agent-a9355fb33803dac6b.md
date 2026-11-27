# Handoff: user-level import readers, first wave

Branch: `worktree-agent-a9355fb33803dac6b`. The agent was stopped during research. **No code was written and nothing is committed except this
file.** All the paths and formats below were checked against the sources named.

## Goal

Add user-level readers to Settings > Import (engine in `packages/client/importer`, readers in `importer/sources/<tool>.go`) for
Codex CLI (including its sessions), Gemini CLI, Cursor, GitHub Copilot and Windsurf. Add their logos to
`packages/ui/app/utils/providerLogos.ts` and the sources to `packages/ui/app/lib/import/sourcesMock.ts`, with fixture tests. Read
`docs/design/import.md`, "Other tools: the import engine", first.

## Done per tool

Nothing yet: no commits. Research results are below.

## Left to do, in order

1. **Engine: importing sessions** (`importer/source.go`, `engine.go`). Today `KindSession` is always `StatusNoted`. Proposed design:
   - Add `Found.Session *SessionSource{ID, Cwd, Title, Modified time.Time, Bytes int64, Convert func(ctx) ([]session.Entry, []string /*notes*/, error)}`.
     `importer` may import `packages/client/session`, which depends only on `llm` and `usagelimit`, so there is no cycle.
   - Add `ProjectsDir`, `Queue SessionQueue`, `UploadSessions`, `HoldSession`, `SessionDone` to `EngineOptions`. Wire them in
     `server/import_sources.go` the same way `server/import.go` RunClaudeImport does: `syncclient.NewUploader(...)`, `s.holdSession`,
     `sessionUploaded`, and `s.refreshImportListing(ctx)` before a scan or run.
   - Target: `ProjectsDir/<session.Slug(cwd)>/<id>.jsonl`. Write the file through a temporary file with `session.OpenWriter` +
     `Seed`, chain `parentUuid` yourself, then `os.Chtimes` it to the source's mtime and rename it into place.
   - Status rule, since the sizes differ from the source:
     - the copy's mtime equals the source's: `exists` ("already imported"), unless `SessionDone` is false (then planned, to finish the upload);
     - the copy is newer: `skipped` ("continued in harness");
     - the copy is older: planned ("has newer messages").
     - `HoldSession` gives `skipped`.
   - After a run: Queue, then Flush, gives `StatusUploaded` or `StatusQueued`, as `importer.go` `session()` does.
   - UI (`components/settings/SettingsImportSource.vue`): session items must start unchecked (opt-in, like Claude Code's).
2. **Codex reader** `sources/codex.go`. This needs a TOML parser. `go.mod` has none, so add `github.com/BurntSushi/toml@v1.5.0`. For
   compressed rollouts (`*.jsonl.zst`), either add `github.com/klauspost/compress@v1.18.0` (zstd), or list those sessions as manual with a
   note.
3. Gemini, Cursor, Copilot and Windsurf user-level readers.
4. Logos and the mock sources. Run `node scripts/i18n-extract.mjs --apply` if the UI text changes.
5. Write fixtures and tests (`sources/testdata/home/...`, `sources_test.go`), then add the per-tool table to `docs/design/import.md` and
   update "Not done yet" there.

## Verified formats and URLs

### Codex CLI (home = `$CODEX_HOME`, else `~/.codex`; take `CODEX_HOME` through `Env`, so add a field to `Env`)

- Global instructions: `AGENTS.override.md` if it is non-empty, else `AGENTS.md`. Only the first non-empty file counts
  (https://learn.chatgpt.com/docs/agent-configuration/agents-md.md).
- `config.toml` (https://learn.chatgpt.com/docs/config-file/config-reference.md), `[mcp_servers.<id>]` fields:
  - `command`, `args`, `env` (map), `env_vars` (array of names, or `{name, source}`) → env `NAME = "${NAME}"`;
  - `cwd`, `url`, `bearer_token_env_var` → header `Authorization: Bearer ${VAR}`;
  - `http_headers` (map), `env_http_headers` (header → env var name) → `${VAR}`;
  - `enabled = false` → Disabled;
  - `auth = "oauth"` and `oauth.*` → add a note. `enabled_tools` / `disabled_tools` and the timeouts also go in the note.
  - `model`, `model_reasoning_effort`, `approval_policy` and `sandbox_mode` become a settings item (manual at user level). Name the values in the note.
  - Profiles: `$CODEX_HOME/<name>.config.toml`. List them as manual.
- Custom prompts (deprecated by Codex, still read): only the top level of `~/.codex/prompts/*.md`. Frontmatter has `description` and `argument-hint`.
  Placeholders:
  - `$1`..`$9` and `$ARGUMENTS` are the same in harness (`skills/expand.go`);
  - `$$` means a literal `$`;
  - named `$UPPER` placeholders (supplied as KEY=value): add an `arguments: [UPPER, ...]` frontmatter list so harness fills them by position, and note it.
  - Codex invokes these as `/prompts:<name>`; the command name here is `<name>` (https://learn.chatgpt.com/docs/custom-prompts.md).
- Custom agents: `~/.codex/agents/*.toml` (and `.codex/agents/*.toml` in a project) with `name`, `description` and `developer_instructions`
  (required), plus `model` and `model_reasoning_effort`. Each becomes an AGENT document: frontmatter `name`/`description`, body taken from
  `developer_instructions` (https://learn.chatgpt.com/docs/agent-configuration/subagents.md).
- Skills: `$HOME/.agents/skills/<name>/SKILL.md` is the user level. Codex's bundled `.system` skills must not be read
  (https://learn.chatgpt.com/docs/build-skills.md). `~/.agents/skills` is shared with Cursor and Gemini, so read it in one reader only (Codex).
- Sessions: `~/.codex/sessions/YYYY/MM/DD/rollout-<timestamp>-<uuid>.jsonl`, and cold files compressed to `.jsonl.zst`
  (`codex-rs/rollout/src/compression.rs`). Each line is `{timestamp, ordinal?, type, payload}` (`codex-rs/history/src/lib.rs` RolloutLine,
  `rollout_payload.rs` RolloutItemWire, tag "type", snake_case). Types:
  - `session_meta`: payload has `id`, `session_id`, `timestamp`, `cwd`, `originator`, `cli_version`, `source`, `forked_from_id`,
    `parent_thread_id`, `git{commit_hash, branch, repository_url}`. `source` is `"cli"`/`"vscode"`/`"exec"`/`"mcp"` or `{"subagent":...}`.
    Skip subagent threads with a note.
  - `turn_context`: `cwd`, `model`, `effort`. Use `model` on the assistant messages.
  - `response_item`: a ResponseItem, flattened (`codex-rs/protocol/src/models.rs`):
    - `message`: `role` user/assistant/developer, `content` of `input_text`/`output_text`/`input_image{image_url}`.
    - `reasoning`: `summary[]`, `encrypted_content`. Leave it out and add a note.
    - `function_call`: `name`, `arguments` (a JSON string), `call_id`.
    - `function_call_output`: `call_id`, `output` as a string, or an array of `{type: input_text, text}`. Older files have `{content, success}`.
    - `custom_tool_call` (`input` string) and `custom_tool_call_output`; `local_shell_call` (`action.exec.command[]`); `web_search_call`;
      `compaction` (encrypted).
  - `compacted`: `message` (the summary), `replacement_history`. In harness this becomes a `compact_boundary` system entry
    (`LogicalParentUUID`, nil parent), then a user entry with `IsCompactSummary` and `IsVisibleInTranscriptOnly` (see `agent/compact.go:314`).
  - `event_msg`: `{type: user_message|agent_message|token_count|...}`. Use it for the title (the first `user_message`). Skip it otherwise:
    `response_item` is what the model saw.
  - Very old files (before about Aug 2025): the first line is a bare `{id, timestamp, instructions}` and the later lines are bare ResponseItems.
- Mapping decisions:
  - Drop `developer` messages, and user messages that are Codex's injected context (text starting with `<environment_context>`,
    `<user_instructions>` or `# AGENTS.md instructions`). Count them in a note.
  - `shell`, `local_shell_call` and `exec_command` become harness `Bash` `{command}`; for `["bash","-lc",X]`, take X.
  - Other tools keep their Codex names as `tool_use`. Their outputs become `tool_result` blocks in user entries.
  - Sanitize tool ids to `[A-Za-z0-9_-]`.
  - Images with a `data:` URL become an `image` block (`llm.Source` base64); otherwise write a text placeholder.

### Gemini CLI (`~/.gemini`)

- `GEMINI.md`; `settings.json` `context.fileName` (string or array) names other context files (https://geminicli.com/docs/cli/gemini-md/).
- MCP: `settings.json` `mcpServers`. A bare `url` is SSE and `httpUrl` is HTTP; reuse `geminiMCP` in `sources/gemini.go`.
- Commands: `~/.gemini/commands/**/*.toml` with `prompt` (required) and `description`. `git/commit.toml` becomes `git:commit`. Conversions:
  - `{{args}}` → `$ARGUMENTS`. With no `{{args}}`, Gemini appends the arguments, so leave the prompt as it is.
  - `!{cmd}` → harness `` !`cmd` `` (single line, no backticks), with `{{args}}` inside → `$ARGUMENTS`.
  - `@{path}`: no equivalent; leave it as written and add a note (https://geminicli.com/docs/cli/custom-commands/).
- Agents: `~/.gemini/agents/*.md` (markdown with YAML frontmatter) → AGENT documents. Skills: `~/.gemini/skills/`
  (https://geminicli.com/docs/core/subagents/, https://geminicli.com/docs/cli/skills/).
- Extensions: `~/.gemini/extensions/<dir>/gemini-extension.json`:
  - `mcpServers`: expand `${extensionPath}` to the absolute extension directory and `${/}` to the separator; `${workspacePath}` gets a note.
  - `contextFileName`, else `GEMINI.md` in the extension directory → instructions.
  - `commands/` → commands. Gemini prefixes them with `<ext>.` only on a conflict.
  - `settings[]` with `envVar`/`sensitive` → secret references.
  - Source: https://geminicli.com/docs/extensions/reference/.

### Cursor (`~/.cursor`)

- `~/.cursor/mcp.json` `mcpServers` (https://cursor.com/docs/context/mcp).
- `~/.cursor/skills/` → skills (https://cursor.com/docs/context/skills).
- User Rules live in Cursor's settings (Customize → Rules), not in a file. Cursor's own docs say "User rules ... are not stored on the file
  system" (https://cursor.com/docs/context/rules). Emit one `StatusNoted` item: "User Rules in Cursor's settings: copy them by hand".

### GitHub Copilot

Two user-level families:

- **Copilot user folders** (`$COPILOT_HOME`, else `~/.copilot`):
  - `copilot-instructions.md` → instructions; `instructions/**/*.instructions.md` → rules (`applyTo` → paths);
  - `agents/*.agent.md` → agents; `skills/` → skills;
  - `mcp-config.json`, top-level `mcpServers`.
  - Sources: https://code.visualstudio.com/docs/agents/reference/custom-instructions-reference.md,
    https://code.visualstudio.com/docs/agents/reference/mcp-configuration.md,
    https://code.visualstudio.com/docs/agent-customization/migrate-customizations.md.
- **VS Code profile user data** (deprecated "Local harness", still read). The folder is `Env.UserConfigDir()/{Code, Code - Insiders}/User`;
  each profile is `.../User/profiles/<id>/`.
  - `prompts/*.prompt.md` → commands. Frontmatter has `description`, `argument-hint`, `agent`, `model` and `tools`.
    - `${input:name[:placeholder]}`: with one input, it becomes `$ARGUMENTS`; with several, `$name` plus an `arguments:` list.
    - `${file}`, `${selection}` and `${workspaceFolder}` stay as written, with a note.
  - `prompts/*.instructions.md` → rules; `prompts/*.agent.md` and legacy `*.chatmode.md` → agents.
  - `mcp.json` with top-level `servers` (JSONC) and `inputs`; `${input:id}` becomes a secret reference, which the engine already does.
  - The `User/prompts` folder is from the docs' "user data (specific to your VS Code profile)" plus common use. Check it again.

### Windsurf

Not researched yet. Check https://docs.windsurf.com/windsurf/cascade/memories (now https://docs.devin.ai/desktop/cascade/memories) for
the global rules path (`~/.codeium/windsurf/memories/global_rules.md`) and the workflows folder (`~/.codeium/windsurf/global_workflows/`?).
Check https://docs.windsurf.com/windsurf/cascade/mcp for `~/.codeium/windsurf/mcp_config.json` (`mcpServers`, `serverUrl` for HTTP).

## Gotchas

- The repo has no TOML library and no zstd library. A dependency is needed for both Codex `config.toml` and Gemini commands.
- The harness session format is the Claude Code JSONL shape (`session.Entry`). The Claude import copies files byte for byte, so its
  size-based `copyStatus` does not fit converted sessions. Use mtime instead, as described above.
- Never read the real `~/.codex`, `~/.gemini`, `~/.cursor`, `~/.copilot` or the VS Code folders on this machine. Tests go through `Env.Home`.
- Another agent edits `providerLogos.ts` and `sourcesMock.ts` at the same time: keep one entry per line.

## Known failing checks

None: there are no code changes.
