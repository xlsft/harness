// ExtensionsApi for the demo and tests, on top of the mock plugin and MCP stores so that installing and approving show up everywhere. The
// fake registry, marketplaces and skill search below are examples, not data: example.* names only, and nothing invented for real names.
// `?scenario=first-run|offline|restricted` in the URL shows those states.

import { create } from '@bufbuild/protobuf'
import {
  ConnectorStatus,
  DiscoverExtensionsResponseSchema,
  ExtensionKind,
  ExtensionConnectorSchema,
  ExtensionSkillSchema,
  GetExternalSkillResponseSchema,
  GetSkillDetailResponseSchema,
  InstalledExternalSkillSchema,
  ListExtensionConnectorsResponseSchema,
  ListExtensionSkillsResponseSchema,
  type DiscoverItem,
  type DiscoverSection,
  type ExtensionConnector,
  type ExtensionSkill,
  type InstalledExternalSkill
} from '~/gen/harness/agent/v1/extensions_pb'
import { McpServerSource, McpServerState, type McpServerEntry } from '~/gen/harness/agent/v1/mcp_pb'
import { PluginStatus } from '~/gen/harness/agent/v1/plugins_pb'
import { SlashCommandKind, SlashCommandSchema, type SlashCommand } from '~/gen/harness/agent/v1/slash_commands_pb'
import { AuthState, Transport } from '~/gen/harness/mcp/v1/mcp_pb'
import type { SkillAudit } from '~/gen/harness/skills/v1/search_pb'
import type { MockMcpApi } from '~/lib/mcp/mock'
import type { MockPluginsApi } from '~/lib/plugins/mock'
import type { PluginStatusVM, PluginVM } from '~/lib/plugins/types'
import type { SlashCatalog } from '~/lib/session/slashCommands'
import type { DiscoverQuery, ExtensionPluginGroupVM, ExtensionsApi } from './types'

export type MockScenario = 'default' | 'first-run' | 'offline' | 'restricted'

const OVERVIEW = 6

const PLUGIN_STATUS: Record<PluginStatusVM, PluginStatus> = {
  active: PluginStatus.ACTIVE, needs_approval: PluginStatus.NEEDS_APPROVAL, disabled: PluginStatus.DISABLED, untrusted_folder: PluginStatus.UNTRUSTED_FOLDER,
  blocked_by_policy: PluginStatus.BLOCKED_BY_POLICY, changed_on_disk: PluginStatus.CHANGED_ON_DISK, broken: PluginStatus.BROKEN, not_installed: PluginStatus.NOT_INSTALLED
}

const CONNECTOR_OF_PLUGIN: Partial<Record<PluginStatusVM, ConnectorStatus>> = {
  active: ConnectorStatus.IN_SESSIONS, needs_approval: ConnectorStatus.NEEDS_APPROVAL, disabled: ConnectorStatus.OFF, untrusted_folder: ConnectorStatus.NEEDS_TRUST, blocked_by_policy: ConnectorStatus.BLOCKED
}

const BADGE: Partial<Record<McpServerSource, string>> = { [McpServerSource.DEVICE]: 'Device', [McpServerSource.ACCOUNT]: 'Account', [McpServerSource.ORG]: 'Admin', [McpServerSource.PROJECT]: 'Repository' }

function entryStatus(e: McpServerEntry): ConnectorStatus {
  switch (e.state) {
    case McpServerState.READY: return e.server?.auth?.state === AuthState.CONNECTED ? ConnectorStatus.CONNECTED : ConnectorStatus.IN_SESSIONS
    case McpServerState.DISABLED: return ConnectorStatus.OFF
    case McpServerState.NEEDS_TRUST: return ConnectorStatus.NEEDS_TRUST
    case McpServerState.NEEDS_APPROVAL: return ConnectorStatus.NEEDS_APPROVAL
    case McpServerState.DENIED: return ConnectorStatus.BLOCKED
    case McpServerState.NEEDS_AUTH: return ConnectorStatus.NEEDS_SIGN_IN
    case McpServerState.UNAVAILABLE: return ConnectorStatus.ERROR
  }
  return ConnectorStatus.UNSPECIFIED
}

