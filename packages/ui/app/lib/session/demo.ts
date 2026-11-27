// Recording a demo into a skill (demo_recording.proto → view models). The client records, keeps the screenshots on
// this machine and writes the skill; this renders its state and sends the user's answers.
import type { DemoRecordingState, DemoSkillReviewRequested } from '~/gen/harness/agent/v1/demo_recording_pb'
import { tierName } from './computer'
import type { DemoRecordingVM, DemoSkillReviewItem } from './types'

export function demoRecordingVM(s: DemoRecordingState): DemoRecordingVM {
  return {
    recording: s.recording,
    id: s.recordingId,
    startedAt: s.startedAt,
    steps: s.steps,
    apps: s.apps.map(a => ({ id: a.id, name: a.name, tier: tierName(a.tier) })),
    hotkey: s.hotkey,
    lastStep: s.lastStep
  }
}

export function demoReviewItem(r: DemoSkillReviewRequested): DemoSkillReviewItem {
  return {
    kind: 'demoSkillReview',
    id: r.requestId,
    toolUseId: r.toolUseId,
    recordingId: r.recordingId,
    name: r.name,
    scope: r.scope === 'personal' ? 'personal' : 'project',
    content: r.content,
    paths: { project: r.projectPath, personal: r.personalPath },
    state: 'pending'
  }
}

// "⌃⌥⌘." as the client reports it; the HKeys form ("ctrl+alt+meta+.") for the bar.
export function hotkeyKeys(hotkey: string): string {
  const map: Record<string, string> = { '⌃': 'ctrl', '⌥': 'alt', '⌘': 'meta', '⇧': 'shift' }
  const mods: string[] = []
  let rest = ''
  for (const ch of hotkey) {
    if (map[ch]) mods.push(map[ch]!)
    else rest += ch
  }
  return [...mods, rest.toLowerCase()].filter(Boolean).join('+')
}
