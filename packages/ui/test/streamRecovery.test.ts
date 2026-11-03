// A session must never stay "working" when nothing runs on the client: a stream that dies silently, a client
// restarted mid-turn, an interrupt with nothing to stop, a stream that misses the end of a turn.

import { createPinia, setActivePinia } from 'pinia'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { SessionStatus, type SessionEvent, type SessionInfo } from '~/gen/harness/agent/v1/agent_pb'
import { createSessionState, sessionMeta } from '~/lib/session/reducer'
import { EventBuilder, sessionInfo } from '~/lib/transport/events'
import { MockTransport } from '~/lib/transport/mock'
import type { SubscribeHandlers } from '~/lib/transport/types'
import { INTERRUPT_SETTLE_MS, RECONCILE_MS, STALL_MS, useSessionStore } from '~/stores/sessions'

vi.mock('vue', () => ({ markRaw: <T>(v: T) => v, toRaw: <T>(v: T) => v }))

const ID = 's1'
const T0 = Date.parse('2026-11-03T12:00:00Z')

interface Stream {
  afterSeq: number
  h: SubscribeHandlers
  closed: boolean
}

// A client whose streams the test drives by hand.
class FakeClient extends MockTransport {
  streams: Stream[] = []
  status = SessionStatus.WORKING
  live = true
  interruptReply: { idle: boolean } | Error = { idle: false }
  resumed = 0

  info(status = this.status): SessionInfo {
    return sessionInfo({ id: ID, cwd: '/p', title: 'T', model: 'm', status })
  }

  override subscribe(_id: string, afterSeq: number, h: SubscribeHandlers) {
    const s: Stream = { afterSeq, h, closed: false }
    this.streams.push(s)
    return () => {
      s.closed = true
    }
  }

  override async listSessions() {
    return [this.info()]
  }

  override async createSession() {
    this.resumed += 1
    this.live = true
    return this.info()
  }

  override async interrupt() {
    if (this.interruptReply instanceof Error) throw this.interruptReply
    return this.interruptReply
  }

  get last() {
    return this.streams[this.streams.length - 1]!
  }

  // A stream of log `logId`: the log id, the replayed events after its afterSeq, the live session.
  serve(s: Stream, logId: string, history: SessionEvent[], status = this.status) {
    s.h.onControl?.({ logId })
    for (const ev of history) if (Number(ev.seq) > s.afterSeq) s.h.onEvent(ev)
    s.h.onControl?.({ logId, session: this.info(status) })
  }
}

function turn(b: EventBuilder, body = 'Команда `echo hello` выполнена,') {
  b.sessionUpdated(sessionInfo({ id: ID, cwd: '/p', title: 'T', model: 'm', status: SessionStatus.IDLE }))
  b.user('u1', 'run echo hello')
  b.turnStarted('u1', 'm')
  b.toolStarted('t1', 'Bash', { command: 'echo hello' }, 'Bash echo hello')
  b.toolFinished('t1', { output: 'hello' })
  b.delta('m2', 0, 'text', body)
}

function setup() {
  setActivePinia(createPinia())
  const store = useSessionStore()
  const client = new FakeClient({ tick: 0 })
  store.transport = client
  store.sessions[ID] = createSessionState(ID, sessionMeta(client.info(SessionStatus.IDLE)))
  store.order = [ID]
  return { store, client }
}

const s = (store: ReturnType<typeof useSessionStore>) => store.sessions[ID]!

beforeEach(() => {
  vi.useFakeTimers()
})
afterEach(() => {
  vi.useRealTimers()
})

describe('stream liveness', () => {
  it('reopens a stream whose tail never arrived and catches up from lastSeq', async () => {
    const { store, client } = setup()
    store.open(ID)
    const b = new EventBuilder(ID, T0)
    turn(b)
    client.serve(client.last, 'log_a', b.events, SessionStatus.WORKING)
    expect(store.isBusy(ID)).toBe(true)
    const seen = s(store).lastSeq
    // The final message and TurnFinished are published, but this stream never delivers them.
    b.assistant('m2', 'm', [{ text: 'Команда `echo hello` выполнена, вывод: `hello`.' }])
    b.turnFinished('success')
    b.sessionUpdated(client.info(SessionStatus.IDLE))
    client.status = SessionStatus.IDLE

    vi.advanceTimersByTime(STALL_MS - 1000)
    expect(client.streams).toHaveLength(1)
    vi.advanceTimersByTime(2000)
    expect(client.streams).toHaveLength(2)
    expect(client.streams[0]!.closed).toBe(true)
    expect(client.last.afterSeq).toBe(seen)
    client.serve(client.last, 'log_a', b.events)
    expect(s(store).meta.status).toBe('idle')
    expect(s(store).turnStartedAt).toBe(0)
    const msg = s(store).items.find(i => i.kind === 'assistant')
    expect(msg).toMatchObject({ streaming: false, blocks: [{ text: 'Команда `echo hello` выполнена, вывод: `hello`.' }] })
  })

  it('keeps a stream that beats, and does not watch one that never sent control messages', () => {
    const { store, client } = setup()
    store.open(ID)
    client.serve(client.last, 'log_a', [])
    for (let i = 0; i < 5; i++) {
      vi.advanceTimersByTime(15_000)
      client.last.h.onControl?.({ logId: 'log_a' })
    }
    expect(client.streams).toHaveLength(1)

    // An older client: events only, no heartbeats; silence proves nothing.
    store.restartStream(ID)
    expect(client.streams).toHaveLength(2)
    vi.advanceTimersByTime(STALL_MS * 3)
    expect(client.streams).toHaveLength(2)
  })

  it('retries a broken stream with backoff and takes the live status after the replay', async () => {
    const { store, client } = setup()
    store.open(ID)
    const b = new EventBuilder(ID, T0)
    turn(b)
    client.serve(client.last, 'log_a', b.events, SessionStatus.WORKING)
    client.last.h.onError?.(new Error('[unavailable] Failed to fetch'))
    expect(s(store).meta.status).toBe('waiting_for_connection')
    vi.advanceTimersByTime(1000)
    expect(client.streams).toHaveLength(2)
    // The turn kept running meanwhile.
    client.serve(client.last, 'log_a', b.events, SessionStatus.WORKING)
    expect(s(store).meta.status).toBe('working')
  })
})