const key = (remote: boolean, endpoint: string) => (remote ? endpoint.toLowerCase().replace(/\/+$/, '') : endpoint)
const transportText = (t: Transport) => (t === Transport.SSE ? 'sse' : t === Transport.WS ? 'ws' : t === Transport.STREAMABLE_HTTP ? 'http' : 'stdio')

/** The same folding the client does (packages/client/extensions), for the demo. */
export function foldConnectors(entries: readonly McpServerEntry[], plugins: readonly PluginVM[]): ExtensionConnector[] {
  const rows: ExtensionConnector[] = []
  const byKey = new Map<string, ExtensionConnector>()
  for (const e of entries) {
    const t = e.server?.transport ?? Transport.STDIO
    const remote = t !== Transport.STDIO
    const c = create(ExtensionConnectorSchema, { id: e.server?.id || `file:${e.server?.name}`, name: e.server?.name, remote, transport: transportText(t), endpoint: e.launchSummary,
      badges: BADGE[e.source] ? [BADGE[e.source]!] : [], status: entryStatus(e), statusDetail: e.stateDetail, entry: e })
    rows.push(c)
    if (!byKey.has(key(remote, e.launchSummary))) byKey.set(key(remote, e.launchSummary), c)
  }
  rows.sort((a, b) => a.name.localeCompare(b.name))
  const fromPlugins: ExtensionConnector[] = []
  for (const p of plugins) {
    if (p.status === 'not_installed') continue
    for (const m of p.contents.mcpServers) {
      const remote = m.transport !== 'stdio'
      const own = !remote && m.command.includes('PLUGIN_ROOT')
      let c = own ? undefined : byKey.get(key(remote, m.command))
      if (!c) {
        c = create(ExtensionConnectorSchema, { id: `plugin:${p.id}:${m.name}`, name: m.name.split(':').at(-1), remote, transport: m.transport, endpoint: m.command })
        fromPlugins.push(c)
        if (!own) byKey.set(key(remote, m.command), c)
      }
      if (!c.badges.includes('Plugin')) c.badges.push('Plugin')
      c.providers.push({ $typeName: 'harness.agent.v1.ConnectorProvider', pluginId: p.id, pluginName: p.displayName || p.name, server: m.name, pluginStatus: PLUGIN_STATUS[p.status] })
      if (!c.entry) {
        const st = CONNECTOR_OF_PLUGIN[p.status] ?? ConnectorStatus.ERROR
        const rank = [ConnectorStatus.CONNECTED, ConnectorStatus.IN_SESSIONS, ConnectorStatus.NEEDS_APPROVAL, ConnectorStatus.NEEDS_TRUST, ConnectorStatus.ERROR, ConnectorStatus.OFF, ConnectorStatus.BLOCKED]
        if (c.status === ConnectorStatus.UNSPECIFIED || rank.indexOf(st) < rank.indexOf(c.status)) {
          c.status = st
          c.statusDetail = p.reason
        }
      }
    }
  }
  fromPlugins.sort((a, b) => a.name.localeCompare(b.name))
  return [...rows, ...fromPlugins]
}

function audit(provider: string, status: string, summary: string): SkillAudit {
  return { $typeName: 'harness.skills.v1.SkillAudit', provider, status, summary, auditedAt: '2026-11-01T10:00:00Z', riskLevel: status === 'pass' ? 'LOW' : 'MEDIUM' }
}

interface FoundSkill {
  id: string
  name: string
  source: string
  installs: bigint
  audits: SkillAudit[]
  description: string
  license: string
}

