# Import and what is read at runtime

The rule: nothing is read from `.claude/` or `CLAUDE.md` at runtime. They are import sources. xlsft`s harness keeps compatibility with their formats and imports from them, but live data comes from its own files. Package `importer` is the only reader; a grep for `.claude` and `CLAUDE` across `packages/client` should find, outside it, only what "What stayed" lists.

## Read at runtime

| What | From |
|---|---|
| Settings | Policy documents, CLI flags, `.harness/settings.local.json`, `.harness/settings.json`, synced user and org documents. Before the first sync there is no user level. |
| "Always allow" rules | Written to `<repo root>/.harness/settings.local.json`. |
| Instructions | The managed file (`/Library/Application Support/harness/AGENTS.md`, `/etc/harness/AGENTS.md`, `C:\Program Files\harness\AGENTS.md`), synced user memory and user rules, then `AGENTS.md`, `.harness/AGENTS.md`, `.harness/AGENTS.local.md` and `.harness/rules/**` from the filesystem root down to the working directory, subfolders lazily, auto memory. |
| Skills, commands | Organization and imported documents, `~/.harness/{skills,commands}`, `.harness/{skills,commands}` when trusted, plugins. |
| Agents | Synced user documents, `~/.harness/agents`, `.harness/agents` when trusted, plugins. |
| Output styles | Bundle, `~/.harness/output-styles`, synced user documents, `.harness/output-styles`, plugins. |
| Workflows | `~/.harness/workflows`, `.harness/workflows` when trusted. |
| Project MCP servers | `.mcp.json`. It is the open MCP project format, not a `.claude` file, so it stays live, under folder trust and per-device approval as before. |

The loader also refuses, at runtime, an `@import` of a `CLAUDE.md`, a `CLAUDE.local.md` or anything below a `.claude` folder, and an `AGENTS.md` that is a symbolic link to one of those. What counts is the part of the path inside the project, since a checkout can sit below some `.claude` (agent worktrees do); ancestors below such a folder are skipped.

The setting `claudeMdMode` keeps its key and values so that existing files read without problems. Only `managed-only` still does anything.

## What import covers

User level, from Settings > Import and `harness-client import claude`:

| From `~/.claude` | To |
|---|---|
| `CLAUDE.md` | MEMORY document; `@imports` below `~/.claude` are taken in, others stay references |
| `rules/**/*.md` | RULE documents |
| `settings.json` | SETTINGS, PERMISSION_RULES and HOOK documents; commands naming `.claude/hooks/` are rewritten |
| `skills`, `agents`, `commands`, `output-styles` | documents |
| `workflows`, `hooks` | copied to `~/.harness` on this device; there is no document for them |
| `projects/**/*.jsonl` | sessions, import-only as before |
| `~/.claude.json` MCP servers | MCP servers of this device, through the engine (see "Other tools" below) |

Project level, from the offer, Settings > Import and `harness-client import --project [folder]` (`importer/project.go`):

| From | To |
|---|---|
| `CLAUDE.md`, `.claude/CLAUDE.md` | `AGENTS.md`, appended as blocks marked with HTML comments the loader drops |
| `.claude/AGENTS.md` | `.harness/AGENTS.md` |
| `CLAUDE.local.md` | `.harness/AGENTS.local.md`, kept out of git through `info/exclude` |
| `.claude/rules/**` | `.harness/rules/**` |
| `.claude/settings.json` | `.harness/settings.json`, merged |
| `.claude/settings.local.json` | `.harness/settings.local.json`, merged, kept out of git, only in a trusted folder |
| `.claude/hooks/**` | `.harness/hooks/**`; hook and status line commands are rewritten to match |
| `.claude/{skills,commands,agents,output-styles,workflows}/**` | the same under `.harness` |

The same applies to subfolders with such files of their own, except dot folders, dependency and build folders, and nested repositories.

Decisions worth keeping:

- **Consent.** Scanning writes nothing. Hooks and allow rules are flagged in the list and pass folder trust once imported, like any `.harness` file.
- **Local settings wait for trust.** `.harness/settings.local.json` applies without folder trust, and a repository can commit a `.claude/settings.local.json`. Importing it into an untrusted folder would hand it rights its source never had, so that item is left out with a note until the folder is trusted.
- **`CLAUDE.local.md` does not go into `AGENTS.md`.** That file is committed and this one is private, hence `.harness/AGENTS.local.md`, a runtime source added for it.
- **Running twice.** A marked block with the same hash is left alone. A changed source is left alone unless forced, which replaces only that block. Text the user moved out of the block is still recognized. Settings merge by union, the existing value winning unless forced. Copied files that differ stay unless forced.
- **`@imports`.** A file no longer read is taken in, a project file stays a reference with its path adjusted, a self-reference is dropped. Files outside the folder are never copied: the reference stays, and asks for approval when loaded.
- **Links.** Import never reads through a link leading out of the folder, and never writes through a link, so `.harness -> .claude` cannot make it modify `.claude`. `AGENTS.md -> CLAUDE.md` becomes a file of its own.
- **`.claude/` is never modified.**

The offer shows when there is something to bring, the folder has no `.harness` of its own and it was not dismissed; `.harness/worktrees` and `.harness/settings.local.json` don't count as its own, since the client puts those there itself. Dismissals are in `~/.harness/project-import.json`, on this device, never synced. The home folder gets no offer: its `.claude` is user-level. The app shows the notice on the new session screen (`NewProjectImport.vue`). A terminal has no notice to dismiss, so the CLI prints one line once.

## What stayed, and why

- `.claude-plugin/` manifests inside plugin packages: format compatibility.
- `$CLAUDE_PROJECT_DIR`, `${CLAUDE_SKILL_DIR}`, `${CLAUDE_PLUGIN_ROOT}` and the like in hook and skill files, and the JSON shapes: format compatibility.
- `.claude` in protected paths (`permissions/paths.go`, `sandbox/policy.go`, `sandbox/seatbelt.go`): the model still must not write there.
- ACP agents such as Claude Code read their own files.

## Known limits

- `.harness` as a symbolic link to `.claude` is still followed by the settings, skill and style readers. The loader's link check covers instruction files only; import refuses such a target.
- A hook command or skill body naming a file under `.claude` that is not one of the copied folders keeps naming it. Import says so in the settings item's note and leaves it; permission rule specifiers and skill bodies are not rewritten at all.
- User-level `~/.harness/hooks` and `workflows` are per device, while the hook documents they serve are synced.
- `CLAUDE.md` above the folder being imported is not imported.

## Other tools: the import engine

Every tool other than Claude Code goes through one engine: a reader per tool, one scan → choose → run flow, one
screen. This part is for whoever adds the next tool: read it before writing a reader.

### Shape

```
packages/client/importer/          the engine and the Claude Code imports
  source.go                        Source, SourceInfo, Env, Found, Kind, FolderPattern, MCPServer
  engine.go                        ScanSource / RunSource: targets, statuses, folder walk, writing, upload
  engine_mcp.go                    MCP servers: secrets, names, MCPTarget, ParseMCPServers
  importer.go, scan.go, project.go the Claude Code user-level and folder imports (their own screens and RPCs)
packages/client/importer/sources/  the readers, one file per tool, each registering itself in init()
  registry.go                      register, All, Get; the `reader` struct most readers use
  helpers.go                       JSONC, MCP files, rules with `paths:` frontmatter, instructions
  folder.go                        Any folder: every reader's FolderPatterns in one walk
  testdata/                        fixture homes and projects; tests never read the real home
packages/client/mcp/import.go      ImportTarget: the engine's MCPTarget over mcp.Inventory (servers of this device)
packages/client/server/import_sources.go  ListImportSources, ScanImportSource, RunImportSource
proto/harness/agent/v1/import_sources.proto
packages/ui/app/lib/import/sources.ts, sourcesMock.ts; components/settings/SettingsImportSource.vue
```

The UI renders what the client decides. It never knows a tool's paths.

### The Source interface

```go
type Source interface {
	Info() SourceInfo                                     // ID, Name, Description, Logo; User / Folder levels
	Detect(env Env) bool                                  // cheap: does the tool have user-level files here?
	ScanUser(ctx context.Context, env Env) ([]Found, error)
	FolderPatterns() []FolderPattern                      // project files, reused by Any folder
}

type FolderPattern struct {
	Glob string                            // ".cursor/rules/*.mdc", relative to any directory of the project
	Read func(f FolderFile) []Found
}
```

A reader only **reads and parses**. It never writes, uploads, decides whether something exists, or decides what is a
secret. It never calls `os.UserHomeDir`, `os.Getenv` or `runtime.GOOS`: everything comes from `Env`
(`Env.Home`, `Env.GOOS`, `Env.UserConfigDir()`, `Env.Display(path)` for "~/…" sources).

`Found` carries `Kind`, `Name` (the key within the kind), `Source` (file shown to the user), `Text` (markdown),
`Files` (a skill's other files), `MCP`, `Settings`, `Dir` (folder level: the project directory it applies to). A reader
may set `Status` to `StatusSkipped`, `StatusNoted` (set up by hand) or `StatusError` with a `Note`; otherwise it leaves
`Status` empty and the engine works it out.

### Kinds and where they go

| Kind | Folder level (written into the project) | User level |
|---|---|---|
| `instructions` | marked block in `<dir>/AGENTS.md` (the Claude import's `mergeBlock`) | RULE document `<source>/<name>.md` |
| `rule` | `<dir>/.harness/rules/<name>.md` (use `paths:` frontmatter for globs) | RULE document `<source>/<name>.md` |
| `command` | `<dir>/.harness/commands/<name>.md` (`a:b` → `a/b.md`) | COMMAND document `<name>` |
| `agent` | `<dir>/.harness/agents/<name>.md` | AGENT document `<name>` |
| `skill` | `<dir>/.harness/skills/<name>/` (SKILL.md + Files) | SKILL document `<name>` with files |
| `mcp` | merged into the root's `.mcp.json` (trust + device approval apply) | server of **this device** (`mcp.ImportTarget`) |
| `settings` | merged into `<dir>/.harness/settings.json`; hooks / allow rules wait for trust | listed for manual setup |
| `session` | listed for manual setup | converted (`Found.Session`) into `<projects>/<slug(cwd)>/<id>.jsonl` and uploaded like any session; without a converter, listed for manual setup |

Documents carry source label `import:<source id>`. AGENTS.md is harness's own format and is never imported. Item names
used as paths are checked (`safeName`): no `..`, no absolute paths; targets reached through symbolic links are refused.
Folder imports go to the project root (`ProjectRoot`), never the home folder, and walk with the project import's rules
(skip hidden dirs, `node_modules`, `vendor`, `dist`, `build`, `target`, nested repositories; depth 8, 50 000 entries).

Statuses: `StatusPlanned` (new), `StatusExists` (already there; comes again only with force), `StatusSkipped`,
`StatusNoted` (manual), `StatusError`; after a run `StatusUploaded` (documents, MCP servers) or `StatusCopied`
(project files). Over the wire they are `ClaudeImportStatus`.

### Secrets

- MCP env, header, argument (`--token=…`, a value after `--api-key`, `sk-…`-like values) and URL query values that look
  like secrets (`importer.isSecret`, shared with the Claude Code settings import) become `${secret:NAME}` references.
  The value is stored only when the user switches **Include secrets** on for that run; otherwise the reference exists
  with no value, and the note names it ("secret values left empty: …").
- A tool's own variables (`${VAR}`, `${env:VAR}`, `${input:id}`, a whole-value `$VAR`) become secret references with no
  value; `${userHome}` becomes the home path.
- User-level servers live on this device and their secrets in this device's MCP store, never sent to the server.
- In a folder's `.mcp.json` secret values are left empty unless Include secrets; variables stay as `${NAME}`.
- Notes and summaries never contain secret values; a URL is shown without its query.
- A server whose name exists (any manager) is `exists`; with force an editable one is replaced (`Inventory.Update`).

### Adding a reader: worked example

Say "Acme Code" keeps global rules in `~/.acme/rules/*.md` and MCP servers in `~/.acme/mcp.json`, and project rules in
`.acme/rules/*.md` with a `globs:` field.

1. Find the official documentation for every path and field, and put the URLs in a comment above the paths.
2. Create `packages/client/importer/sources/acme.go`:

```go
package sources

// Acme Code.
//
// Rules: ~/.acme/rules/*.md and .acme/rules/*.md, frontmatter globs (https://acme.example/docs/rules).
// MCP: ~/.acme/mcp.json, "mcpServers" (https://acme.example/docs/mcp).

func acmeRule(f importer.FolderFile) importer.Found {
	md := string(f.Data)
	return rule(ruleName(f.Rel, ".acme/rules/", ".md"), md, globList(md, "globs"), "")
}

func init() {
	register(&reader{
		info: importer.SourceInfo{ID: "acme", Name: "Acme Code", Logo: "acme",
			Description: "Rules and MCP servers from Acme Code."},
		detect: func(env importer.Env) bool { return exists(filepath.Join(env.Home, ".acme")) },
		user: func(env importer.Env) []importer.Found {
			var out []importer.Found
			dir := filepath.Join(env.Home, ".acme", "rules")
			entries, _ := os.ReadDir(dir)
			for _, e := range entries {
				p := filepath.Join(dir, e.Name())
				data, err := os.ReadFile(p)
				if err != nil || !strings.HasSuffix(e.Name(), ".md") {
					continue
				}
				f := rule(strings.TrimSuffix(e.Name(), ".md"), string(data), globList(string(data), "globs"), "")
				f.Source = env.Display(p)
				out = append(out, f)
			}
			if data, err := os.ReadFile(filepath.Join(env.Home, ".acme", "mcp.json")); err == nil {
				out = append(out, mcpFile(data, "mcpServers", "~/.acme/mcp.json", false)...)
			}
			return out
		},
		patterns: []importer.FolderPattern{{Glob: ".acme/rules/*.md", Read: one(acmeRule)}},
	})
}
```

3. Add fixtures under `sources/testdata/` (a fake home, a project) and a test that scans them with
   `importer.ScanSource`, runs with `importer.RunSource` against a temp copy and a recording `MCPTarget`, and checks the
   targets, statuses and that secrets stay out without Include secrets.
4. Add the logo key to `packages/ui/app/utils/providerLogos.ts` if there is a logo; add the source to
   `packages/ui/app/lib/import/sourcesMock.ts` so the browser mock matches.
5. The folder pattern now also appears in Any folder; nothing else to do there.

A reader with a different shape (sessions, a SQLite store) implements `importer.Source` directly instead of `reader`.

### Checklist for reader authors

- [ ] Every path and field comes from the tool's official docs; the URL is in a comment above the paths.
- [ ] Only parsing: no writes, no uploads, no secret decisions, no `os.UserHomeDir` / `os.Getenv`.
- [ ] Missing files yield nothing; malformed ones yield a `StatusError` item with a note, never a failed scan.
- [ ] JSON with comments goes through `stripJSONC`; MCP files through `mcpFile` / `importer.ParseMCPServers`, setting
      `Transport` when the tool means something else by a bare `url` (Gemini: SSE).
- [ ] Rules that the tool applied on request or by the model's judgment say so in the note (`rule(..., onDemand)`).
- [ ] Names are stable keys (relative path without extension; `a:b` for namespaced commands).
- [ ] No tool's prompts or proprietary text are copied: only file formats are parsed.
- [ ] Fixture tests under `testdata`; no test reads the real home or runs an import against it.
- [ ] `gofmt`, `go vet`, `go test ./importer/...`.

### User-level readers

User-level helpers live in `importer/sources/user.go` (`readUser`, `userInstructions`, `userMCP`, `userMarkdown`,
`userSkills`, `frontmatter`): every file is bounded in size and read without following links. `~/.agents/skills` is
read by the Codex reader only, although Cursor, Gemini CLI and Devin read it too, so it is not imported three times.

| Tool | Read from | Becomes |
| --- | --- | --- |
| Codex CLI | `$CODEX_HOME` (`~/.codex`): `AGENTS.override.md` else `AGENTS.md`; `config.toml` `[mcp_servers.*]` (`env_vars`, `bearer_token_env_var`, `env_http_headers` become `${VAR}`); `prompts/*.md` (top level); `agents/*.toml`; `~/.agents/skills` | instructions, MCP, commands (`$$` → `$`, named `$UPPER` → `arguments:`), agents (`developer_instructions` is the body), skills. `model`, `approval_policy`, `sandbox_mode`, `model_reasoning_effort` and `<name>.config.toml` profiles are listed to set by hand. Sessions: each rollout in `sessions/` and `archived_sessions/` (see below). `auth.json` is never read |
| Gemini CLI | `~/.gemini`: `settings.json` (`context.fileName`, `mcpServers`), the context files, `commands/**/*.toml`, `agents/*.md`, `skills/`, `extensions/*/gemini-extension.json` | instructions, MCP (bare `url` is SSE), commands (`git/commit.toml` → `git:commit`; `{{args}}` → `$ARGUMENTS`, `!{cmd}` → `` !`cmd` ``, `@{path}` kept with a note), agents, skills. Extensions: MCP with `${extensionPath}` expanded, context file, commands (prefixed `<ext>.` on a clash), `settings[].envVar` listed |
| Cursor | `~/.cursor/mcp.json`, `~/.cursor/skills/` | MCP, skills. User Rules live in Cursor's settings: listed to copy by hand |
| GitHub Copilot | `$COPILOT_HOME` (`~/.copilot`): `copilot-instructions.md`, `instructions/**/*.instructions.md`, `agents/*.agent.md`, `skills/`, `mcp-config.json`. VS Code user data (`Code[ - Insiders]/User` and each `profiles/<id>`): `prompts/*.prompt.md`, `*.instructions.md`, `*.agent.md`, `*.chatmode.md`, `mcp.json` (`servers`) | instructions, rules (`applyTo` → `paths`), agents (tools and model left out), skills, commands (one `${input:x}` → `$ARGUMENTS`, several → named arguments; `${file}` and the like kept with a note), MCP (`${input:id}` are secret references) |
| Windsurf / Devin Desktop | `~/.devin/global_rules.md`, else `~/.windsurf/global_rules.md`, else `~/.codeium/windsurf/memories/global_rules.md`; `~/.devin/rules`, `~/.windsurf/rules`; `~/.config/devin/skills`, `~/.codeium/windsurf/skills`; `~/.config/devin/mcp_config.json`, else `~/.codeium/windsurf/mcp_config.json`; `~/.codeium/windsurf/global_workflows/*.md` | instructions, rules (trigger as in a folder), skills, MCP (`serverUrl` is HTTP), commands (workflows) |

| Cline | `~/Documents/Cline/Rules`, `~/.cline/rules`, `~/Cline/Rules`; `~/Documents/Cline/Workflows`, `~/.cline/data/workflows`; `cline_mcp_settings.json` in `~/.cline/data/settings` and in the extension's globalStorage (`saoudrizwan.claude-dev`). Folder: `.clinerules/workflows/*.md` too | rules (`paths:`), commands, MCP (a bare `url` is SSE in Cline); a server named twice is imported once, from the first file |
| Roo Code | `~/.roo/rules/`, `~/.roo/rules-{slug}/` (recursive, `.md`/`.txt`); globalStorage `rooveterinaryinc.roo-cline/settings/custom_modes.yaml` (or `.json`) and `mcp_settings.json`. Folder: `.roomodes`, `.roo/mcp.json` too | rules (a mode's say so), agents from modes (`name: <slug>`, `description` from whenToUse, `read_only` when the groups have neither edit nor command, body roleDefinition + customInstructions, groups in the note), MCP |
| Kilo Code | `~/.config/kilo/kilo.json(c)` and `agent/`, `agents/`; legacy `~/.kilocode/rules*`, globalStorage `kilocode.kilo-code/settings`. Folder: `kilo.json(c)`, `.kilo/kilo.json(c)`, `.kilo/agents`, `.kilo/agent`, `.kilocode/agents`, `.kilocodemodes` | as OpenCode for the config and agents, as Roo for the legacy files |
| OpenCode | `~/.config/opencode/opencode.json(c)`, `AGENTS.md`, `agents/` (`agent/`), `commands/` (`command/`). Folder: `opencode.json(c)`, `.opencode/opencode.json(c)`, `.opencode/agents`, `.opencode/commands` | MCP (V1 and V2 `mcp`; `{env:X}` → `${X}`, `{file:…}` left out with a note), agents (a config agent without a prompt only changes a built-in one: skipped; nested files joined with `-`), commands (nested folders give `a:b`), instructions (`~/` and absolute paths and globs read; project paths and URLs listed) |
| Kiro | `~/.kiro/steering/*.md`, `~/.kiro/settings/mcp.json`, `~/.kiro/hooks`. Folder: `.kiro/settings/mcp.json`, `.kiro/hooks`, `.kiro/specs/*/*.md` too | rules (inclusion as in a folder), MCP, hooks listed to set up by hand (events and tool names differ), specs listed with a note to reference them from AGENTS.md |
| Continue | `~/.continue/config.yaml`, else the legacy `config.json`. Folder: `.continue/prompts/*.md`, `.continue/mcpServers/*.yaml\|json` too | rules (strings and objects; `regex` noted), prompts → commands, MCP (`${{ secrets.X }}` → `${X}`, `requestOptions.headers`); `uses:` hub blocks and models/context/docs/data listed to set up by hand. Legacy: `systemMessage` → instructions, `customCommands`, `experimental.modelContextProtocolServers` |
| Zed | `~/.config/zed` (`%APPDATA%\Zed` on Windows): `AGENTS.md`, `settings.json` `context_servers`. Folder: `.zed/settings.json` too | instructions, MCP (`{command, args, env}`, `{url, headers}`, the older `{source: custom, command: {path, …}}`); servers an extension provides are listed to add by hand |
| Junie | `~/.junie/AGENTS.md`, `~/.junie/mcp/mcp.json`. Folder: `.junie/mcp/mcp.json` too | instructions, MCP |

Sources: the URLs in each reader's header comment (checked 2026-11-27).

### Sessions of other tools

A reader converts a session into a `SessionSource` (`importer/transcript.go`): the conversation only, the user's prompts
and the model's answers, with one `*Used <tool>: <command>*` line per tool call. Tool output, reasoning and token counts
stay behind. The harness id is a UUIDv5 of `<tool>:<its id>`, and the transcript is the same bytes on every run, so the
status is a comparison with the copy already there: the same bytes are `exists` (or planned again while the upload is
unfinished), a copy that starts with them was continued here and is skipped, another one changed since and is replaced
only with force. A session open here or known from another machine (`HoldSession`) is skipped. After writing, the file
goes to the upload queue and is flushed at once. In the app, sessions start unchecked.

Codex (`sources/codex_sessions.go`): rollouts are `{timestamp, type, payload}` lines (`session_meta`, `turn_context` for
the model, `response_item`), or, before about August 2025, a bare meta object and bare response items. Messages Codex
adds in the user's name (`<environment_context>`, `<user_instructions>`, `# AGENTS.md instructions`…) are dropped;
`["bash", "-lc", X]` shows as X. A subagent's thread and a rollout with no folder recorded are skipped.

Gemini CLI (`sources/gemini_sessions.go`): `~/.gemini/tmp/<project>/chats/session-*.json` (one document) or `.jsonl`
(the latest line of a message id wins, `$rewindTo` drops a message and what follows, `$set` updates the metadata). The
folder is `tmp/<project>/.project_root`, else `projects.json`. Thoughts and the CLI's own `info` messages stay behind.

### Not done yet

- Codex's `.jsonl.zst` rollouts are counted, not read. Cursor, Copilot and the others keep chats in app databases: not read.
- User-level settings mapping (listed as manual).
- When the registry is unreachable, MCP names are checked against this device's servers only.
