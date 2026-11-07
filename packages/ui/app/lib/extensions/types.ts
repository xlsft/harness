// The Extensions screen: what this device has (Yours) and what it could add (Discover). The client joins skills, plugins and MCP servers
// (ExtensionsService); this layer only carries the answers. Changes to plugins and servers still go through PluginsApi and McpApi.

import type {
  ConnectorOption,
  DiscoverCategory,
  DiscoverExtensionsResponse,
  DiscoverItem,
  DiscoverSection,
  ExtensionConnector,
  ExtensionMarketplace,
  ExtensionSkill,
  ExtensionSkillGroup,
  GetExternalSkillResponse,
  GetSkillDetailResponse,
  InstalledExternalSkill,
  ListExtensionConnectorsResponse,
  ListExtensionSkillsResponse
} from '~/gen/harness/agent/v1/extensions_pb'
import { ConnectorStatus, ExtensionKind } from '~/gen/harness/agent/v1/extensions_pb'
import type { PluginVM } from '~/lib/plugins/types'

export { ConnectorStatus, ExtensionKind }
export type {
  ConnectorOption,
  DiscoverCategory,
  DiscoverExtensionsResponse,
  DiscoverItem,
  DiscoverSection,
  ExtensionConnector,
  ExtensionMarketplace,
  ExtensionSkill,
  ExtensionSkillGroup,
  GetExternalSkillResponse,
  GetSkillDetailResponse,
  InstalledExternalSkill,
  ListExtensionConnectorsResponse,
  ListExtensionSkillsResponse
}

export type ExtensionType = 'skills' | 'connectors' | 'plugins'
export type ExtensionView = 'yours' | 'discover'

export interface ExtensionPluginVM {
  plugin: PluginVM
  category: string
  tags: string[]
  publisher: string
  homepage: string
  license: string
  folder: string
}

export interface ExtensionPluginGroupVM {
  key: string
  title: string
  plugins: ExtensionPluginVM[]
}

export interface ExtensionPluginsVM {
  groups: ExtensionPluginGroupVM[]
  marketplaces: ExtensionMarketplace[]
  cwd: string
  folderTrusted: boolean
  sourcesRestricted: boolean
  suggestedMarketplaces: Record<string, string>
}

export interface DiscoverQuery {
  kind: ExtensionKind
  query?: string
  category?: string
  section?: string
  source?: string
  connectorType?: string
  hideInstalled?: boolean
  sort?: string
  cursor?: string
  refresh?: boolean
  cwd?: string
}

export interface ExtensionsApi {
  skills(cwd: string): Promise<ListExtensionSkillsResponse>
  plugins(cwd: string): Promise<ExtensionPluginsVM>
  connectors(cwd: string): Promise<ListExtensionConnectorsResponse>
  discover(q: DiscoverQuery): Promise<DiscoverExtensionsResponse>
  skillDetail(name: string, path: string, cwd: string): Promise<GetSkillDetailResponse>
  createSkill(input: { name: string, description: string, body: string, cwd: string }): Promise<string>
  removeSkill(name: string, path: string, cwd: string): Promise<void>
  externalSkill(id: string): Promise<GetExternalSkillResponse>
  // hash is what was shown: the client refuses other content.
  installExternal(id: string, hash: string): Promise<InstalledExternalSkill | undefined>
  setExternalApproval(name: string, approved: boolean, contentHash: string): Promise<void>
  removeExternal(name: string): Promise<void>
}