const FOUND: FoundSkill[] = [
  { id: 'example-labs/skills/pdf-tools', name: 'pdf-tools', source: 'example-labs/skills', installs: 18200n, audits: [audit('Example audit', 'pass', 'No risks detected')], description: 'Read and fill PDF files.', license: 'MIT' },
  { id: 'example-labs/skills/sql-review', name: 'sql-review', source: 'example-labs/skills', installs: 960n, audits: [audit('Example audit', 'warn', 'Runs a shell command')], description: 'Review SQL before it runs.', license: '' },
  { id: 'example.org/fetch-all', name: 'fetch-all', source: 'example.org', installs: 41n, audits: [audit('Example audit', 'fail', 'Sends file contents to a remote host')], description: 'Fetch everything a page links to.', license: 'Apache-2.0' },
  { id: 'example-labs/skills/unaudited', name: 'unaudited', source: 'example-labs/skills', installs: 0n, audits: [], description: 'No audit yet.', license: '' }
]

function item(init: Partial<DiscoverItem> & { id: string, kind: ExtensionKind, name: string, sourceLabel: string }): DiscoverItem {
  return { $typeName: 'harness.agent.v1.DiscoverItem', description: '', publisher: '', version: '', homepage: '', license: '', category: '', tags: [], updatedAt: '', installed: false, refusal: '', pluginId: '', installs: 0n, options: [], problem: '', externalId: '', audits: [], ...init }
}

export class MockExtensionsApi implements ExtensionsApi {
  private created: ExtensionSkill[] = []
  private external = new Map<string, InstalledExternalSkill>()

  constructor(
    private pluginsApi: MockPluginsApi,
    private mcpApi: MockMcpApi,
    private catalog: () => SlashCatalog,
    private scenario: MockScenario = 'default'
  ) {
    if (scenario === 'first-run') this.pluginsApi.clearForDemo()
    if (scenario === 'default') {
      this.external.set('sql-review', this.installedView(FOUND[1]!, false))
    }
  }

  private installedView(f: FoundSkill, approved: boolean): InstalledExternalSkill {
    const name = f.id.split('/').at(-1)!
    return create(InstalledExternalSkillSchema, { id: f.id, source: f.source, name, description: f.description, pinnedHash: `hash-of-${name}`, contentHash: `sha256:${name}`, approved, audits: f.audits, license: f.license, installs: f.installs,
      installedAt: '2026-11-04T09:00:00Z', folder: `~/.harness/skills/${name}`, files: ['SKILL.md', 'examples/query.sql'] })
  }

  private skillRow(c: Partial<Omit<SlashCommand, '$typeName' | '$unknown'>> & { name: string, description: string, source: string }, extra: Partial<Omit<ExtensionSkill, '$typeName' | '$unknown'>> = {}): ExtensionSkill {
    return create(ExtensionSkillSchema, {
      command: create(SlashCommandSchema, { kind: SlashCommandKind.SKILL, enabled: true, userInvocable: true, modelInvocable: true, switchable: true, listed: true, ...c }),
      updatedAt: '2026-10-28T09:00:00Z', ...extra
    })
  }

