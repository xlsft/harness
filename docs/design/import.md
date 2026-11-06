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
| `~/.claude.json` MCP servers | listed for manual setup |

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
