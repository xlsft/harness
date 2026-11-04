// Computer use: wire mapping (computer_use.proto → view models). What is allowed is decided by the client; this only
// renders it and sends the user's answer.
import { ComputerAccessTier } from '~/gen/harness/agent/v1/computer_use_pb'
import type { ComputerAccessRequested, ComputerUsePermission, ComputerUseState } from '~/gen/harness/agent/v1/computer_use_pb'
import type { ContentBlock } from '~/gen/harness/llm/v1/content_pb'
import type { ComputerAccessItem, ComputerPermissionVM, ComputerTierName, ComputerUseVM, ToolImage } from './types'

export function tierName(t: ComputerAccessTier): ComputerTierName {
  if (t === ComputerAccessTier.FULL) return 'full'
  if (t === ComputerAccessTier.CLICK) return 'click'
  return 'read'
}

export const TIER_LABELS: Record<ComputerTierName, string> = { read: 'Read only', click: 'Click only', full: 'Full control' }

export function permissionVM(p: ComputerUsePermission): ComputerPermissionVM {
  return { id: p.id, name: p.name, granted: p.granted, howToGrant: p.howToGrant }
}

export function accessItem(r: ComputerAccessRequested): ComputerAccessItem {
  return {
    kind: 'computerAccess',
    id: r.requestId,
    toolUseId: r.toolUseId,
    reason: r.reason,
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
