// A plugin store for the demo and tests: it behaves like the client's gates (nothing runs until this content is approved).

import type { PluginsApi, PluginsState, PluginVM } from './types'

function plugin(over: Partial<PluginVM>): PluginVM {
  return {
    id: 'format-tools@acme', name: 'format-tools', marketplace: 'acme', displayName: 'Format tools', description: 'Formats what the agent writes', version: '1.2.0', commit: 'a1b2c3d4e5f6',
    contentHash: 'sha256:mock', status: 'needs_approval', reason: 'Nothing of it runs until you approve it on this device.', enabled: true, approved: false, pinned: false, managed: false,
    suggestedBy: '', source: '', problems: [],
    contents: {
      skills: ['format-tools:format'], commands: ['format-tools:check'], agents: ['format-tools:reviewer'], outputStyles: [],
      hooks: [{ event: 'PostToolUse', matcher: 'Write|Edit', type: 'command', command: '"${CLAUDE_PLUGIN_ROOT}"/scripts/format.sh', fromSkill: '' }],
      mcpServers: [{ name: 'plugin:format-tools:style', transport: 'stdio', command: 'node server.js' }],
      allowedTools: ['Bash(prettier:*)'], skipped: []
    },
    ...over
  }
}

export class MockPluginsApi implements PluginsApi {
  private state: PluginsState = {
    plugins: [
      plugin({}),
      plugin({ id: 'docs-kit@acme', name: 'docs-kit', displayName: 'Docs kit', description: 'Keeps documentation next to the code it describes', status: 'active', reason: '', approved: true, version: '2.0.1',
        contents: { skills: ['docs-kit:write'], commands: [], agents: [], outputStyles: [], hooks: [], mcpServers: [{ name: 'plugin:docs-kit:tasks', transport: 'sse', command: 'https://mcp.tasks.example/sse' }], allowedTools: [], skipped: [] } }),
      plugin({ id: 'plan-kit@acme', name: 'plan-kit', displayName: 'Plan kit', description: 'Specs and roadmaps from what is in the tracker', status: 'disabled', reason: 'It is turned off.', enabled: false, approved: true, version: '1.0.0',
        contents: { skills: ['plan-kit:spec'], commands: [], agents: [], outputStyles: [], hooks: [], mcpServers: [{ name: 'plugin:plan-kit:tasks', transport: 'sse', command: 'https://mcp.tasks.example/sse' }], allowedTools: [], skipped: [] } }),
      plugin({ id: 'org-standards@corp', name: 'org-standards', marketplace: 'corp', displayName: 'Org standards', description: 'What the organization requires of a change', status: 'needs_approval', managed: true, version: '3.1.0',
        contents: { skills: ['org-standards:check'], commands: [], agents: [], outputStyles: [], hooks: [], mcpServers: [], allowedTools: [], skipped: [] } })
    ],
    marketplaces: [{ name: 'xlsft-catalog', description: 'Skills that ship with xlsft`s harness.', source: '~/.harness/plugins/builtin-catalog', commit: '', updatedAt: '2026-11-06T10:00:00Z', managed: false, refusal: '', problem: '',
      plugins: [{ id: 'starter@xlsft-catalog', name: 'starter', description: 'A first skill to read and copy: find your way around a repository.', version: '0.1.0', installed: false }] }, { name: 'acme', description: 'Plugins of the Acme team', source: 'acme/plugins', commit: 'a1b2c3d', updatedAt: '2026-11-01T10:00:00Z', managed: false, refusal: '', problem: '',
      plugins: [
        { id: 'format-tools@acme', name: 'format-tools', description: 'Formats what the agent writes', version: '1.2.0', installed: true },
        { id: 'docs-kit@acme', name: 'docs-kit', description: 'Keeps documentation next to the code it describes', version: '2.0.1', installed: true },
        { id: 'plan-kit@acme', name: 'plan-kit', description: 'Specs and roadmaps from what is in the tracker', version: '1.0.0', installed: true },
        { id: 'release-notes@acme', name: 'release-notes', description: 'Writes release notes from the log', version: '0.3.0', installed: false },
        { id: 'triage@acme', name: 'triage', description: 'Sorts an issue queue by what needs doing first', version: '0.2.0', installed: false },
        { id: 'changelog@acme', name: 'changelog', description: 'Turns merged pull requests into a changelog people want to read, with a section per area and the breaking changes first', version: '1.4.0', installed: false },
        { id: 'style-guide@acme', name: 'style-guide', description: 'Checks writing against the team\'s style guide', version: '0.9.0', installed: false },
        { id: 'migrations@acme', name: 'migrations', description: 'Plans and reviews database migrations', version: '0.5.0', installed: false }
      ] }],
    sourcesRestricted: false,
    folderTrusted: true,
    suggestedMarketplaces: {}
  }

  private find(id: string): PluginVM {
    const p = this.state.plugins.find(x => x.id === id)
    if (!p) throw new Error(`${id} is not installed`)
    return p
  }

  private settle(p: PluginVM) {
    if (!p.enabled) {
      p.status = 'disabled'
      p.reason = 'It is turned off.'
    } else if (!p.approved) {
      p.status = 'needs_approval'
      p.reason = 'Nothing of it runs until you approve it on this device.'
    } else {
      p.status = 'active'
      p.reason = ''
    }
  }

  // The demo's first run: the built-in catalog and nothing else.
  clearForDemo() {
    this.state.plugins = []
    this.state.marketplaces = this.state.marketplaces.filter(m => m.name === 'xlsft-catalog')
  }

  async list() {
    return structuredClone(this.state)
  }

  async install(spec: string) {
    const [name = spec, marketplace = 'local'] = spec.split('@')
    const p = plugin({ id: `${name}@${marketplace}`, name, marketplace, displayName: '', description: '' })
    this.state.plugins.push(p)
    for (const m of this.state.marketplaces) {
      for (const l of m.plugins) if (l.id === p.id) l.installed = true
    }
    return structuredClone(p)
  }

  async update(id: string) {
    return { plugin: structuredClone(this.find(id)), changed: false, note: `${id} is up to date.` }
  }

  async remove(id: string) {
    this.find(id)
    this.state.plugins = this.state.plugins.filter(p => p.id !== id)
    for (const m of this.state.marketplaces) {
      for (const l of m.plugins) if (l.id === id) l.installed = false
    }
  }

  async setEnabled(id: string, enabled: boolean) {
    const p = this.find(id)
    p.enabled = enabled
    this.settle(p)
  }

  async setApproval(id: string, approved: boolean, contentHash: string) {
    const p = this.find(id)
    if (approved && contentHash !== p.contentHash) throw new Error(`${id} changed since you looked at it; look again`)
    p.approved = approved
    this.settle(p)
  }

  async setPinned(id: string, pinned: boolean) {
    this.find(id).pinned = pinned
  }

  async addMarketplace(source: string) {
    if (this.state.sourcesRestricted) throw new Error(`${source} is not on your organization's list of allowed plugin sources.`)
    const name = source.split('/').pop() ?? source
    this.state.marketplaces.push({ name, description: '', source, commit: '', updatedAt: '', managed: false, refusal: '', problem: '', plugins: [] })
    return name
  }

  async updateMarketplace() {}

  async removeMarketplace(name: string) {
    if (name === 'xlsft-catalog') throw new Error(`the marketplace ${name} ships with this client`)
    this.state.marketplaces = this.state.marketplaces.filter(m => m.name !== name)
    this.state.plugins = this.state.plugins.filter(p => p.marketplace !== name)
  }
}
