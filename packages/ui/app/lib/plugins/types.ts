// Settings > Plugins works on the client's PluginService. The client decides what may run; this only shows it and sends intent.

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

export interface PluginContentsVM {
  skills: string[]
  commands: string[]
  agents: string[]
  outputStyles: string[]
  hooks: PluginHookVM[]
  mcpServers: PluginMcpVM[]
  allowedTools: string[]
  skipped: string[]
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
  contents: PluginContentsVM
  problems: string[]
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
