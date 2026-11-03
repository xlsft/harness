import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { SendUserMessageRequest_Delivery, UsageLimit_State, type SessionEvent } from '~/gen/harness/agent/v1/agent_pb'
import { applyLiveInfo, createSessionState, reduce, reduceAll } from '~/lib/session/reducer'
import { EventBuilder, sessionInfo } from '~/lib/transport/events'
import { MockTransport } from '~/lib/transport/mock'

const T0 = Date.parse('2026-11-03T12:00:00Z')
const limit = { providerId: 'anthropic', providerName: 'Anthropic', windowLabel: '5h', resetsAt: '2026-11-03T23:00:00Z' }

describe('usage limit', () => {
  it('keeps the latest state of the usage-limit events', () => {
    const b = new EventBuilder('s1', T0)
    b.user('u1', 'go')
    b.turnStarted('u1', 'claude')
    b.usageLimit({ state: 'near', usedPercent: 91, ...limit })
    const s = createSessionState('s1')
    reduceAll(s, b.events)
    expect(s.usageLimit).toEqual({ state: 'near', providerName: 'Anthropic', windowLabel: '5h', usedPercent: 91, resetsAt: limit.resetsAt, balanceText: '', wrappedUp: false, interrupted: false, autoContinue: false })

    b.usageLimit({ state: 'wrap_up', usedPercent: 98, ...limit })
    b.usageLimit({ state: 'wrap_up', usedPercent: 98, wrappedUp: true, ...limit })
    b.turnFinished('success')
    reduceAll(s, b.events)
    expect(s.usageLimit?.state).toBe('wrap_up')
    expect(s.usageLimit?.wrappedUp).toBe(true)

    // The next turn no longer counts as wrapped up; REACHED carries no numbers when they were hidden.
    b.user('u2', 'more')
    b.turnStarted('u2', 'claude')
    reduceAll(s, b.events)
    expect(s.usageLimit).toMatchObject({ state: 'wrap_up', wrappedUp: false })
    b.usageLimit({ state: 'reached', providerId: 'team-openai' })
    reduceAll(s, b.events)
    expect(s.usageLimit).toEqual({ state: 'reached', providerName: 'team-openai', windowLabel: '', usedPercent: 0, resetsAt: '', balanceText: '', wrappedUp: false, interrupted: false, autoContinue: false })
    b.usageLimit({ state: 'cleared', providerName: 'Team OpenAI' })
    reduceAll(s, b.events)
    expect(s.usageLimit?.state).toBe('cleared')
  })

  it('takes the state from SessionInfo, without forgetting it when an update has none', () => {
    const b = new EventBuilder('s1', T0)
    b.sessionUpdated(sessionInfo({ id: 's1', usageLimit: { state: UsageLimit_State.NEAR, providerName: 'OpenRouter', windowLabel: 'credits', balanceText: '$0.80 left' } }))
    const s = createSessionState('s1')
    reduceAll(s, b.events)
    expect(s.usageLimit).toMatchObject({ state: 'near', providerName: 'OpenRouter', balanceText: '$0.80 left' })
    reduce(s, b.sessionUpdated(sessionInfo({ id: 's1' })))
    expect(s.usageLimit?.state).toBe('near')
    applyLiveInfo(s, sessionInfo({ id: 's1', usageLimit: { state: UsageLimit_State.REACHED, providerName: 'OpenRouter' } }))
    expect(s.usageLimit?.state).toBe('reached')
    // An unknown state (a newer client) is ignored.
    reduce(s, b.usageLimit({ state: 'near' }))
    const ev = b.usageLimit({ state: 'near' })
    if (ev.event.case === 'usageLimit') ev.event.value.state = 99 as UsageLimit_State
    reduce(s, ev)
    expect(s.usageLimit?.state).toBe('near')
  })

  it('mock: a message about the limit walks NEAR, WRAP_UP, and ends wrapped up', async () => {
    const t = new MockTransport({ tick: 0 })
    const info = await t.createSession({ cwd: '/Users/xlsft/Projects/xlsft/harness' } as never)
    const events: SessionEvent[] = []
    t.subscribe(info.id, 0, { onEvent: e => events.push(e) } as never)
    await Promise.resolve()
    await t.sendUserMessage(info.id, 'u1', 'finish before the limit', SendUserMessageRequest_Delivery.QUEUE)
    const states = events.flatMap(e => e.event.case === 'usageLimit' ? [[UsageLimit_State[e.event.value.state], e.event.value.wrappedUp]] : [])
    expect(states).toEqual([['NEAR', false], ['WRAP_UP', false], ['WRAP_UP', true]])
    // wrapped_up comes right before the turn's end.
    const last = events.at(-1)!
    expect(last.event.case).toBe('turnFinished')
    expect(events.at(-2)!.event.case).toBe('usageLimit')
    const s = createSessionState(info.id)
    reduceAll(s, events)
    expect(s.usageLimit).toMatchObject({ state: 'wrap_up', providerName: 'Anthropic', windowLabel: '5h', usedPercent: 98, wrappedUp: true })
    expect(s.usageLimit?.resetsAt).toMatch(/Z$/)
  })
})

