// Computer use: wire mapping (computer_use.proto → view models). What is allowed is decided by the client; this only
// renders it and sends the user's answer.
import { ComputerAccessTier } from '~/gen/harness/agent/v1/computer_use_pb'
import type { ComputerAccessRequested, ComputerUsePermission, ComputerUseProfile, ComputerUseState } from '~/gen/harness/agent/v1/computer_use_pb'
import type { ContentBlock } from '~/gen/harness/llm/v1/content_pb'
import type { ComputerAccessItem, ComputerPermissionVM, ComputerProfileVM, ComputerTierName, ComputerUseVM, ToolImage } from './types'

export function tierName(t: ComputerAccessTier): ComputerTierName {
  if (t === ComputerAccessTier.FULL) return 'full'
  if (t === ComputerAccessTier.CLICK) return 'click'
  if (t === ComputerAccessTier.READ) return 'read'
  return 'blocked'
}

const TIERS: Record<ComputerTierName, ComputerAccessTier> = {
  blocked: ComputerAccessTier.BLOCKED,
  read: ComputerAccessTier.READ,
  click: ComputerAccessTier.CLICK,
  full: ComputerAccessTier.FULL
}

export const tierValue = (t: ComputerTierName) => TIERS[t]

export function profileVM(p: ComputerUseProfile): ComputerProfileVM {
  return {
    id: p.id,
    name: p.name,
    browsers: tierName(p.browsers),
    terminals: tierName(p.terminals),
    other: tierName(p.other),
    apps: p.appTiers.map(a => ({ id: a.appId, name: a.appName, tier: tierName(a.tier) })),
    lifted: [...p.liftedBlocks],
    allowSecureTyping: p.allowSecureTyping,
    allowRemoteOrigin: p.allowRemoteOrigin,
    allowSystemShortcuts: p.allowSystemShortcuts,
    allowSettingsEdit: p.allowSettingsEdit
  }
}

// The custom profile as SetComputerUseProfile takes it. The client decides what it means.
export function profileMessage(p: ComputerProfileVM) {
  return {
    browsers: tierValue(p.browsers),
    terminals: tierValue(p.terminals),
    other: tierValue(p.other),
    appTiers: p.apps.map(a => ({ appId: a.id, appName: a.name, tier: tierValue(a.tier) })),
    liftedBlocks: p.lifted,
    allowSecureTyping: p.allowSecureTyping,
    allowRemoteOrigin: p.allowRemoteOrigin,
    allowSystemShortcuts: p.allowSystemShortcuts,
    allowSettingsEdit: p.allowSettingsEdit
  }
}

export const TIER_LABELS: Record<ComputerTierName, string> = { blocked: 'Blocked', read: 'Read only', click: 'Click only', full: 'Full control' }

export function permissionVM(p: ComputerUsePermission): ComputerPermissionVM {
  return { id: p.id, name: p.name, granted: p.granted, howToGrant: p.howToGrant }
}

export function accessItem(r: ComputerAccessRequested): ComputerAccessItem {
  return {
    kind: 'computerAccess',
    id: r.requestId,
    toolUseId: r.toolUseId,
    reason: r.reason,
    profileName: r.profileName,
    apps: r.apps.map(a => ({ id: a.id, name: a.name, tier: tierName(a.tier), note: a.tierNote })),
    missing: r.missingPermissions.map(permissionVM),
    state: 'pending',
    approved: []
  }
}

export function computerUseVM(s: ComputerUseState): ComputerUseVM {
  return { controlling: s.controlling, action: s.action, grants: s.grants.map(g => ({ id: g.appId, name: g.appName, tier: tierName(g.tier) })) }
}

function base64(data: Uint8Array): string {
  let bin = ''
  const step = 0x8000
  for (let i = 0; i < data.length; i += step) bin += String.fromCharCode(...data.subarray(i, i + step))
  return btoa(bin)
}

// The images of a tool result, as URLs an <img> can show.
export function toolImages(blocks: ContentBlock[]): ToolImage[] {
  const out: ToolImage[] = []
  for (const b of blocks) {
    if (b.block.case !== 'image') continue
    out.push({ url: `data:${b.block.value.mediaType || 'image/jpeg'};base64,${base64(b.block.value.data)}` })
  }
  return out
}
