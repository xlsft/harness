import { describe, expect, it, vi } from 'vitest'
import { ComputerAccessTier } from '~/gen/harness/agent/v1/computer_use_pb'
import { hotkeyKeys } from '~/lib/session/demo'
import { createSessionState, reduce, reduceAll, settleTurn } from '~/lib/session/reducer'
import type { DemoSkillReviewItem, ToolItem } from '~/lib/session/types'
import { EventBuilder } from '~/lib/transport/events'
import { toolIcon, toolLabel } from '~/utils/tools'

vi.mock('vue', () => ({ markRaw: <T>(v: T) => v, toRaw: <T>(v: T) => v }))

const T0 = Date.parse('2026-11-07T12:00:00Z')

function setup() {
  return { b: new EventBuilder('s1', T0), s: createSessionState('s1') }
}
const card = (s: ReturnType<typeof createSessionState>) => s.items.find(i => i.kind === 'demoSkillReview') as DemoSkillReviewItem

describe('recording a demo in the session state', () => {
  it('tracks the recording indicator', () => {
    const { b, s } = setup()
    reduce(s, b.demoRecordingState(true, 'rec1', 3, [{ id: 'com.apple.Notes', name: 'Notes', tier: ComputerAccessTier.FULL }], 'Clicked "New Note" in Notes'))
    expect(s.demoRecording).toMatchObject({ recording: true, id: 'rec1', steps: 3, lastStep: 'Clicked "New Note" in Notes', hotkey: '⌃⌥⌘.' })
    expect(s.demoRecording!.apps).toEqual([{ id: 'com.apple.Notes', name: 'Notes', tier: 'full' }])
    reduce(s, b.demoRecordingState(false, 'rec1', 3, []))
    expect(s.demoRecording!.recording).toBe(false)
  })

  it('shows the review card, which blocks until the user answers', () => {
    const { b, s } = setup()
    reduce(s, b.demoSkillReviewRequested('sr1', 't1', 'rec1', 'file-expense', '---\nname: file-expense\n---\n', 'project', '/p/.harness/skills/file-expense/SKILL.md', '/h/.harness/skills/file-expense/SKILL.md'))
    expect(s.meta.status).toBe('needs_input')
    expect(s.pendingDemoReview).toBe('sr1')
    expect(card(s)).toMatchObject({ state: 'pending', scope: 'project', name: 'file-expense', paths: { project: '/p/.harness/skills/file-expense/SKILL.md', personal: '/h/.harness/skills/file-expense/SKILL.md' } })

    reduce(s, b.demoSkillReviewResolved('sr1', true, 'personal'))
    expect(card(s)).toMatchObject({ state: 'saved', scope: 'personal' })
    expect(s.pendingDemoReview).toBeNull()
    expect(s.meta.status).toBe('working')
  })

  it('a discarded card, one cut by an interrupt, and one left open by the turn', () => {
    const { b, s } = setup()
    reduceAll(s, [b.demoSkillReviewRequested('sr1', 't1', 'rec1', 'x', 'body'), b.demoSkillReviewResolved('sr1', false)])
    expect(card(s).state).toBe('discarded')

    const cut = setup()
    reduceAll(cut.s, [cut.b.demoSkillReviewRequested('sr2', 't2', 'rec1', 'x', 'body'), cut.b.demoSkillReviewResolved('sr2', false, 'project', 'interrupt')])
    expect(card(cut.s).state).toBe('cancelled')

    const open = setup()
    reduce(open.s, open.b.demoSkillReviewRequested('sr3', 't3', 'rec1', 'x', 'body'))
    settleTurn(open.s)
    expect(card(open.s).state).toBe('cancelled')
    expect(open.s.pendingDemoReview).toBeNull()
  })

  it('reads the hotkey for the key caps', () => {
    expect(hotkeyKeys('⌃⌥⌘.')).toBe('ctrl+alt+meta+.')
  })
})

describe('the iOS Simulator in the session state', () => {
  it('keeps the agent\'s latest action for the pane', () => {
    const { b, s } = setup()
    reduce(s, b.simulatorActivity('U1', 'Tap at 10, 20'))
    expect(s.simulatorActivity).toMatchObject({ udid: 'U1', action: 'Tap at 10, 20' })
  })

  it('labels the Simulator and demo tools', () => {
    const tool = (name: string, input: Record<string, unknown>) => ({ kind: 'tool', id: 't', name, input, status: 'ok', summary: '' }) as unknown as ToolItem
    expect(toolLabel(tool('SimulatorTap', { x: 10, y: 20 }))).toBe('10, 20')
    expect(toolLabel(tool('SimulatorInstall', { app_path: '/w/build/Debug-iphonesimulator/App.app' }))).toBe('App.app')
    expect(toolLabel(tool('SimulatorLaunch', { bundle_id: 'com.example.app' }))).toBe('com.example.app')
    expect(toolLabel(tool('SimulatorType', { text: 'hello\nworld' }))).toBe('"hello"')
    expect(toolLabel(tool('DemoSkillSave', { name: 'file-expense' }))).toBe('file-expense')
    expect(toolIcon(tool('SimulatorSwipe', {}))).toBe('phone')
  })
})
