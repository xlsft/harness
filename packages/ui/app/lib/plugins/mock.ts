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
    plugins: [plugin({})],
    marketplaces: [{ name: 'acme', description: 'Plugins of the Acme team', source: 'acme/plugins', commit: 'a1b2c3d', updatedAt: '2026-11-01T10:00:00Z', managed: false, refusal: '', problem: '',
      plugins: [
        { id: 'format-tools@acme', name: 'format-tools', description: 'Formats what the agent writes', version: '1.2.0', installed: true },
        { id: 'release-notes@acme', name: 'release-notes', description: 'Writes release notes from the log', version: '0.3.0', installed: false }
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
    const name = source.split('/').pop() ?? source
    this.state.marketplaces.push({ name, description: '', source, commit: '', updatedAt: '', managed: false, refusal: '', problem: '', plugins: [] })
    return name
  }

  async updateMarketplace() {}

  async removeMarketplace(name: string) {
    this.state.marketplaces = this.state.marketplaces.filter(m => m.name !== name)
    this.state.plugins = this.state.plugins.filter(p => p.marketplace !== name)
  }
}
