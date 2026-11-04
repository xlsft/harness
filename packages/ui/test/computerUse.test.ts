import { describe, expect, it, vi } from 'vitest'
import { create } from '@bufbuild/protobuf'
import { ComputerAccessTier, ComputerUseProfileSchema } from '~/gen/harness/agent/v1/computer_use_pb'
import { profileMessage, profileVM } from '~/lib/session/computer'
import { computerGroupLabel } from '~/utils/tools'
import { createSessionState, reduce, reduceAll, settleTurn } from '~/lib/session/reducer'
import type { ComputerAccessItem, ToolItem } from '~/lib/session/types'
import { EventBuilder } from '~/lib/transport/events'
import { MockTransport } from '~/lib/transport/mock'

vi.mock('vue', () => ({ markRaw: <T>(v: T) => v, toRaw: <T>(v: T) => v }))

const T0 = Date.parse('2026-11-04T12:00:00Z')

function setup() {
  return { b: new EventBuilder('s1', T0), s: createSessionState('s1') }
}
const card = (s: ReturnType<typeof createSessionState>) => s.items.find(i => i.kind === 'computerAccess') as ComputerAccessItem

const APPS = [
  { id: 'com.apple.Notes', name: 'Notes', tier: ComputerAccessTier.FULL, tierNote: 'Full' },
  { id: 'com.apple.Safari', name: 'Safari', tier: ComputerAccessTier.READ, tierNote: 'Read-only' }
]

describe('computer use in the session state', () => {
  it('shows the card and goes back to working when answered', () => {
    const { b, s } = setup()
    reduceAll(s, [
      b.computerAccessRequested('r1', 't1', 'to write it down', APPS, [{ id: 'accessibility', name: 'Accessibility', granted: false, howToGrant: 'Turn it on.' }])
    ])
    expect(s.meta.status).toBe('needs_input')
    expect(s.pendingComputerAccess).toBe('r1')
    const c = card(s)
    expect(c.state).toBe('pending')
    expect(c.apps.map(a => a.tier)).toEqual(['full', 'read'])
    expect(c.missing[0]!.howToGrant).toBe('Turn it on.')

    reduce(s, b.computerAccessResolved('r1', ['com.apple.Notes']))
    expect(card(s).state).toBe('resolved')
    expect(card(s).approved).toEqual(['com.apple.Notes'])
    expect(s.pendingComputerAccess).toBeNull()
    expect(s.meta.status).toBe('working')
  })

  it('a card resolved by an interrupt, or left open by the turn, is cancelled', () => {
    const { b, s } = setup()
    reduceAll(s, [b.computerAccessRequested('r1', 't1', '', APPS, []), b.computerAccessResolved('r1', [], 'interrupt')])
    expect(card(s).state).toBe('cancelled')

    const other = setup()
    reduce(other.s, other.b.computerAccessRequested('r2', 't2', '', APPS, []))
    settleTurn(other.s)
    expect(card(other.s).state).toBe('cancelled')
    expect(other.s.pendingComputerAccess).toBeNull()
  })

  it('tracks the indicator, which a turn end turns off even if the event is missed', () => {
    const { b, s } = setup()
    reduce(s, b.computerUseState(true, 'Clicking in Notes', [{ appId: 'com.apple.Notes', appName: 'Notes', tier: ComputerAccessTier.FULL }]))
    expect(s.computerUse).toEqual({ controlling: true, action: 'Clicking in Notes', grants: [{ id: 'com.apple.Notes', name: 'Notes', tier: 'full' }] })
    reduce(s, b.turnFinished('success'))
    expect(s.computerUse?.controlling).toBe(false)
    expect(s.computerUse?.grants).toHaveLength(1)
  })

  it('keeps the screenshots of computer tools for the row', () => {
    const { b, s } = setup()
    reduceAll(s, [
      b.toolStarted('t1', 'ComputerScreenshot', { app: 'Notes' }, 'Screenshot of Notes'),
      b.toolFinishedWithImage('t1', 'Screenshot of Notes', new Uint8Array([1, 2, 3]), 'image/jpeg')
    ])
    const t = s.items.find(i => i.kind === 'tool') as ToolItem
    expect(t.images).toEqual([{ url: 'data:image/jpeg;base64,AQID' }])
    expect(t.output).toBe('Screenshot of Notes')
  })

  it('maps profiles both ways, and an unclear tier is blocked', () => {
    const p = profileVM(create(ComputerUseProfileSchema, { id: 'custom', name: 'Custom', browsers: ComputerAccessTier.CLICK, other: ComputerAccessTier.FULL, liftedBlocks: ['keychain'], allowSecureTyping: true, appTiers: [{ appId: 'a', tier: ComputerAccessTier.READ }] }))
    expect(p.browsers).toBe('click')
    expect(p.terminals).toBe('blocked')
    expect(p.apps).toEqual([{ id: 'a', name: '', tier: 'read' }])
    const m = profileMessage(p)
    expect(m.terminals).toBe(ComputerAccessTier.BLOCKED)
    expect(m.liftedBlocks).toEqual(['keychain'])
    expect(m.allowSecureTyping).toBe(true)
  })

  it('ignores an event emptied by the relay', () => {
    const { b, s } = setup()
    const ev = b.computerUseState(true, 'x', [])
    ev.event = { case: undefined }
    reduce(s, ev)
    expect(s.computerUse).toBeUndefined()
  })
})

describe('computer use in the mock transport', () => {
  it('starts on Strict, and the custom profile is kept', async () => {
    const t = new MockTransport()
    const st = await t.getComputerUseStatus()
    expect(st.activeProfileId).toBe('strict')
    expect(st.profiles.map(p => p.id)).toEqual(['strict', 'standard', 'full', 'custom'])
    const custom = { ...st.profiles[3]!, browsers: 'click' as const, lifted: ['1password'] }
    const next = await t.setComputerUseProfile('custom', custom)
    expect(next.activeProfileId).toBe('custom')
    expect(next.profiles[3]!.lifted).toEqual(['1password'])
    expect(next.profiles[0]!.browsers).toBe('read')
  })

  it('is off by default, and the switch is remembered', async () => {
    const t = new MockTransport()
    expect((await t.getComputerUseStatus()).enabled).toBe(false)
    expect((await t.setComputerUseEnabled(true)).enabled).toBe(true)
    expect((await t.getComputerUseStatus()).enabled).toBe(true)
  })
})

describe('labels', () => {
  it('names the group', () => {
    expect(computerGroupLabel(1)).toBe('used the computer')
    expect(computerGroupLabel(3)).toBe('took 3 computer steps')
  })
})
