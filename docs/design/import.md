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
| `session` | listed for manual setup (not imported yet) | listed for manual setup |

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

### Not done yet

- User-level readers for Codex CLI, Gemini CLI, Cursor, Copilot, Windsurf, Cline, Roo/Kilo, Kiro, OpenCode, Continue,
  Zed and Junie (their folder patterns are in place, except Codex and OpenCode, whose project instructions are AGENTS.md).
- Sessions from other tools (`KindSession` is listed as manual).
- User-level settings mapping (listed as manual).
- When the registry is unreachable, MCP names are checked against this device's servers only.
