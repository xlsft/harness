// PluginsApi over the client's PluginService, on the same connect transport (and agent token) as AgentService.

import { createClient, type Client, type Transport } from '@connectrpc/connect'
import { PluginService, PluginStatus, type Plugin } from '~/gen/harness/agent/v1/plugins_pb'
import type { PluginsApi, PluginsState, PluginStatusVM, PluginVM } from './types'

const STATUS: Partial<Record<PluginStatus, PluginStatusVM>> = {
  [PluginStatus.ACTIVE]: 'active',
  [PluginStatus.NEEDS_APPROVAL]: 'needs_approval',
  [PluginStatus.DISABLED]: 'disabled',
  [PluginStatus.UNTRUSTED_FOLDER]: 'untrusted_folder',
  [PluginStatus.BLOCKED_BY_POLICY]: 'blocked_by_policy',
  [PluginStatus.CHANGED_ON_DISK]: 'changed_on_disk',
  [PluginStatus.BROKEN]: 'broken',
  [PluginStatus.NOT_INSTALLED]: 'not_installed'
}

export function pluginFromProto(p: Plugin): PluginVM {
  const c = p.contents
  return {
    id: p.id, name: p.name, marketplace: p.marketplace, displayName: p.displayName, description: p.description, version: p.version, commit: p.commit,
    contentHash: p.contentHash, status: STATUS[p.status] ?? 'broken', reason: p.reason, enabled: p.enabled, approved: p.approved, pinned: p.pinned, managed: p.managed,
    suggestedBy: p.suggestedBy, source: p.source, installedAt: p.installedAt, problems: p.problems,
    contents: {
      skills: c?.skills ?? [], commands: c?.commands ?? [], agents: c?.agents ?? [], outputStyles: c?.outputStyles ?? [],
      hooks: (c?.hooks ?? []).map(h => ({ event: h.event, matcher: h.matcher, type: h.type, command: h.command, fromSkill: h.fromSkill })),
      mcpServers: (c?.mcpServers ?? []).map(m => ({ name: m.name, transport: m.transport, command: m.command })),
      allowedTools: c?.allowedTools ?? [], skipped: c?.skipped ?? []
    }
  }
}

export class ConnectPluginsApi implements PluginsApi {
  private client: Client<typeof PluginService>

  constructor(transport: Transport) {
    this.client = createClient(PluginService, transport)
  }

  async list(cwd: string): Promise<PluginsState> {
    const res = await this.client.listPlugins({ cwd })
    return {
      plugins: res.plugins.map(pluginFromProto),
      marketplaces: res.marketplaces.map(m => ({ name: m.name, description: m.description, source: m.source, commit: m.commit, updatedAt: m.updatedAt, managed: m.managed, refusal: m.refusal, problem: m.problem,
        plugins: m.plugins.map(l => ({ id: l.id, name: l.name, description: l.description, version: l.version, installed: l.installed })) })),
      sourcesRestricted: res.sourcesRestricted,
      folderTrusted: res.folderTrusted,
      suggestedMarketplaces: res.suggestedMarketplaces
    }
  }

  async install(spec: string, cwd: string) {
    const res = await this.client.installPlugin({ spec, cwd })
    if (!res.plugin) throw new Error('the client returned no plugin')
    return pluginFromProto(res.plugin)
  }

  async update(id: string, cwd: string) {
    const res = await this.client.updatePlugin({ id, cwd })
    return { plugin: res.plugin ? pluginFromProto(res.plugin) : undefined, changed: res.changed, note: res.note }
  }

  async remove(id: string, keepData: boolean) {
    await this.client.removePlugin({ id, keepData })
  }

  async setEnabled(id: string, enabled: boolean) {
    await this.client.setPluginEnabled({ id, enabled })
  }

  async setApproval(id: string, approved: boolean, contentHash: string) {
    await this.client.setPluginApproval({ id, approved, contentHash })
  }

  async setPinned(id: string, pinned: boolean) {
    await this.client.setPluginPinned({ id, pinned })
  }

  async addMarketplace(source: string) {
    return (await this.client.addPluginMarketplace({ source })).name
  }

  async updateMarketplace(name: string) {
    await this.client.updatePluginMarketplace({ name })
  }

  async removeMarketplace(name: string) {
    await this.client.removePluginMarketplace({ name })
  }
}
