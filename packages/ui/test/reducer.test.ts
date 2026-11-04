import { describe, expect, it } from 'vitest'
import { RespondToPermissionRequest_Decision, RespondToPlanRequest_Decision } from '~/gen/harness/agent/v1/agent_pb'
import { createSessionState, reduce, reduceAll, sessionMeta } from '~/lib/session/reducer'
import type { AssistantItem, PermissionItem, PlanItem, ToolItem } from '~/lib/session/types'
import { EventBuilder, sessionInfo } from '~/lib/transport/events'
import { MAIN_SESSION, mainHistory } from '~/lib/transport/scenario'

const T0 = Date.parse('2026-11-02T12:00:00Z')

function setup() {
  const b = new EventBuilder('s1', T0)
  const s = createSessionState('s1')
  return { b, s }
}

describe('changes the model makes to the session', () => {
  it('reach the chips through SessionUpdated, and the transcript as a notice', () => {
    const { b, s } = setup()
    b.sessionUpdated(sessionInfo({ id: 's1', title: 'Renamed', model: 'other-model', effort: 'high', keepAwake: true }))
    b.notice('info', 'session_model_changed', 'Model changed to Other by the agent.')
    reduceAll(s, b.events)
    expect(s.meta).toMatchObject({ title: 'Renamed', model: 'other-model', effort: 'high', keepAwake: true })
    expect(s.items.at(-1)).toMatchObject({ kind: 'notice', noticeKind: 'session_model_changed', text: 'Model changed to Other by the agent.' })
  })
})

describe('assistant deltas', () => {
  it('merges text and thinking deltas into one streaming message, then the final message replaces it', () => {
    const { b, s } = setup()
    b.user('u1', 'hi')
    b.turnStarted('u1', 'glm-4.6')
    b.delta('m1', 0, 'thinking', 'Let me ')
    b.delta('m1', 0, 'thinking', 'think.')
    b.wait(12_000).delta('m1', 1, 'text', 'Hello ')
    b.delta('m1', 1, 'text', 'world')
    reduceAll(s, b.events)

    const msg = s.items.find(i => i.kind === 'assistant') as AssistantItem
    expect(msg.streaming).toBe(true)
    expect(msg.blocks.map(x => [x.type, x.text])).toEqual([['thinking', 'Let me think.'], ['text', 'Hello world']])
    expect(msg.blocks[0]!.durationMs).toBe(12_000)
    expect(s.meta.status).toBe('working')

    b.assistant('m1', 'glm-4.6', [{ thinking: 'Let me think.' }, { text: 'Hello world!' }], { output: 42 })
    reduce(s, b.events[b.events.length - 1]!)
    const final = s.items.filter(i => i.kind === 'assistant') as AssistantItem[]
    expect(final).toHaveLength(1)
    expect(final[0]!.streaming).toBe(false)
    expect(final[0]!.blocks[1]!.text).toBe('Hello world!')
    // The thinking time measured from the deltas survives the final message.
    expect(final[0]!.blocks[0]!.durationMs).toBe(12_000)
    expect(s.turnTokens).toBe(42)
  })

  it('never deduplicates deltas by seq, but skips replayed durable events', () => {
    const { b, s } = setup()
    b.user('u1', 'hi')
    b.delta('m1', 0, 'text', 'a')
    b.delta('m1', 0, 'text', 'b')
    reduceAll(s, b.events)
    // Deltas share the seq of the previous durable event and must all apply.
    expect((s.items[1] as AssistantItem).blocks[0]!.text).toBe('ab')
    // A replay of the durable user message is ignored.
    reduce(s, b.events[0]!)
    expect(s.items.filter(i => i.kind === 'user')).toHaveLength(1)
  })
})

