// ExtensionsApi over the client's ExtensionsService, on the same connect transport (and agent token) as AgentService.

import { createClient, type Client, type Transport } from '@connectrpc/connect'
import { ExtensionsService } from '~/gen/harness/agent/v1/extensions_pb'
import { pluginFromProto } from '~/lib/plugins/connect'
import type { DiscoverQuery, ExtensionsApi } from './types'

export class ConnectExtensionsApi implements ExtensionsApi {
  private client: Client<typeof ExtensionsService>

  constructor(transport: Transport) {
    this.client = createClient(ExtensionsService, transport)
  }

  skills(cwd: string) {
    return this.client.listExtensionSkills({ cwd })
  }

  async plugins(cwd: string) {
    const res = await this.client.listExtensionPlugins({ cwd })
    return {
      groups: res.groups.map(g => ({ key: g.key, title: g.title, plugins: g.plugins.filter(p => p.plugin).map(p => ({
        plugin: pluginFromProto(p.plugin!), category: p.category, tags: p.tags, publisher: p.publisher, homepage: p.homepage, license: p.license, folder: p.folder
      })) })),
      marketplaces: res.marketplaces,
      cwd: res.cwd,
      folderTrusted: res.folderTrusted,
      sourcesRestricted: res.sourcesRestricted,
      suggestedMarketplaces: res.suggestedMarketplaces
    }
  }

  connectors(cwd: string) {
    return this.client.listExtensionConnectors({ cwd })
  }

  discover(q: DiscoverQuery) {
    return this.client.discoverExtensions(q)
  }

  skillDetail(name: string, path: string, cwd: string) {
    return this.client.getSkillDetail({ name, path, cwd })
  }

  async createSkill(input: { name: string, description: string, body: string, cwd: string }) {
    return (await this.client.createSkill(input)).path
  }

  async removeSkill(name: string, path: string, cwd: string) {
    await this.client.removeSkill({ name, path, cwd })
  }

  externalSkill(id: string) {
    return this.client.getExternalSkill({ id })
  }

  async installExternal(id: string, hash: string) {
    return (await this.client.installExternalSkill({ id, hash })).skill
  }

  async setExternalApproval(name: string, approved: boolean, contentHash: string) {
    await this.client.setExternalSkillApproval({ name, approved, contentHash })
  }

  async removeExternal(name: string) {
    await this.client.removeExternalSkill({ name })
  }
}