  async skills(cwd: string) {
    const first = this.scenario === 'first-run'
    const project = cwd ? this.catalog().commands.filter(c => c.source === 'project').map(c => create(ExtensionSkillSchema, { command: c, updatedAt: '2026-11-02T12:00:00Z', canRemove: true, folder: `${cwd}/.harness/skills` })) : []
    const restricted = this.scenario === 'restricted'
    const external = [...this.external.values()]
    const groups = first
      ? []
      : [
          { key: 'project', title: 'In this project', skills: project },
          { key: 'personal', title: 'Personal', skills: [
            this.skillRow({ name: 'tidy-imports', description: 'Sort and group imports the way this team keeps them, then run the formatter over the changed files.', source: 'personal', path: '~/.harness/skills/tidy-imports/SKILL.md', allowedTools: ['Bash(prettier:*)'] }, { canRemove: true, folder: '~/.harness/skills/tidy-imports' }),
            this.skillRow({ name: 'standup', description: 'Write a standup note from the last day of commits.', source: 'personal', path: '~/.harness/skills/standup/SKILL.md', enabled: false }, { canRemove: true, folder: '~/.harness/skills/standup', updatedAt: '2026-09-24T09:00:00Z' }),
            ...this.created
          ] },
          { key: 'external', title: 'skills.sh', skills: external.filter(e => e.approved).map(e => create(ExtensionSkillSchema, { command: create(SlashCommandSchema, { name: e.name, description: e.description, kind: SlashCommandKind.SKILL, source: 'personal', enabled: true, switchable: true, userInvocable: true, modelInvocable: true }), external: e, folder: e.folder, updatedAt: e.installedAt })) },
          { key: 'plugin:format-tools@acme', title: 'Format tools', skills: [this.skillRow({ name: 'format-tools:format', description: 'Formats what the agent writes.', source: 'plugin', path: 'format-tools/skills/format/SKILL.md' }, { pluginId: 'format-tools@acme', updatedAt: '2026-10-15T09:00:00Z' })] },
          { key: 'organization', title: 'Organization', skills: [this.skillRow({ name: 'release-checklist', description: 'What a release needs before it ships.', source: 'organization', path: 'sync:doc-1', switchable: !restricted, enabled: !restricted }, { updatedAt: '', blockedByPolicy: restricted })] }
        ].filter(g => g.skills.length)
    return create(ListExtensionSkillsResponseSchema, {
      groups, cwd, folderTrusted: !!cwd, projectSkippedUntrusted: false,
      policyNote: restricted ? 'Your administrator limits which skills can run. Your administrator turned external skill search off.' : '',
      waiting: external.filter(e => !e.approved).map(e => create(ExtensionSkillSchema, { external: e }))
    })
  }

  async plugins(cwd: string) {
    const st = await this.pluginsApi.list()
    const groups = new Map<string, ExtensionPluginGroupVM>()
    for (const p of st.plugins) {
      const key = p.suggestedBy ? 'project' : p.managed ? 'organization' : p.marketplace === 'local' ? 'local' : `marketplace:${p.marketplace}`
      if (key === 'project' && !cwd) continue
      const title = key === 'project' ? 'In this project' : key === 'organization' ? 'Organization' : key === 'local' ? 'Installed directly' : p.marketplace
      if (!groups.has(key)) groups.set(key, { key, title, plugins: [] })
      groups.get(key)!.plugins.push({ plugin: p, category: p.id.startsWith('format') ? 'Code quality' : p.managed ? 'Compliance' : 'Productivity', tags: p.id.startsWith('format') ? ['format', 'lint'] : [], publisher: p.managed ? 'Example Corp' : 'Acme', homepage: '', license: 'MIT', folder: `~/.harness/plugins/cache/${p.id}` })
    }
    return {
      groups: [...groups.values()],
      marketplaces: st.marketplaces.map(m => ({ $typeName: 'harness.agent.v1.ExtensionMarketplace' as const, name: m.name, description: m.description, source: m.source, commit: m.commit, updatedAt: m.updatedAt, managed: m.managed, refusal: m.refusal, problem: m.problem, pluginCount: m.plugins.length, installedCount: m.plugins.filter(l => l.installed).length, builtin: m.name === 'xlsft-catalog' })),
      cwd, folderTrusted: !!cwd, sourcesRestricted: this.scenario === 'restricted',
      suggestedMarketplaces: (cwd && this.scenario === 'default' ? { 'project-tools': 'example/project-tools' } : {}) as Record<string, string>
    }
  }