describe('usage limit: held messages (mock)', () => {
  const NOW = SendUserMessageRequest_Delivery.NOW
  const QUEUE = SendUserMessageRequest_Delivery.QUEUE

  async function reached() {
    const t = new MockTransport({ tick: 0, now: () => Date.now() })
    const info = await t.createSession({ cwd: '/Users/xlsft/Projects/xlsft/harness', model: 'glm-4.6' } as never)
    const s = createSessionState(info.id)
    t.subscribe(info.id, 0, { onEvent: (e: SessionEvent) => reduce(s, e) } as never)
    await t.sendUserMessage(info.id, 'u1', 'limit reached please', QUEUE)
    return { t, s, id: info.id }
  }

  beforeEach(() => {
    vi.useFakeTimers()
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('drives REACHED with a reset 30 s away and holds later messages', async () => {
    const { t, s, id } = await reached()
    expect(s.usageLimit).toMatchObject({ state: 'reached', interrupted: true, autoContinue: false })
    expect(Date.parse(s.usageLimit!.resetsAt) - Date.now()).toBeLessThanOrEqual(30_000)
    expect(s.meta.status).toBe('idle')
    await t.sendUserMessage(id, 'u2', 'second', QUEUE)
    await t.sendUserMessage(id, 'u3', 'third', QUEUE)
    expect(s.queue.map(q => [q.id, q.held])).toEqual([['u2', true], ['u3', true]])
    // Send now: still refused, still held, not duplicated.
    await t.sendUserMessage(id, 'u2', 'second', NOW)
    expect(s.queue.map(q => q.id)).toEqual(['u2', 'u3'])
    expect(await t.removeQueuedMessage(id, 'u3')).toBe(true)
    expect(s.queue.map(q => q.id)).toEqual(['u2'])
  })

  it('releases held messages in order at the reset, with auto-continue off', async () => {
    const { t, s, id } = await reached()
    await t.sendUserMessage(id, 'u2', 'second', QUEUE)
    await t.sendUserMessage(id, 'u3', 'third', QUEUE)
    vi.advanceTimersByTime(30_000)
    expect(s.usageLimit?.state).toBe('cleared')
    expect(s.queue).toEqual([])
    expect(s.delivered).toMatchObject({ u2: 'u2', u3: 'u2' })
  })

  it('releases on a switch to another provider, not to a model of the same one', async () => {
    const { t, s, id } = await reached()
    await t.sendUserMessage(id, 'u2', 'second', QUEUE)
    await t.setModel(id, 'qwen3-coder') // OpenRouter too
    expect(s.usageLimit?.state).toBe('reached')
    expect(s.queue).toHaveLength(1)
    await t.setModel(id, 'claude-sonnet-5-5')
    expect(s.usageLimit).toMatchObject({ state: 'cleared', providerName: 'Anthropic' })
    expect(s.queue).toEqual([])
    expect(s.delivered).toMatchObject({ u2: 'u2' })
  })

  it('continues a cut-off turn at the reset only with auto-continue on', async () => {
    const off = await reached()
    vi.advanceTimersByTime(30_000)
    expect(off.s.usageLimit).toMatchObject({ state: 'cleared', interrupted: true })
    expect(off.s.turnStartedAt).toBeFalsy()

    const on = await reached()
    await on.t.setAutoContinue(on.id, true)
    expect(on.s.usageLimit).toMatchObject({ state: 'reached', autoContinue: true, interrupted: true })
    expect(on.s.meta.autoContinue).toBe(true)
    const assistants = () => on.s.items.filter(i => i.kind === 'assistant').length
    const before = assistants()
    vi.advanceTimersByTime(30_000)
    expect(on.s.usageLimit).toMatchObject({ state: 'cleared', interrupted: false })
    expect(assistants()).toBeGreaterThan(before)
  })
})
