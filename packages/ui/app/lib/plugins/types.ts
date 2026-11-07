// Extensions > Plugins works on the client's PluginService. The client decides what may run; this only shows it and sends intent.

import { tt } from '~/utils/i18n'

export type PluginStatusVM = 'active' | 'needs_approval' | 'disabled' | 'untrusted_folder' | 'blocked_by_policy' | 'changed_on_disk' | 'broken' | 'not_installed'

export interface PluginHookVM {
  event: string
  matcher: string
  type: string
  command: string
  fromSkill: string
}

export interface PluginMcpVM {
  name: string
  transport: string
  command: string
}

// Plugin UI (docs/design/plugin-ui.md): a pane is an HTML page of the plugin, a status-bar item opens one or sends a prompt.
export interface PluginPaneVM {
  pluginId: string
  id: string
  title: string
  icon: string
  page: string
}

export interface PluginStatusItemVM {
  pluginId: string
  id: string
  text: string
  icon: string
  tooltip: string
  pane: string
  prompt: string
}

// A capability of a plugin's UI and whether this device approved it for the plugin's current content.
export interface PluginUiGrantVM {
  capability: string
  granted: boolean
  grantedAt: string
}

export interface PluginContentsVM {
  skills: string[]
  commands: string[]
  agents: string[]
  outputStyles: string[]
  hooks: PluginHookVM[]
  mcpServers: PluginMcpVM[]
  allowedTools: string[]
  skipped: string[]
  panes: PluginPaneVM[]
  statusItems: PluginStatusItemVM[]
  uiCapabilities: string[]
  uiNetwork: string[]
}

export interface PluginVM {
  id: string
  name: string
  marketplace: string
  displayName: string
  description: string
  version: string
  commit: string
  contentHash: string
  status: PluginStatusVM
  reason: string
  enabled: boolean
  approved: boolean
  pinned: boolean
  managed: boolean
  suggestedBy: string
  source: string
  installedAt?: string
  contents: PluginContentsVM
  problems: string[]
  uiGrants: PluginUiGrantVM[]
  // Why policy hides its panes and status-bar items.
  uiBlocked: string
}

export interface PluginListingVM {
  id: string
  name: string
  description: string
  version: string
  installed: boolean
}

export interface PluginMarketplaceVM {
  name: string
  description: string
  source: string
  commit: string
  updatedAt: string
  managed: boolean
  refusal: string
  problem: string
  plugins: PluginListingVM[]
}

export interface PluginsState {
  plugins: PluginVM[]
  marketplaces: PluginMarketplaceVM[]
  sourcesRestricted: boolean
  folderTrusted: boolean
  suggestedMarketplaces: Record<string, string>
}

// The panes and status-bar items of the plugins that may run in a session's folder.
export interface PluginUiListVM {
  panes: PluginPaneVM[]
  statusItems: PluginStatusItemVM[]
  pluginNames: Record<string, string>
  // Set when policy hides all plugin UI.
  blocked: string
  // What a grant made from a status-bar item applies to, by plugin id.
  contentHashes: Record<string, string>
}

// A pane's document, as the client built it: the CSP first in its head, then the host marker.
export interface PluginPaneDocVM {
  pane: PluginPaneVM
  html: string
  csp: string
  // Declared origins this device hasn't approved.
  networkPending: string[]
  pluginName: string
  contentHash: string
}

// One call of the message API, answered by the client.
export interface PluginUiCallVM {
  resultJson: string
  // The capability to ask the user for; the call is made again after.
  needsApproval: string
  openFile: string
  theme: boolean
}

export interface PluginsApi {
  list(cwd: string): Promise<PluginsState>
  install(spec: string, cwd: string): Promise<PluginVM>
  update(id: string, cwd: string): Promise<{ plugin: PluginVM | undefined, changed: boolean, note: string }>
  remove(id: string, keepData: boolean): Promise<void>
  setEnabled(id: string, enabled: boolean): Promise<void>
  // contentHash is the hash of what was shown: the client refuses other content.
  setApproval(id: string, approved: boolean, contentHash: string): Promise<void>
  setPinned(id: string, pinned: boolean): Promise<void>
  addMarketplace(source: string): Promise<string>
  updateMarketplace(name: string): Promise<void>
  removeMarketplace(name: string): Promise<void>
  listUi(sessionId: string): Promise<PluginUiListVM>
  getPane(pluginId: string, paneId: string, sessionId: string): Promise<PluginPaneDocVM>
  callUi(pluginId: string, sessionId: string, method: string, paramsJson: string): Promise<PluginUiCallVM>
  // Only the app's own controls call this, never a pane. contentHash is what was shown.
  setUiGrant(pluginId: string, capability: string, granted: boolean, contentHash: string): Promise<void>
}

// What a capability lets a plugin's panes do, for the person deciding.
export function capabilityLabel(c: string, network: string[] = []): string {
  switch (c) {
    case 'session.read': return tt('Read this session\'s title, model, mode, folder, cost and recent messages')
    case 'session.prompt': return tt('Send messages to this session')
    case 'files.open': return tt('Open files of this session in the Files pane')
    case 'theme.read': return tt('Read the app\'s theme colors')
    case 'network': return network.length ? tt('Reach {0}', [network.join(', ')]) : tt('Reach the network')
  }
  return c
}

export function statusLabel(s: PluginStatusVM): string {
  return {
    active: 'Runs',
    needs_approval: 'Needs approval',
    disabled: 'Off',
    untrusted_folder: 'Folder not trusted',
    blocked_by_policy: 'Blocked by policy',
    changed_on_disk: 'Changed on disk',
    broken: 'Can\'t be read',
    not_installed: 'Not installed'
  }[s]
}

// What to read before approving: true when the plugin can do more than give the model text.
export function runsPrograms(c: PluginContentsVM): boolean {
  return c.hooks.length > 0 || c.mcpServers.length > 0 || c.allowedTools.length > 0
}