  async connectors(cwd: string) {
    const list = await this.mcpApi.list(cwd)
    const st = await this.pluginsApi.list()
    return create(ListExtensionConnectorsResponseSchema, { connectors: foldConnectors(this.scenario === 'first-run' ? [] : list.servers, st.plugins), registryError: '', canManageOrg: list.canManageOrg,
      projectRoot: list.projectRoot, projectTrusted: list.projectTrusted, projectHasFile: list.projectHasFile, projectError: '', accountAvailable: true })
  }

  private catalogConnectors(installed: (endpoint: string) => boolean): DiscoverItem[] {
    const rows: [string, string, string, string, string][] = [
      ['linear', 'Linear', 'Linear', 'Find, create and update issues and projects.', 'https://mcp.linear.app/mcp'],
      ['github', 'GitHub', 'GitHub', 'Read repositories, issues and pull requests, and act on them.', 'https://api.githubcopilot.com/mcp/'],
      ['sentry', 'Sentry', 'Sentry', 'Look into errors and performance issues of your projects.', 'https://mcp.sentry.dev/mcp'],
      ['notion', 'Notion', 'Notion', 'Search and edit pages and databases of a workspace.', 'https://mcp.notion.com/mcp'],
      ['context7', 'Context7', 'Upstash', 'Current documentation of libraries, by version.', 'https://mcp.context7.com/mcp'],
      ['huggingface', 'Hugging Face', 'Hugging Face', 'Search models, datasets and papers on the Hub.', 'https://huggingface.co/mcp'],
      ['stripe', 'Stripe', 'Stripe', 'Work with a Stripe account\'s data and read its documentation.', 'https://mcp.stripe.com']
    ]
    const restricted = this.scenario === 'restricted'
    return rows.map(([id, name, publisher, description, url]) => item({
      id: `catalog:${id}`, kind: ExtensionKind.CONNECTOR, name, publisher, description, sourceLabel: 'Default catalog', installed: installed(url), refusal: restricted && id !== 'github' ? 'Your organization\'s policy doesn\'t allow this server.' : '',
      options: [{ $typeName: 'harness.agent.v1.ConnectorOption', label: 'Remote', remote: true, suggestedSource: McpServerSource.ACCOUNT, required: [], server: { name: id, enabled: true, transport: Transport.STREAMABLE_HTTP, remote: { url } } as never }]
    }))
  }

  private registryConnectors(): DiscoverItem[] {
    const local = (id: string, name: string, description: string, spec: string, version: string): DiscoverItem => item({
      id: `registry:${id}`, kind: ExtensionKind.CONNECTOR, name, description, publisher: id.split('/')[0]!.split('.').reverse().join('.'), sourceLabel: 'MCP registry', version, updatedAt: '2026-11-01T00:00:00Z',
      options: [{ $typeName: 'harness.agent.v1.ConnectorOption', label: 'npm package', remote: false, suggestedSource: McpServerSource.DEVICE, required: [{ $typeName: 'harness.agent.v1.ConnectorRequiredValue', name: 'EXAMPLE_TOKEN', description: 'A token for the example service', secret: true }], server: { name: id.split('/').at(-1), enabled: true, transport: Transport.STDIO, stdio: { command: 'npx', args: ['-y', spec], env: { EXAMPLE_TOKEN: '${secret:EXAMPLE_TOKEN}' } } } as never }]
    })
    return [
      local('com.example/tickets', 'Example tickets', 'Registry entry for the demo: read tickets from an example service.', '@example/tickets@1.2.0', '1.2.0'),
      local('io.github.example/weather', 'Example weather', 'Registry entry for the demo: forecasts over stdio. A long description to show how a card keeps to two lines however much the source says about itself, which is often a lot.', '@example/weather@0.4.1', '0.4.1'),
      item({ id: 'registry:com.example/needs-url', kind: ExtensionKind.CONNECTOR, name: 'Example with a blank', description: 'Its URL holds a blank only you can fill in.', publisher: 'example.com', sourceLabel: 'MCP registry', version: '1.0.0', problem: 'The registry doesn\'t say how to add this one without details only you can fill in. Add it by hand.' })
    ]
  }