describe('client restarted mid-turn', () => {
  it('resumes the session, drops the old log and shows the lost turn interrupted', async () => {
    const { store, client } = setup()
    store.open(ID)
    const b = new EventBuilder(ID, T0)
    turn(b)
    client.serve(client.last, 'log_a', b.events, SessionStatus.WORKING)
    const oldSeq = s(store).lastSeq

    // The process goes away: the stream breaks, the new one finds the session not open.
    client.last.h.onError?.(new Error('[unavailable] connection refused'))
    vi.advanceTimersByTime(1000)
    client.status = SessionStatus.IDLE
    client.last.h.onError?.(new Error('[not_found] session s1 is not open; call CreateSession first'))
    await vi.waitFor(() => expect(client.resumed).toBe(1))
    // The resumed session is idle at once, before its stream says anything.
    expect(s(store).meta.status).toBe('idle')
    expect(s(store).turnStartedAt).toBe(0)
    expect(client.last.afterSeq).toBe(oldSeq)

    // The new process replays the transcript in a new log, with the lost turn ended as interrupted.
    const r = new EventBuilder(ID, T0)
    r.user('u1', 'run echo hello')
    r.turnStarted('u1', 'm')
    r.toolStarted('t1', 'Bash', { command: 'echo hello' }, 'Bash echo hello')
    r.toolFinished('t1', { output: 'hello' })
    r.turnFinished('interrupted')
    r.sessionUpdated(client.info(SessionStatus.IDLE))
    const stale = client.last
    stale.h.onControl?.({ logId: 'log_b' })
    expect(stale.closed).toBe(true)
    expect(client.last.afterSeq).toBe(0)
    client.serve(client.last, 'log_b', r.events)
    expect(s(store).logId).toBe('log_b')
    expect(s(store).items.filter(i => i.kind === 'user')).toHaveLength(1)
    expect(s(store).items.find(i => i.kind === 'turn')).toMatchObject({ result: 'interrupted' })
    expect(s(store).meta.status).toBe('idle')
  })

  it('trusts the live status over a replay that ends with an open turn', () => {
    const { store, client } = setup()
    store.open(ID)
    const b = new EventBuilder(ID, T0)
    turn(b)
    client.serve(client.last, 'log_a', b.events, SessionStatus.IDLE)
    expect(s(store).meta.status).toBe('idle')
    expect(s(store).turnStartedAt).toBe(0)
    expect(s(store).items.find(i => i.kind === 'assistant')).toMatchObject({ streaming: false })
  })
})

describe('Stop', () => {
  function busy() {
    const ctx = setup()
    ctx.store.open(ID)
    const b = new EventBuilder(ID, T0)
    turn(b)
    ctx.client.serve(ctx.client.last, 'log_a', b.events, SessionStatus.WORKING)
    return ctx
  }

  it('settles the turn when the client says nothing was running', async () => {
    const { store, client } = busy()
    client.interruptReply = { idle: true }
    await store.interrupt(ID)
    expect(s(store).meta.status).toBe('idle')
    expect(s(store).turnStartedAt).toBe(0)
  })

  it('settles the turn when the session is not live on the client', async () => {
    const { store, client } = busy()
    client.interruptReply = new Error('[not_found] session s1 is not open; call CreateSession first')
    await store.interrupt(ID)
    expect(s(store).meta.status).toBe('idle')
    expect(store.error).toBe('')
  })

  it('reopens the stream when the turn does not end after Stop', async () => {
    const { store, client } = busy()
    await store.interrupt(ID)
    expect(client.streams).toHaveLength(1)
    vi.advanceTimersByTime(INTERRUPT_SETTLE_MS + 10)
    expect(client.streams).toHaveLength(2)
    client.serve(client.last, 'log_a', [], SessionStatus.IDLE)
    expect(s(store).meta.status).toBe('idle')
  })
})

describe('list reconciliation', () => {
  it('reopens a stream that shows a running turn the client lists as idle', async () => {
    const { store, client } = setup()
    store.open(ID)
    const b = new EventBuilder(ID, T0)
    turn(b)
    client.serve(client.last, 'log_a', b.events, SessionStatus.WORKING)
    client.status = SessionStatus.IDLE
    await store.refreshList()
    expect(client.streams).toHaveLength(1)
    vi.advanceTimersByTime(RECONCILE_MS + 10)
    await store.refreshList()
    expect(client.streams).toHaveLength(2)
    client.serve(client.last, 'log_a', [])
    expect(s(store).meta.status).toBe('idle')
  })

  it('leaves a stream alone while the list agrees', async () => {
    const { store, client } = setup()
    store.open(ID)
    client.serve(client.last, 'log_a', [], SessionStatus.WORKING)
    for (let i = 0; i < 4; i++) {
      await store.refreshList()
      vi.advanceTimersByTime(RECONCILE_MS)
    }
    expect(client.streams).toHaveLength(1)
  })
})