describe('tool calls', () => {
  it('reads auto mode verdicts: allowed ones stay on the tool, blocked ones become a readable notice', () => {
    const { b, s } = setup()
    b.assistant('m1', 'glm-4.6', [{ toolUse: { id: 't1', name: 'Edit', input: { file_path: 'a.go' } } }])
    b.toolStarted('t1', 'Edit', { file_path: 'a.go' }, 'a.go')
    b.notice('info', 'auto_decision', { toolUseId: 't1', toolName: 'Edit', decision: 'allow', provider: 'local-rules', reason: 'inside the workspace', text: 'x' })
    b.notice('warning', 'auto_decision', { toolUseId: 't2', toolName: 'Bash', decision: 'deny', provider: 'rules', reason: 'shuts the machine down', text: 'y' })
    reduceAll(s, b.events)

    const tool = s.items.find(i => i.kind === 'tool') as ToolItem
    expect(tool.autoDecision).toEqual({ decision: 'allow', reason: 'inside the workspace', provider: 'local-rules' })
    const notices = s.items.filter(i => i.kind === 'notice') as { text: string }[]
    expect(notices.map(n => n.text)).toEqual(['Auto mode blocked Bash: shuts the machine down'])
  })

  it('pairs ToolCallStarted and ToolCallFinished by tool_use_id and groups parallel calls', () => {
    const { b, s } = setup()
    b.assistant('m1', 'glm-4.6', [
      { toolUse: { id: 't1', name: 'Read', input: { file_path: 'a.ts' } } },
      { toolUse: { id: 't2', name: 'Bash', input: { command: 'ls' } } }
    ])
    b.toolStarted('t1', 'Read', { file_path: 'a.ts' }, 'a.ts')
    b.toolStarted('t2', 'Bash', { command: 'ls' }, 'ls')
    b.toolFinished('t2', { output: 'x', isError: true, durationMs: 400 })
    b.toolFinished('t1', { result: { file: { numLines: 12 } }, summary: '12 lines', durationMs: 5 })
    reduceAll(s, b.events)

    const tools = s.items.filter(i => i.kind === 'tool') as ToolItem[]
    expect(tools.map(t => [t.id, t.status])).toEqual([['t1', 'ok'], ['t2', 'error']])
    expect(tools[0]!.result).toEqual({ file: { numLines: 12 } })
    expect(tools[1]!.output).toBe('x')
    expect(tools[1]!.durationMs).toBe(400)
    // Both came from one assistant message: they render as one group. A tool-only message adds no text item.
    expect(tools.every(t => t.grouped && t.messageId === 'm1')).toBe(true)
    expect(s.items.some(i => i.kind === 'assistant')).toBe(false)
  })

  it('a single tool call is not grouped; an interrupted turn stops running tools', () => {
    const { b, s } = setup()
    b.turnStarted('u1', 'glm-4.6')
    b.assistant('m1', 'glm-4.6', [{ text: 'Running it.' }, { toolUse: { id: 't1', name: 'Bash', input: { command: 'sleep 9' } } }])
    b.toolStarted('t1', 'Bash', { command: 'sleep 9' }, 'sleep 9')
    b.turnFinished('interrupted')
    reduceAll(s, b.events)
    const tool = s.items.find(i => i.kind === 'tool') as ToolItem
    expect(tool.grouped).toBe(false)
    expect(tool.status).toBe('interrupted')
    expect(s.meta.status).toBe('idle')
  })
})

describe('tool titles', () => {
  it('sets the title on an existing tool call, before or after its result', () => {
    const { b, s } = setup()
    b.assistant('m1', 'glm-4.6', [
      { toolUse: { id: 't1', name: 'Bash', input: { command: 'pnpm test' } } },
      { toolUse: { id: 't2', name: 'Bash', input: { command: 'ls' } } }
    ])
    b.toolStarted('t1', 'Bash', { command: 'pnpm test' }, 'pnpm test')
    b.toolStarted('t2', 'Bash', { command: 'ls' }, 'ls')
    b.toolTitle('t1', 'unit tests')
    b.toolFinished('t2', { output: 'a\nb', summary: 'exit 0' })
    b.toolTitle('t2', 'project files')
    b.toolTitle('t3', '')
    reduceAll(s, b.events)
    const tools = s.items.filter(i => i.kind === 'tool') as ToolItem[]
    expect(tools.map(t => [t.id, t.title, t.status])).toEqual([['t1', 'unit tests', 'running'], ['t2', 'project files', 'ok']])
    expect(s.toolTitles).toEqual({})
  })

  it('keeps a title that arrives first and applies it when the tool call appears', () => {
    const { b, s } = setup()
    b.toolTitle('t1', 'page script on localhost')
    reduceAll(s, b.events)
    expect(s.items.filter(i => i.kind === 'tool')).toHaveLength(0)
    expect(s.toolTitles).toEqual({ t1: 'page script on localhost' })

    reduce(s, b.toolStarted('t1', 'Bash', { command: 'node script.js' }, 'node script.js'))
    const tool = s.items.find(i => i.kind === 'tool') as ToolItem
    expect(tool.title).toBe('page script on localhost')
    expect(s.toolTitles).toEqual({})
  })

  it('leaves calls without a ToolTitle untitled', () => {
    const { b, s } = setup()
    b.toolStarted('t1', 'Read', { file_path: 'a.ts' }, 'a.ts')
    reduceAll(s, b.events)
    expect((s.items.find(i => i.kind === 'tool') as ToolItem).title).toBeUndefined()
  })

  it('titles the Bash calls of the design scenario', () => {
    const s = reduceAll(createSessionState(MAIN_SESSION), mainHistory(Date.now()))
    const buf = s.items.find(i => i.kind === 'tool' && i.id === 't-buf') as ToolItem
    expect(buf.title).toBe('proto code generation')
  })
})

