# Handoff: open agent work (2026-11-27)

Four agents were stopped on 2026-11-27. Each left a note in this folder, named after its branch. This page says what is
in `main`, what is on each branch, and how to continue. One task per agent, each in its own worktree.

| Task | Branch | State | Note |
|---|---|---|---|
| Knowledge sources apart from issue trackers | `worktree-agent-a58b2378f3a1713cd` | **Merged into `main`** (408fd18b), all checks green | [note](worktree-agent-a58b2378f3a1713cd.md) |
| Custom roles; Roles, Groups and Quotas in Settings | `worktree-agent-a2090aa2f79eddcf2` | Server side WIP, 2 of 14 role tests fail, no UI | [note](worktree-agent-a2090aa2f79eddcf2.md) |
| Import readers, first wave (Codex, Gemini, Cursor, Copilot, Windsurf) | `worktree-agent-a9355fb33803dac6b` | Research only, no code | [note](worktree-agent-a9355fb33803dac6b.md) |
| Import readers, second wave (Cline, Roo, Kilo, Kiro, OpenCode, Continue, Zed, Junie) | `worktree-agent-a75fefe6129441adc` | Research only, no code | [note](worktree-agent-a75fefe6129441adc.md) |

Logos for the import sources are already in `main` (`packages/ui/app/utils/providerLogos.ts`: cursor, cline, roo, kilo,
kiro, junie, windsurf, codex, opencode, zed). Continue and Aider have no logo in a free set yet and show a monogram.

## Rules for every agent

- In prose the product is always "xlsft\`s harness"; bare `harness` only in commands, paths and identifiers. Code,
  comments and docs in English.
- Commit small increments as you go and push your branch after each commit (`git push -u origin HEAD`). Messages end with
  `Co-Authored-By: <model> <noreply@anthropic.com>`. Never push to or merge into `main`, never rebase, force-push or
  rewrite history. Update with `git fetch origin && git merge origin/main`, before you start and before you finish.
- Logic lives in the client (Go) and the server (core), not the UI. UI text goes through `tt()`, then
  `node scripts/i18n-extract.mjs --apply` in `packages/ui`. Settings sections use the list style of Settings > VCS.
- Nothing is read from `.claude/` or `CLAUDE.md` at runtime. Device-local data never syncs. Never read the real config
  of any tool on the machine and never import from the real home: tests use a temp home and fixtures.
- Secrets: never log or print them, never put them in URLs. Import turns secret-looking values into `${secret:NAME}`.
- Use unique scratch prefixes. Never run `go clean -cache`. Codegen: `buf format -w proto && buf generate` from the
  repo root. If `node_modules` is missing in a worktree, symlink `packages/{ui,core}/node_modules` to the main
  checkout's; never commit the links.
- Checks: gofmt, go vet, go test (server package with `-timeout 40m`); core `npx tsc --noEmit -p .` and
  `vitest run --maxWorkers=2 --hookTimeout=200000 --testTimeout=100000`; UI eslint on changed files,
  `nuxt typecheck`, `vitest run`. Never run tests and `git push` in one shell command.
- Write down decisions in `docs/design/<topic>.md` and keep the branch continuable at every commit.

## 1. Custom roles (continue `worktree-agent-a2090aa2f79eddcf2`)

Check out the branch and read its note first. In order:

1. `git merge origin/main`. Main now has migration `0018_knowledge_connections`, so renumber this branch's
   `0018_custom_roles` to `0019` (SQL, journal, snapshot) and re-run `node scripts/embed-migrations.ts`.
2. Fix the two failing tests in `packages/core/test/roles.test.ts`: the `policy.write` probe (the local policy service
   probably also needs `policy.read`), and the taken-name case (AlreadyExists in the test vs Aborted from the server).
3. Run the full core suite and fix what the move from role checks to permission checks broke.
4. Move the three files still checking `role === "admin"` to permissions: `src/tracker/connections.ts`,
   `src/services/knowledge.ts` and `src/rpc/boards.ts` (the knowledge one now lives next to the new knowledge
   connections code from main).
5. Go client: `packages/client/auth/connect.go` prefers `role_id` / `role_name`.
6. UI: Roles, Groups and Quotas as three Settings sections in the list style, registered in `SettingsPage.vue`; a role
   editor with the permission catalog; role select and group chips in Settings > Users; custom roles in role mappings
   and the MFA policy.
7. Docs: `docs/design/roles.md` and the admin parts of `deploy/README.md`.

Done means: an admin creates a role with chosen permissions, gives it to a user, and the user can do exactly that; the
built-ins behave as before; all checks green.

## 2. Import readers, first wave (continue `worktree-agent-a9355fb33803dac6b`)

Read `docs/design/import.md` ("Other tools: the import engine") and the branch note, which has verified doc URLs and
format details. Then:

1. Add the dependencies the note proposes: `github.com/BurntSushi/toml` (Codex config, Gemini commands) and
   `github.com/klauspost/compress` (zstd, compressed Codex sessions).
2. Readers in `packages/client/importer/sources/<tool>.go`, one file each, registered in `init()`: Codex CLI, Gemini CLI,
   Cursor, GitHub Copilot (both `~/.copilot` and VS Code profile data), Windsurf (still to research).
3. Codex sessions: the engine first needs session support (the note has the design: session fields on found items and
   engine options, wiring in `server/import_sources.go`). Status goes by file time, not size. Session items start
   unchecked in the UI.
4. Fixtures and tests in their own folders, entries in `packages/ui/app/lib/import/sourcesMock.ts`, the per-tool table in
   `docs/design/import.md`.

## 3. Import readers, second wave (continue `worktree-agent-a75fefe6129441adc`)

Same engine and rules as section 2; the branch note has the research. It changes the original plan:

- Kilo Code now uses an OpenCode-style config (`~/.config/kilo/kilo.jsonc`), so Kilo and OpenCode share one parser; the
  old Kilo files are legacy and are read too.
- OpenCode reads both old and new key and folder names (`mcp` vs `mcp.servers`, `agent/` vs `agents/`, `prompt` vs
  `system`).
- Cline: an MCP entry with a `url` and no `type` is SSE.
- Zed: `~/.config/zed/AGENTS.md` is the user-level instructions; the old Rules Library is gone.
- Junie: `~/.junie/AGENTS.md` and `~/.junie/mcp/mcp.json`, plus the project `.junie/mcp/mcp.json`.
- Kiro: hooks and specs are listed as set up by hand.
- Continue: global rule folders are not read (undocumented).

One reader file per tool (move Kiro, Continue, Junie and Zed out of `others.go`), own fixtures (`testdata/w2home`,
`testdata/w2project`) and an own test file so the first wave's exact counts don't break.

## 4. Knowledge and tracker providers (new, on `main`)

Knowledge sources are merged. Follow section 10 of `docs/design/tracker.md` ("Writing a provider"). One agent per vendor:
Atlassian (Jira + Confluence), Linear (issues + docs), GitHub and GitLab (Issues + Wiki), Yandex (Tracker + Wiki), Kaiten.
Small leftovers from the knowledge work: an own address together with a borrowed sign-in in the form, and a picker of
code host connections instead of the plain `vcs_connection_id` field.
