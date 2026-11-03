import { describe, expect, it } from 'vitest'
import { BackgroundReason, SendUserMessageRequest_Delivery, type SessionEvent } from '~/gen/harness/agent/v1/agent_pb'
import { backgroundReasonName, backgroundReasonPb, createSessionState, reduceAll } from '~/lib/session/reducer'
import type { ToolItem, UserItem } from '~/lib/session/types'
import { EventBuilder } from '~/lib/transport/events'
import { MockTransport } from '~/lib/transport/mock'

const T0 = Date.parse('2026-11-03T12:00:00Z')

function bashCall(b: EventBuilder, id: string) {
  b.assistant('m-' + id, 'glm-4.6', [{ toolUse: { id, name: 'Bash', input: { command: 'pnpm dev' } } }])
  b.toolStarted(id, 'Bash', { command: 'pnpm dev' }, 'pnpm dev')
}

describe('backgrounded tool calls', () => {
  it('puts the background marker on the tool item, for every reason', () => {
    const b = new EventBuilder('s1', T0)
    const reasons = [BackgroundReason.USER, BackgroundReason.QUEUED_MESSAGE, BackgroundReason.AGENT, BackgroundReason.AUTO]
    reasons.forEach((reason, i) => {
      bashCall(b, `t${i}`)
      b.toolFinished(`t${i}`, { output: `note b${i}`, summary: `Moved to background task b${i}`, background: { taskId: `b${i}`, reason } })
    })
    bashCall(b, 'plain')
    b.toolFinished('plain', { output: 'ok', summary: 'Exit code 0' })
    const s = createSessionState('s1')
    reduceAll(s, b.events)
    const tools = s.items.filter(i => i.kind === 'tool') as ToolItem[]
    expect(tools.map(t => [t.id, t.status, t.background])).toEqual([
      ['t0', 'ok', { taskId: 'b0', reason: 'user' }],
      ['t1', 'ok', { taskId: 'b1', reason: 'queued_message' }],
      ['t2', 'ok', { taskId: 'b2', reason: 'agent' }],
      ['t3', 'ok', { taskId: 'b3', reason: 'auto' }],
      ['plain', 'ok', undefined]
    ])
  })

  it('maps reasons both ways; an unrecorded reason reads as agent', () => {
    for (const r of ['user', 'queued_message', 'agent', 'auto'] as const) expect(backgroundReasonName(backgroundReasonPb(r))).toBe(r)
    expect(backgroundReasonName(BackgroundReason.UNSPECIFIED)).toBe('agent')
  })
})

describe('mock: run in background', () => {
  async function running(text: string) {
    const t = new MockTransport({ tick: 0 })
    const info = await t.createSession({ cwd: '/Users/xlsft/Projects/xlsft/harness' } as never)
    const events: SessionEvent[] = []
    t.subscribe(info.id, 0, { onEvent: e => events.push(e) } as never)
    await Promise.resolve()
    await t.sendUserMessage(info.id, 'u1', text, SendUserMessageRequest_Delivery.QUEUE)
    const s = () => {
      const st = createSessionState(info.id)
      reduceAll(st, events)
      return st
    }
    const bash = s().items.find(i => i.kind === 'tool') as ToolItem
    return { t, id: info.id, s, bash }
  }

  it('backgrounds the running Bash by hand: note, marker and a background task', async () => {
    const { t, id, s, bash } = await running('run the long tests')
    expect(bash.status).toBe('running')
    expect(await t.backgroundToolCall(id, 'nope', 'user')).toBe(false)
    expect(await t.backgroundToolCall(id, bash.id, 'user')).toBe(true)
    const st = s()
    const tool = st.items.find(i => i.kind === 'tool') as ToolItem
    expect(tool.status).toBe('ok')
    expect(tool.background?.reason).toBe('user')
    expect(tool.output).toContain(tool.background!.taskId)
    expect(tool.output).toContain('The user moved this command')
    expect(st.tasks[tool.background!.taskId]?.toolUseId).toBe(bash.id)
    expect(st.meta.status).toBe('idle')
    // Already backgrounded.
    expect(await t.backgroundToolCall(id, bash.id, 'user')).toBe(false)
  })

  it('a message sent now backgrounds the running Bash and is read in the same turn', async () => {
    const { t, id, s, bash } = await running('run the long tests')
    await t.sendUserMessage(id, 'q1', 'also check the lint', SendUserMessageRequest_Delivery.NOW)
    const st = s()
    const tool = st.items.find(i => i.kind === 'tool') as ToolItem
    expect(tool.id).toBe(bash.id)
    expect(tool.background?.reason).toBe('queued_message')
    expect(tool.output).toContain('so that a message the user sent')
    const users = st.items.filter(i => i.kind === 'user') as UserItem[]
    expect(users.map(u => u.text)).toEqual(['run the long tests', 'also check the lint'])
    expect(st.items.filter(i => i.kind === 'turn')).toHaveLength(1)
    expect(st.meta.status).toBe('idle')
  })
})
