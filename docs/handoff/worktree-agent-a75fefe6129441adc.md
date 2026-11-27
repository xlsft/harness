# Handoff: second-wave import readers (worktree-agent-a75fefe6129441adc)

## Goal

User-level readers (and missing folder patterns) for Settings > Import: Cline, Roo Code, Kilo Code, Kiro, OpenCode,
Continue, Zed, JetBrains Junie. Logos in packages/ui/app/utils/providerLogos.ts, entries in
packages/ui/app/lib/import/sourcesMock.ts, a per-tool table in docs/design/import.md ("Other tools: the import engine").

## Done

- Merged origin/main (at b4cdc468). Read the engine (importer/source.go, engine.go, engine_mcp.go, sources/*).
- Checked every path and format below against official docs. **No code written yet**: the coordinator stopped the
  work during the design pass. This file is the only commit.

## Verified paths and formats (cite these URLs in the reader comments)

**Cline**
- Global rules: `~/Documents/Cline/Rules` (Windows `Documents\Cline\Rules`), also `~/.cline/rules`, `~/Cline/Rules`;
  `.md`, optional `paths:` frontmatter (the only conditional). https://docs.cline.bot/features/cline-rules
- Workflows (→ commands, invoked `/name.md`): global `~/Documents/Cline/Workflows`, CLI `~/.cline/data/workflows/`;
  project `.clinerules/workflows/*.md` (add a folder pattern). https://docs.cline.bot/customization/workflows ,
  https://docs.cline.bot/features/slash-commands/workflows/best-practices
- MCP: `cline_mcp_settings.json`, key `mcpServers`; CLI `~/.cline/data/settings/cline_mcp_settings.json`
  (https://docs.cline.bot/mcp/configuring-mcp-servers); extension:
  `<UserConfigDir>/Code/User/globalStorage/saoudrizwan.claude-dev/settings/cline_mcp_settings.json` (not on
  docs.cline.bot; from third-party guides). Fields: command, args, env, url, headers, type (`streamableHttp` | `sse`),
  disabled, autoApprove. **A bare `url` without `type` means SSE in Cline**: set `Transport = "sse"` when
  Transport == "" && URL != "" && Command == "".

**Roo Code** (docs moved to https://roocodeinc.github.io/Roo-Code/...)
- Global rules: `~/.roo/rules/` and `~/.roo/rules-{modeSlug}/`, read recursively, `.md`/`.txt`
  (features/custom-instructions).
- Custom modes (features/custom-modes): global `settings/custom_modes.yaml` or `.json` in the extension's
  globalStorage (`rooveterinaryinc.roo-cline`, lower-case folder), project `.roomodes` (YAML or JSON). Top key
  `customModes`; fields slug, name, description, roleDefinition, whenToUse, customInstructions, groups (`read`,
  `edit`, `command`, `mcp`; restricted edit is `[edit, {fileRegex, description}]`).
- MCP (features/mcp/using-mcp-in-roo): global `mcp_settings.json` (same globalStorage `settings/`), project
  `.roo/mcp.json`, key `mcpServers`; type `streamable-http` | `sse` is required for url servers.

**Kilo Code**: the current extension is built on the Kilo CLI (an OpenCode-style config)
- `~/.config/kilo/kilo.jsonc` / `kilo.json`; project `kilo.jsonc`, `.kilo/kilo.json(c)`; `mcp` key with
  `type: local` (command array, environment, enabled) / `remote` (url, headers, enabled, oauth); `instructions` key
  for rules. https://kilo.ai/docs/automate/mcp/using-in-kilo-code , https://kilo.ai/docs/customize/custom-rules
- Agents: global `~/.config/kilo/agent/<name>.md`; project `.kilo/agents/`, `.kilo/agent/`, legacy `.kilocode/agents/`.
  Frontmatter description, mode, model, permission, ...; body is the prompt. https://kilo.ai/docs/customize/custom-modes
- Legacy (worth reading; the new extension migrates it once): globalStorage
  `kilocode.kilo-code/settings/custom_modes.yaml` (same page), `.kilocodemodes`, global rules `~/.kilocode/rules/`
  (https://kilocode.ai/docs/advanced-usage/custom-rules), `mcp_settings.json` in the same settings folder (the Roo
  layout; Kilo does not document it).

**Kiro**
- Steering: global `~/.kiro/steering/*.md` (an AGENTS.md there is always included); inclusion
  always / fileMatch (fileMatchPattern) / manual / auto. Reuse `kiroSteering` from others.go.
  https://kiro.dev/docs/steering/
- MCP: `~/.kiro/settings/mcp.json` and `.kiro/settings/mcp.json`, key `mcpServers`, `${VAR}` env syntax, a bare url is
  remote HTTP. https://kiro.dev/docs/mcp/configuration/
- Hooks: `.kiro/hooks/` and global `~/.kiro/hooks/` (CLI V3), JSON `{version: "v1", hooks: [{name, trigger
  (PascalCase: PostFileSave, PreToolUse, PostToolUse, Stop), matcher, action: {type command|agent, command|prompt}}]}`;
  older `*.kiro.hook` files exist. https://kiro.dev/docs/hooks/
- Proposed decision: hooks become `StatusNoted` items (Kind settings), one per hook, "set up by hand in Settings >
  Hooks" (tool names and events do not map 1:1). Specs `.kiro/specs/<feature>/*.md` become `StatusNoted` items: they
  stay in the repo, and the note suggests referencing them from AGENTS.md. Record this in import.md.

**OpenCode** (V2 renamed keys; support both)
- Config: `~/.config/opencode/opencode.json(c)`; project `opencode.json(c)` or `.opencode/opencode.json(c)`; JSONC.
  `{env:VAR}` and `{file:path}` substitutions (turn `{env:X}` into `${X}` so the engine makes secret references).
  https://opencode.ai/docs/config/
- MCP: V1 `mcp: {name: {...}}`, V2 `mcp: {servers: {...}}`; local `type: local`, `command` array, `environment`,
  `cwd`, `disabled` (V1 `enabled`); remote `type: remote`, `url` (Streamable HTTP), `headers`, `oauth`.
  https://opencode.ai/docs/mcp-servers/
- Agents: `~/.config/opencode/agents/<name>.md` (V1 `agent/`), project `.opencode/agents/`; a nested path is part of
  the id; config key `agents` (V1 `agent`), prompt field `system` (V1 `prompt`). https://opencode.ai/docs/agents/
- Commands: `~/.config/opencode/commands/` (legacy `command/`), `.opencode/commands/`; nested folders give `a/b`;
  frontmatter description, agent, model, subagent; config key `commands` with a required `template`.
  https://opencode.ai/docs/commands/
- Rules: global `~/.config/opencode/AGENTS.md` (→ instructions). In `instructions`, `~/` and absolute paths and globs
  can be imported at user level; relative entries are per project; http(s) URLs are set up by hand.
  https://opencode.ai/docs/rules/
- Config dir: `$XDG_CONFIG_HOME` or `~/.config` on every OS (Env.ConfigHome, else Home/.config).

**Continue**
- `~/.continue/config.yaml` (Windows `%USERPROFILE%\.continue\config.yaml`); when it exists, the legacy `config.json`
  is not loaded. https://docs.continue.dev/customize/deep-dives/configuration , https://docs.continue.dev/reference
- YAML: rules (strings, objects with name/rule/globs/regex/description/alwaysApply, or `uses:`), prompts (name,
  description, prompt, or `uses:`), mcpServers list (name, type stdio|sse|streamable-http, command, args, env, cwd,
  url, requestOptions.headers); turn `${{ secrets.NAME }}` into `${NAME}`. models/context/docs/data are set up by
  hand. `uses:` hub blocks are set up by hand, with a note. https://docs.continue.dev/customize/deep-dives/mcp
- Legacy config.json: systemMessage, customCommands (name, prompt, description), experimental.modelContextProtocolServers
  (`transport: {type, command, args}`). https://docs.continue.dev/reference/json-reference
- Folder: add `.continue/prompts/*.md` (frontmatter name, description, invokable) and `.continue/mcpServers/*.yaml|json`.
  The docs do not mention global block folders in ~/.continue: do not read them.

**Zed**
- settings.json: `~/.config/zed/settings.json` (Linux `$XDG_CONFIG_HOME/zed`); Windows assumed `%APPDATA%\Zed`
  (AGENTS.md is documented there). JSONC. `context_servers`: `{command, args, env}` or `{url, headers}`; older
  `{source: custom, command: {path, args, env}}`; extension servers (only `settings`) are set up by hand.
  https://zed.dev/docs/ai/mcp , https://zed.dev/docs/configuring-zed
- User instructions: `~/.config/zed/AGENTS.md` (Windows `%APPDATA%\Zed\AGENTS.md`). Skills and AGENTS.md replaced the
  Rules Library (https://zed.dev/docs/ai/instructions), so there is no database to read. Folder: `.zed/settings.json`.

**Junie**
- Global guidelines `~/.junie/AGENTS.md` (JUNIE_HOME overrides it; ignore env). MCP `~/.junie/mcp/mcp.json` and
  project `.junie/mcp/mcp.json`, key `mcpServers`. https://junie.jetbrains.com/docs/junie-cli-mcp-configuration.html ,
  https://junie.jetbrains.com/docs/guidelines-and-memory.html (returns 403 to fetchers; confirmed through search).

## Design decisions (not yet implemented)

- Files: `sources/userlevel.go` (shared helpers: walk a rules dir, markdown commands, VS Code globalStorage paths for
  Code, Code - Insiders, VSCodium, Cursor, Windsurf, duplicate-name dedupe that marks later ones Skipped), `cline.go`
  (Cline), `roo.go` (Roo + Kilo legacy + modes parser), `opencode.go` (OpenCode + Kilo's new config, one parser for
  both), `kiro.go`, `continue.go`, `zed.go`, `junie.go`. Move Kiro/Continue/Junie/Zed out of `others.go` (leave Aider).
  Do not edit helpers.go, sources_test.go or testdata/home|project: the first-wave agent edits those, and
  TestAnyFolder expects an exact item count in testdata/project. Use new fixtures `testdata/w2home` and
  `testdata/w2project`, and a new `sources_w2_test.go`.
- Modes → agents: harness agent frontmatter keys are `name`, `description`, `model`, `read_only`
  (packages/client/subagent/types.go). Write `name: <slug>`, `description: <whenToUse or description>`, and
  `read_only: true` when the groups have neither edit nor command; the body is roleDefinition + customInstructions; the
  note lists the tool groups (and fileRegex). Do not copy other tools' model names or tool maps.
- OpenCode/Kilo agents and commands: fresh frontmatter (name and description only); the note says what was dropped
  (mode, model, permissions, agent, subagent). Nested agent ids are flattened with `-`; commands use `a:b` (the engine
  maps it to a/b.md). Config agents without a prompt (overrides of built-in agents) are Skipped.
- Readers set `StatusNoted` for items set up by hand; the engine keeps a reader's status (engine.go plan()).

## Gotchas

- `ParseMCPServers` treats a bare url as Streamable HTTP; Cline means SSE by it.
- The engine's item ID includes Source, but the same MCP name in two files would make two creates: dedupe in the
  reader.
- The worktree's shell guard refuses compound commands (heredoc + git). Run git commands plainly, one per call.
- packages/ui/node_modules is missing in this worktree.

## Next steps

1. Implement the files above with fixtures and tests: scan the user level with a temp Home per GOOS, run with the
   `recorder` MCPTarget, check that no secret values go through without Include secrets, scan w2project as a folder.
2. Logos from @lobehub/icons-static-svg@1.97.0 (cline, roocode, kilocode, kiro, opencode, continue, zed, junie; check
   the names), stripped and with prefixed ids; add the sources to sourcesMock.ts, each on its own line.
3. Per-tool table in docs/design/import.md; update "Not done yet".
4. Checks: gofmt, go vet, `go test ./importer/... ./mcp`; in the UI eslint, nuxt typecheck, vitest, i18n-extract
   (symlink node_modules from the main checkout and never commit the link).

## Known failing checks

None introduced (no code changed).