  async discover(q: DiscoverQuery) {
    const res = create(DiscoverExtensionsResponseSchema, {})
    const sections: DiscoverSection[] = []
    const section = (key: string, title: string, items: DiscoverItem[], nextCursor = ''): DiscoverSection => ({ $typeName: 'harness.agent.v1.DiscoverSection', key, title, total: items.length, items, nextCursor })
    const skills = q.kind === ExtensionKind.SKILL
    if (q.kind === ExtensionKind.CONNECTOR) {
      const list = await this.mcpApi.list('')
      const endpoints = new Set(list.servers.map(e => e.launchSummary.replace(/\/+$/, '')))
      sections.push(section('catalog', 'Well-known connectors', this.catalogConnectors(u => endpoints.has(u.replace(/\/+$/, '')))))
      if (this.scenario === 'offline') {
        res.registryProblem = 'The MCP registry can\'t be reached right now: dial tcp: network is unreachable'
      } else {
        sections.push(section('registry', 'From the MCP registry', this.registryConnectors(), q.cursor ? '' : 'more'))
        res.registryFetchedAt = '2026-11-06T12:00:00Z'
      }
      res.featured = sections[0]!.items.find(i => !i.installed && !i.refusal)
    } else {
      const st = await this.pluginsApi.list()
      for (const m of st.marketplaces) {
        const items = m.plugins.flatMap((l): DiscoverItem[] => {
          const base = { sourceLabel: m.name, version: l.version, publisher: m.name === 'xlsft-catalog' ? 'xlsft' : 'Acme', updatedAt: m.updatedAt, category: l.name.includes('format') ? 'Code quality' : l.name.includes('release') ? 'Releases' : 'Getting started', installed: l.installed, pluginId: l.id,
            contents: { $typeName: 'harness.agent.v1.PluginContents' as const, skills: [`${l.name}:main`], commands: [], agents: [], outputStyles: [], hooks: [], mcpServers: [], allowedTools: [], skipped: [] } }
          return skills
            ? [item({ ...base, id: `skill:${l.name}:main@${m.name}`, kind: ExtensionKind.SKILL, name: `${l.name}:main`, description: l.description })]
            : [item({ ...base, id: l.id, kind: ExtensionKind.PLUGIN, name: l.name, description: l.description })]
        })
        if (items.length) sections.push(section(`market:${m.name}`, m.name === 'xlsft-catalog' ? 'The xlsft`s harness catalog' : m.name, items))
      }
      sections.sort((a, b) => Number(b.key === 'market:xlsft-catalog') - Number(a.key === 'market:xlsft-catalog'))
      const builtin = sections.find(s => s.key === 'market:xlsft-catalog')
      res.featured = builtin?.items.find(i => !i.installed)
      const counts = new Map<string, number>()
      for (const s of sections) for (const i of s.items) if (i.category) counts.set(i.category, (counts.get(i.category) ?? 0) + 1)
      res.categories = [...counts].map(([name, count]) => ({ $typeName: 'harness.agent.v1.DiscoverCategory' as const, name, count })).sort((a, b) => b.count - a.count)
      if (skills) {
        if (this.scenario === 'restricted') res.externalBlockedByPolicy = true
        else if (this.scenario === 'offline') res.externalProblem = 'Skill search can\'t be reached right now.'
        else if (this.scenario === 'default') {
          const stored = new Set([...this.external.values()].map(e => e.id))
          sections.push(section('external', 'From skills.sh', FOUND.map(f => item({ id: `external:${f.id}`, kind: ExtensionKind.SKILL, name: f.name, publisher: f.source.split('/')[0]!, sourceLabel: 'skills.sh', installs: f.installs, audits: f.audits, externalId: f.id, installed: stored.has(f.id) }))))
        }
      }
    }
    res.sources = [...new Set(sections.flatMap(s => s.items.map(i => i.sourceLabel)))]
    const w = (q.query ?? '').toLowerCase().split(/\s+/).filter(Boolean)
    const keep = (i: DiscoverItem) => (!q.source || i.sourceLabel === q.source)
      && (!q.category || i.category.toLowerCase() === q.category.toLowerCase())
      && (!q.connectorType || i.options.some(o => o.remote === (q.connectorType === 'remote')))
      && (!q.hideInstalled || !i.installed)
      && w.every(x => `${i.name} ${i.description} ${i.publisher} ${i.category}`.toLowerCase().includes(x))
    const sort = (items: DiscoverItem[]) => {
      if (q.sort === 'name') items.sort((a, b) => a.name.localeCompare(b.name))
      else if (q.sort === 'updated') items.sort((a, b) => b.updatedAt.localeCompare(a.updatedAt))
      return items
    }
    const filtered = sections.map(s => ({ ...s, items: s.items.filter(keep), total: s.items.filter(keep).length })).filter(s => s.items.length)
    if (q.section) {
      res.featured = undefined
      res.sections = filtered.filter(s => s.key === q.section).map(s => ({ ...s, items: sort(s.items) }))
    } else if (w.length || q.category || q.source || q.connectorType || q.hideInstalled) {
      res.featured = undefined
      const all = sort(filtered.flatMap(s => s.items))
      res.sections = all.length ? [section('results', 'Results', all)] : []
    } else {
      res.sections = filtered.map(s => ({ ...s, items: sort(s.items).slice(0, OVERVIEW) }))
    }
    return res
  }

