import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { ConnectorStatus, ExtensionConnectorSchema, ExtensionKind, ExtensionSkillGroupSchema, ExtensionSkillSchema } from '~/gen/harness/agent/v1/extensions_pb'
import { SlashCommandSchema } from '~/gen/harness/agent/v1/slash_commands_pb'
import { foldConnectors, MockExtensionsApi } from '~/lib/extensions/mock'
import { auditSummary, compactTags, connectorStatus, installsLabel, narrowConnectors, narrowSkills, parseType, parseView, providedBy, shortDate } from '~/lib/extensions/view'
import { MockMcpApi } from '~/lib/mcp/mock'
import { MockPluginsApi } from '~/lib/plugins/mock'
import { mockSlashCatalog } from '~/lib/transport/mockSlash'

const skill = (name: string, description = '') => create(ExtensionSkillSchema, { command: create(SlashCommandSchema, { name, description, enabled: true }) })

describe('words', () => {
  it('statuses', () => {
    expect(connectorStatus(ConnectorStatus.IN_SESSIONS)).toEqual({ label: 'Connects in sessions', tone: 'muted' })
    expect(connectorStatus(ConnectorStatus.ERROR).tone).toBe('bad')
    expect(connectorStatus(ConnectorStatus.NEEDS_SIGN_IN).label).toBe('Needs sign-in')
  })

  it('counts: nothing when the source gives none', () => {
    expect(installsLabel(0n)).toBe('')
    expect(installsLabel(1n)).toBe('1 install')
    expect(installsLabel(960n)).toBe('960 installs')
    expect(installsLabel(18200n)).toBe('18.2K installs')
    expect(installsLabel(3_000_000n)).toBe('3M installs')
  })

  it('the worst audit speaks; no audit is no verdict', () => {
    expect(auditSummary([])).toBeNull()
    expect(auditSummary([{ status: 'pass' }])).toEqual({ label: 'Audit: pass', tone: 'ok' })
    expect(auditSummary([{ status: 'pass' }, { status: 'warn' }, { status: 'FAIL' }])).toEqual({ label: 'Audit: fail', tone: 'bad' })
    expect(auditSummary([{ status: 'pending' }])?.tone).toBe('warn')
  })

  it('dates, tags and who provides', () => {
    const now = new Date('2026-11-06T00:00:00Z')
    expect(shortDate('2026-10-28T09:00:00Z', now)).toBe('Oct 28')
    expect(shortDate('2025-10-28T09:00:00Z', now)).toBe('Oct 28, 2025')
    expect(shortDate('', now)).toBe('')
    expect(compactTags(['a', 'b', 'c'])).toEqual({ first: 'a', more: 2 })
    expect(compactTags([])).toBeNull()
    expect(providedBy({ providers: [] })).toBe('')
    expect(providedBy({ providers: [{ pluginName: 'A' }, { pluginName: 'A' }] as never })).toBe('Provided by the A plugin')
    expect(providedBy({ providers: [{ pluginName: 'A' }, { pluginName: 'B' }, { pluginName: 'C' }] as never })).toBe('Provided by the A, B and C plugins')
  })

  it('reads the URL', () => {
    expect(parseType('plugins')).toBe('plugins')
    expect(parseType('nonsense')).toBe('skills')
    expect(parseView('discover')).toBe('discover')
    expect(parseView(undefined)).toBe('yours')
  })
})

