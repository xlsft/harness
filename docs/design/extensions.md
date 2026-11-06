# Extensions

The Extensions screen (`/extensions`) is where skills, connectors (MCP servers) and plugins are managed: **Yours** is what exists, **Discover** what could be added. Nothing of user-level management for these three stays in Settings. The client decides and joins (`packages/client/extensions`, `ExtensionsService` in `proto/harness/agent/v1/extensions.proto`); the frontend renders and sends intent. Plugin and server changes still go through `PluginService` and the MCP calls of `AgentService`, with their approvals.

## What moved

- **Settings > MCP and Settings > Plugins are gone.** Add, edit, tool rules, sign-in and approvals are in the Add menu and the detail view of Extensions. The manager of a server (device, account, admin, repository) is a badge and a filter, not a section. The repository's folder comes from the page's project row.
- **The MCP audit log** moved under Settings > Audit log, for admins. **Managed policy** stays in Settings, and gained skill limits.
- `/skills` opens Extensions, and `/plugin` its Plugins tab. There is no `/customize`.
- The wire enum `SlashCommandResult.ACTION_OPEN_CUSTOMIZE` keeps its name and number: users never see it, and `buf breaking` guards it.
- Nothing is read from `.claude` here, and no such location is shown; "Import from Claude Code" in the Add menu opens Settings > Import. New skills go to `~/.harness/skills` or `<folder>/.harness/skills`.

## Where Discover's data comes from

Nothing is invented: no install counts, publishers or marks of our own, and no personalised sections. A number or a badge appears only where the source gives it, and an empty section isn't sent.

| Source | Kinds | How | Offline |
|---|---|---|---|
| Built-in marketplace | skills, plugins | `packages/client/extensions/builtin`, embedded: a copy of the repository's `catalog/` (`scripts/sync-catalog.sh`, a test checks it). Registered like any marketplace, under the same policy and approvals, and can't be removed. It is the featured content. Most entries are references pinned by commit, so their files aren't on this device: an entry whose folder sits under `skills/` is listed as one skill, in its own words, and a bundle is listed under Plugins only. The catalog names no categories, so it adds none. | Yes |
| Marketplaces the user and admin configured | skills, plugins | `plugins.Manager.Browse`, read from the copies on disk. A plugin with a repository of its own isn't on this device, so what it holds is known only after installing. | Yes |
| Default catalog | connectors | `packages/client/extensions/catalog.json`, in our own words. | Yes |
| Public MCP registry | connectors | Fetched by the client, `registry.modelcontextprotocol.io/v0.1/servers`, first page cached in `HARNESS_HOME/extensions/mcp-registry.json` for 6 hours; searches remembered 10 minutes; a minute's backoff after a failure. `HARNESS_MCP_REGISTRY_URL` points it elsewhere, `off` stops it. A managed policy allowing no server stops it too. | The saved list, saying so |
| skills.sh | skills | On the xlsft\`s harness server (`packages/core/src/skills/skillssh.ts`, `SkillSearchService`), cached there, so clients never see the token. | Hidden or noted; the rest stands |

The registry's only word on a publisher is the namespace of a name, so that is what's shown. Registry entries with a blank in the URL, or required arguments, aren't offered as one click: they say to add them by hand.

### The skills.sh token

Checked once by hand, without credentials, on 2026-11-06: `/api/v1/skills`, `/skills/search` and `/skills/curated` all answer `401 authentication_required`, asking for a Vercel OIDC token. So a token is really needed, whatever the terms say about a public API limited per IP.

It is an optional server setting: `SKILLS_SH_TOKEN`, or `VERCEL_OIDC_TOKEN` on a Vercel deployment; `SKILLS_SH_URL` overrides the host. Without one, the source is hidden and nobody is asked. **Limit to decide on:** these tokens are minted by Vercel and rotate about every 12 hours, so outside a Vercel deployment a static value expires, and search then reports it can't be reached until someone replaces it. Real calls with a token have not been made; everything beyond that one check ran on fakes.

## Installing

- **Plugins and marketplace skills:** a skill has no install of its own, so adding one installs the plugin that holds it. Nothing runs until this device approves exactly that content.
- **Connectors:** the card opens what would run; "Set up" fills in the server form, and adding it on this device is the existing approval.
- **Skills from search:** `InstallExternalSkill` takes the hash that was shown and refuses other content, then stores under `~/.harness/skills/<name>` with the record in `HARNESS_HOME/skills-external.json`, outside the skills folder, so nothing a download holds can speak for itself. Such a folder stays out of the catalog until `SetExternalSkillApproval` approves the hash of its files, and again whenever they stop matching. The approval view shows the audits word for word; no audit is said not to be a pass. The source's hash algorithm isn't documented, so the pin is the source's hash at install plus our own hash of the files, which is what is compared. Paths that leave the folder are refused, files are written without the executable bit, and a personal skill of the same name isn't replaced.

## Policy

`allowedSkills`, `blockedSkills` (names, `kit:*` for all of a plugin) and `disableExternalSkillSearch` in the org POLICY document, set in Settings > Managed policy. Clients enforce them: a refused skill stays listed, can't run, and no switch changes that. The server also refuses search. All are shown as set by the administrator. Built-in commands aren't skills.

## Connectors table

`extensions.FoldConnectors`: each managed server keeps its row, since each can be changed on its own. A plugin's server joins the row of the same endpoint, and plugins bringing one endpoint share a row, the status closest to working speaking for it. A command naming its plugin's own folder is never merged. Listing connects to nothing: "connected" means signed in, the rest say "connects in sessions" until the detail inspects the server.

## Trying it

The mock transport shows the whole screen: `pnpm exec nuxt dev` in `packages/ui`, then `/extensions`. `?scenario=first-run|offline|restricted` shows those states.