  async skillDetail() {
    return create(GetSkillDetailResponseSchema, { body: 'Do the work this skill describes, one step at a time.\n\nThis text comes from the demo.', files: ['SKILL.md', 'scripts/run.sh'] })
  }

  async createSkill(input: { name: string, description: string, body: string, cwd: string }) {
    if (!/^[A-Za-z0-9][A-Za-z0-9_-]*$/.test(input.name)) throw new Error('a skill\'s name uses letters, digits, "-" and "_", and starts with a letter or digit')
    const path = `~/.harness/skills/${input.name}/SKILL.md`
    this.created.push(this.skillRow({ name: input.name, description: input.description, source: 'personal', path }, { canRemove: true, folder: `~/.harness/skills/${input.name}`, updatedAt: new Date().toISOString() }))
    return path
  }

  async removeSkill(name: string) {
    this.created = this.created.filter(s => s.command?.name !== name)
  }

  private found(id: string): FoundSkill {
    const f = FOUND.find(x => x.id === id)
    if (!f) throw new Error('skills.sh has no such skill')
    return f
  }

  async externalSkill(id: string) {
    const f = this.found(id)
    const name = id.split('/').at(-1)!
    return create(GetExternalSkillResponseSchema, { hash: `hash-of-${name}`, audits: f.audits, license: f.license, description: f.description, installs: f.installs, name, source: f.source, installed: this.external.has(name),
      files: [{ path: 'SKILL.md', contents: `---\ndescription: ${f.description}\n---\nDo the work.` }] })
  }

  async installExternal(id: string, hash: string) {
    const f = this.found(id)
    const name = id.split('/').at(-1)!
    if (hash !== `hash-of-${name}`) throw new Error('this skill changed since you looked at it; look again')
    const v = this.installedView(f, false)
    this.external.set(name, v)
    return v
  }

  async setExternalApproval(name: string, approved: boolean, contentHash: string) {
    const e = this.external.get(name)
    if (!e) throw new Error(`${name} wasn't installed from search`)
    if (approved && contentHash !== e.contentHash) throw new Error(`${name} changed since you looked at it; look again`)
    e.approved = approved
  }

  async removeExternal(name: string) {
    this.external.delete(name)
  }
}
