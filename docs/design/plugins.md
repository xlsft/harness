# Plugins

A plugin bundles skills, custom commands, agents, output styles, hooks and MCP servers in one folder. xlsft\`s harness reads the format existing tools use, so a plugin written for them installs unchanged. The code is in `packages/client/plugins`; `packages/client/bootstrap/plugins.go` joins what a plugin holds to a session.

## The format

A plugin is a folder. A manifest is optional and sits at `.harness-plugin/plugin.json`, or at `.claude-plugin/plugin.json` in a package made for other agents; the first wins when both exist. Without one the name comes from the marketplace entry or the folder.

| Component | Default place | Manifest field | Field and default |
|---|---|---|---|
| Skills | `skills/<name>/SKILL.md`; a `SKILL.md` at the root when there is nothing else | `skills` | adds |
| Commands | `commands/**/*.md` | `commands`: paths, or a map of name to `source` or `content` | replaces |
| Agents | `agents/**/*.md` | `agents` | replaces |
| Output styles | `output-styles/*.md` | `outputStyles` | replaces |
| Hooks | `hooks/hooks.json`, with a top-level `hooks` key | `hooks`: paths or inline event maps | merges |
| MCP servers | `.mcp.json` | `mcpServers`: paths or inline maps | merges, a later name replacing an earlier one |

Paths in a manifest start with `./` and stay inside the folder, links included; what doesn't is a problem shown with the plugin, and the rest still loads.

Everything is named below the manifest's name: the skill `fmt` of plugin `kit` is `/kit:fmt`, its agent type `kit:helper`, its style `kit:terse`, its server `plugin:kit:srv` with tools `mcp__plugin_kit_srv__<tool>`. Bodies of skills, commands and agents can name `${CLAUDE_PLUGIN_ROOT}` and `${CLAUDE_PLUGIN_DATA}` (or `HARNESS_` for `CLAUDE_`); so can MCP commands, arguments, environment, URLs and headers. Hook commands read them from their environment, so a shell keeps a path with spaces whole: `"${CLAUDE_PLUGIN_ROOT}"/format.sh`.

These are read and reported, not acted on: `lspServers` and `.lsp.json`, `userConfig`, `dependencies`, `channels`, `workflows`, themes, monitors, `settings`, `bin/`, MCP bundles. Agents of a plugin take no hooks of their own.

## Marketplaces

A marketplace is a repository or folder with `.harness-plugin/marketplace.json` or `.claude-plugin/marketplace.json`, or a URL of such a file. An entry has a `name`, a `source` and any manifest field.

| Source of an entry | Fields |
|---|---|
| A path in the marketplace | `"./plugins/kit"`, or a bare name under `metadata.pluginRoot` |
| `github` | `repo`, `ref`, `sha` |
| `url` | `url` of a git repository, `ref`, `sha` |
| `git-subdir` | `url`, `path`, `ref`, `sha` |

A marketplace added by the URL of its file has no folders, so its entries need one of the repository sources. Without a `plugin.json`, the entry is the manifest. With one, the entry's components are added to it, unless `strict` is false, which makes that a conflict. Fetching is over https, ssh, git and file; plain http only to this machine. `npm`, `archive` and `command` sources are refused.

## Where things are, and what stays here

All of it is in `HARNESS_HOME/plugins`, a part of this device:

```
state.json                              what is installed, its switches, approvals and pins
policy.json                             the last policy seen, for an offline start
marketplaces/<name>/                    the catalog as last fetched
cache/<marketplace>/<plugin>/<version>-<hash>/    the plugin, as installed
data/<id>/                              what the plugin keeps; outlives updates
```

Nothing of this goes to the xlsft\`s harness server. `enabledPlugins` in a synced settings document is not read: only a policy document and a project's own files say anything about a plugin. Commands that change plugins are refused from another machine, through the relay or otherwise, both for `PluginService` and `/plugin`.

## What keeps a plugin from running

A plugin runs in a session only if all of these hold, checked each time it would load:

1. **Approved on this device, as it is now.** The approval is a hash of the plugin's files (paths, the executable bit, bytes, link targets). `PluginService` approves only the hash the frontend showed. A new version, or files changed under it, needs a new approval; a plugin whose files stopped matching what was installed can't be approved until it is installed again. The server can't grant this, and the organization's distributing a plugin doesn't either.
2. **In a trusted folder.** In a folder this machine doesn't trust, nothing of a plugin loads. Settings > Hooks lists its hooks with that reason.
3. **Enabled.** The user's switch, unless a trusted project's settings say otherwise for that folder. `enabledPlugins` there can turn an approved plugin on or off, and ask for one that isn't here, which then shows as not installed; it never approves anything or installs by itself.
4. **Allowed by policy.** See below. A plugin already installed stops loading when policy stops allowing it.

`HARNESS_NO_PLUGINS=1` turns them all off for a client. `allowManagedHooksOnly` stops plugins' hooks; `disableAllHooks` does too. Hooks and MCP servers run with the user's rights and outside the sandbox, like those of settings files, which is why the approval screen lists each command line. MCP servers of an approved plugin ask for no approval of their own.

Skills, commands and styles follow a change at once. Hooks, MCP servers and agent types are read when a session opens or moves, like those of settings files: revoking doesn't stop what a running session started. Keeping the previous version on disk lets such a session finish.

`harness-client plugin approve` needs a terminal. The model's shell commands have none, and it must not approve what it wants to run. The state files are under `HARNESS_HOME`, a path the permission engine and the sandbox keep the model from writing to.

## Versions

Installing pins a plugin to its commit and hash, and a session loads exactly that folder. Nothing moves it except an update someone asked for. A `version` in the manifest holds it as the format says: an update doesn't move it until the version changes, whatever else changed upstream. A `sha` in a marketplace entry is checked out and verified. `pin` keeps an update from looking at all. There is no automatic update.

The version shown is the manifest's, then the entry's, then the commit.

## Administrators

The managed policy (`harness.auth.v1.PolicyService`, Settings > Managed policy) writes these keys into the org POLICY document, and clients enforce them:

| Key | Form | Effect |
|---|---|---|
| `strictKnownMarketplaces` | sources | Plugins and marketplaces come only from these. `[]` allows none. Each document's list applies. |
| `blockedMarketplaces` | sources | Checked first. |
| `extraKnownMarketplaces` | name to `{source}` | Registered on every device, and can't be removed there. |
| `enabledPlugins` | `name@marketplace` to bool | `true`: fetched on every device and kept on, still waiting for that device's approval. `false`: blocked at every level. |
| `allowedMcpServers` | names | Also covers plugins' servers, by `plugin:<plugin>:<server>` or the name inside the plugin. |

A source is `{"source":"github","repo":"owner/repo"}` (or `owner/*`), `{"source":"git","url":...}`, `{"source":"url","url":...}`, `{"source":"file"|"directory","path":...}`, `{"source":"hostPattern","hostPattern":...}` or `{"source":"pathPattern","pathPattern":...}`. A `ref` or `path` must match too. Spellings of one repository match each other. A direct install from a folder or repository is checked as if it were a marketplace. Entries inside an allowed marketplace may point anywhere; to stop one, block it by id.

The form has one line per source: `owner/repo`, `owner/*`, a URL, an absolute path, `host:<regexp>`, `path:<regexp>`.

## Commands

`/plugin ...` in a session, and `harness-client plugin ...` in a shell, take the same words: `list`, `info`, `install`, `approve`, `revoke`, `enable`, `disable`, `update`, `pin`, `unpin`, `remove`, `marketplace add|update|remove|list`, `new` and `validate`. `new` writes a plugin's starting files. Settings > Plugins does the same over `PluginService`. `/plugin` alone also opens that screen.

## Hooks in frontmatter

A `hooks` block in a skill's or agent definition's frontmatter has the shape of the settings key.

- A skill's run from when it starts until the turn ends, or for a forked skill until its agent ends.
- An agent definition's see only that agent's events, and its `Stop` fires as `SubagentStop`. They last until the agent first stops, and don't come back if it is resumed.
- `once: true` runs a handler a single time.

A project file is already behind folder trust and a plugin behind its approval. A skill stored on the server needs this device's trust of that content, like hooks the server delivers; until then it runs without them and says so. `allowManagedHooksOnly` stops all of them.