describe('permission, question and plan cards', () => {
  it('shows a pending permission card and resolves it on PermissionResolved', () => {
    const { b, s } = setup()
    b.turnStarted('u1', 'glm-4.6')
    b.permissionRequest('r1', 't1', 'Bash', { command: 'rm -rf .output' }, ['Bash(rm -rf .output)'], 'clean build')
    reduceAll(s, b.events)
    const card = s.items.find(i => i.kind === 'permission') as PermissionItem
    expect(card.state).toBe('pending')
    expect(card.suggestedRules).toEqual(['Bash(rm -rf .output)'])
    expect(s.meta.status).toBe('needs_input')
    expect(s.pendingPermission).toBe('r1')

    reduce(s, b.permissionResolved('r1', RespondToPermissionRequest_Decision.ALLOW_ALWAYS, 'relay:web'))
    expect(card.state).toBe('allowed_always')
    expect(card.resolvedBy).toBe('relay:web')
    expect(s.pendingPermission).toBeNull()
    expect(s.meta.status).toBe('working')
  })

  it('resolves question and plan cards', () => {
    const { b, s } = setup()
    b.turnStarted('u1', 'glm-4.6')
    b.question('q1', 't1', [{ question: 'Where?', header: 'Keys', options: [{ label: 'A', description: '' }] }])
    b.planProposed('p1', 't2', '## Plan', '/plans/p.md')
    reduceAll(s, b.events)
    expect(s.meta.status).toBe('needs_input')
    reduce(s, b.questionResolved('q1'))
    expect(s.meta.status).toBe('needs_input')
    reduce(s, b.planResolved('p1', RespondToPlanRequest_Decision.APPROVE_MANUAL))
    expect((s.items.find(i => i.kind === 'plan') as PlanItem).state).toBe('approved_manual')
    expect(s.meta.status).toBe('working')
  })
})

describe('queue, todos, tasks and the main scenario', () => {
  it('tracks queue, todos and background tasks', () => {
    const { b, s } = setup()
    b.queue([{ uuid: 'q1', text: 'next' }])
    b.todos([{ content: 'Do it', activeForm: 'Doing it', status: 'in_progress' }])
    b.taskStarted('k1', 't1', 'pnpm dev', 'dev')
    b.taskOutput('k1', 'ready')
    b.taskFinished('k1', 1, 6000)
    reduceAll(s, b.events)
    expect(s.queue).toEqual([{ id: 'q1', text: 'next' }])
    expect(s.todos[0]!.status).toBe('in_progress')
    expect(s.tasks.k1).toMatchObject({ state: 'failed', exitCode: 1, durationMs: 6000, tail: 'ready' })
    reduce(s, b.queue([]))
    expect(s.queue).toEqual([])
  })

  it('replays the design scenario into the Main screen state', () => {
    const now = Date.now()
    const s = reduceAll(createSessionState(MAIN_SESSION), mainHistory(now))
    expect(s.meta.title).toBe('M0: LLM gateway in core')
    expect(s.meta.status).toBe('working')
    expect(s.contextTokens).toBe(62_000)
    expect(s.costUsd).toBeCloseTo(0.84)
    expect(s.turnTokens).toBe(8400)
    const last = s.items[s.items.length - 1]!
    expect(last.kind).toBe('task')
    const group = (s.items.filter(i => i.kind === 'tool') as ToolItem[]).filter(t => t.messageId === 'm3a')
    expect(group.map(t => t.name)).toEqual(['Read', 'Bash', 'Edit', 'Task'])
  })
})

describe('sessions of other machines', () => {
  it('reads the origin of a remote session from SessionInfo and SessionUpdated', () => {
    const local = sessionMeta(sessionInfo({ id: 'l1', deviceName: 'macbook' }))
    expect(local).toMatchObject({ remote: false, deviceOnline: null, remoteControllable: false })
    const remote = sessionMeta(sessionInfo({ id: 'r1', remote: true, deviceName: 'workstation', deviceOnline: false }))
    expect(remote).toMatchObject({ remote: true, deviceName: 'workstation', deviceOnline: false, remoteControllable: false })

    const s = createSessionState('r1', remote)
    const b = new EventBuilder('r1', T0)
    b.sessionUpdated(sessionInfo({ id: 'r1', title: 'Relay', remote: true, deviceName: 'workstation', deviceOnline: true, remoteControllable: true }))
    reduceAll(s, b.events)
    expect(s.meta).toMatchObject({ title: 'Relay', remote: true, deviceOnline: true, remoteControllable: true })
  })
})