describe('narrowing', () => {
  it('drops a group left with nothing', () => {
    const groups = [
      create(ExtensionSkillGroupSchema, { key: 'personal', title: 'Personal', skills: [skill('alpha', 'first'), skill('beta')] }),
      create(ExtensionSkillGroupSchema, { key: 'organization', title: 'Organization', skills: [skill('gamma')] })
    ]
    const by = (query: string, source = '') => narrowSkills(groups, { query, source, state: '', sort: '' }).map(g => [g.key, g.skills.map(s => s.command?.name)])
    expect(by('alpha')).toEqual([['personal', ['alpha']]])
    expect(by('FIRST')).toEqual([['personal', ['alpha']]])
    expect(by('organization')).toEqual([['organization', ['gamma']]])
    expect(by('', 'organization')).toEqual([['organization', ['gamma']]])
    expect(by('nothing')).toEqual([])
  })

  it('connectors by manager, type and status', () => {
    const rows = [
      create(ExtensionConnectorSchema, { id: 'a', name: 'a', remote: true, badges: ['Admin'], status: ConnectorStatus.CONNECTED }),
      create(ExtensionConnectorSchema, { id: 'b', name: 'b', remote: false, badges: ['Device'], status: ConnectorStatus.NEEDS_APPROVAL }),
      create(ExtensionConnectorSchema, { id: 'c', name: 'c', remote: true, badges: ['Plugin'], status: ConnectorStatus.ERROR })
    ]
    const by = (f: Partial<Parameters<typeof narrowConnectors>[1]>) => narrowConnectors(rows, { query: '', manager: '', type: '', status: '', ...f }).map(r => r.name)
    expect(by({ manager: 'Admin' })).toEqual(['a'])
    expect(by({ type: 'local' })).toEqual(['b'])
    expect(by({ status: 'attention' })).toEqual(['b', 'c'])
    expect(by({ status: 'connected' })).toEqual(['a'])
  })
})

describe('the demo store', () => {
  const api = (scenario: 'default' | 'first-run' | 'offline' | 'restricted' = 'default') => {
    const plugins = new MockPluginsApi()
    const mcp = new MockMcpApi()
    return { plugins, mcp, ext: new MockExtensionsApi(plugins, mcp, () => mockSlashCatalog(new Set()), scenario) }
  }

  it('folds what plugins share, and keeps a plugin\'s own command apart', async () => {
    const { plugins } = api()
    const st = await plugins.list()
    const rows = foldConnectors([], st.plugins)
    expect(rows.filter(r => r.name === 'tasks')).toHaveLength(1)
    expect(rows.find(r => r.name === 'tasks')!.providers.map(p => p.pluginId)).toEqual(['docs-kit@acme', 'plan-kit@acme'])
    const own = st.plugins[0]!
    const twice = foldConnectors([], [
      { ...own, id: 'one@x', contents: { ...own.contents, mcpServers: [{ name: 'plugin:one:run', transport: 'stdio', command: 'node ${CLAUDE_PLUGIN_ROOT}/s.js' }] } },
      { ...own, id: 'two@x', contents: { ...own.contents, mcpServers: [{ name: 'plugin:two:run', transport: 'stdio', command: 'node ${CLAUDE_PLUGIN_ROOT}/s.js' }] } }
    ])
    expect(twice).toHaveLength(2)
  })

  it('a plugin approved in one place shows everywhere', async () => {
    const { ext, plugins } = api()
    const before = await ext.connectors('')
    expect(before.connectors.find(c => c.name === 'style')!.status).toBe(ConnectorStatus.NEEDS_APPROVAL)
    await plugins.setApproval('format-tools@acme', true, 'sha256:mock')
    const after = await ext.connectors('')
    expect(after.connectors.find(c => c.name === 'style')!.status).toBe(ConnectorStatus.IN_SESSIONS)
  })

  it('the built-in marketplace can\'t be removed', async () => {
    const { plugins } = api()
    await expect(plugins.removeMarketplace('xlsft-catalog')).rejects.toThrow('ships with this client')
  })

  it('refuses a hash other than the one shown', async () => {
    const { ext } = api()
    await expect(ext.installExternal('example-labs/skills/pdf-tools', 'other')).rejects.toThrow('changed')
    const stored = (await ext.installExternal('example-labs/skills/pdf-tools', 'hash-of-pdf-tools'))!
    expect(stored.approved).toBe(false)
    await expect(ext.setExternalApproval('pdf-tools', true, 'sha256:other')).rejects.toThrow('changed')
    await ext.setExternalApproval('pdf-tools', true, stored.contentHash)
    const list = await ext.skills('')
    expect(list.groups.find(g => g.key === 'external')!.skills.map(s => s.external?.name)).toContain('pdf-tools')
  })

  it('search isn\'t a section of its own on a server without it', async () => {
    const { ext } = api('first-run')
    const res = await ext.discover({ kind: ExtensionKind.SKILL })
    expect(res.sections.map(s => s.key)).toEqual(['market:xlsft-catalog'])
    expect(res.externalBlockedByPolicy).toBe(false)
  })
})
