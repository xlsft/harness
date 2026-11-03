import { describe, expect, it } from 'vitest'
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
    expect(s.usageLimit).toEqual({ state: 'near', providerName: 'Anthropic', windowLabel: '5h', usedPercent: 91, resetsAt: limit.resetsAt, balanceText: '', wrappedUp: false })

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
    expect(s.usageLimit).toEqual({ state: 'reached', providerName: 'team-openai', windowLabel: '', usedPercent: 0, resetsAt: '', balanceText: '', wrappedUp: false })
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
