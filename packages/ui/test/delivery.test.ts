import { createPinia, setActivePinia } from 'pinia'
import { describe, expect, it, vi } from 'vitest'
import { RewindRequest_Mode } from '~/gen/harness/agent/v1/agent_pb'
import { messageActions, pendingMessages } from '~/lib/session/delivery'
import { createSessionState, reduceAll, sessionMeta } from '~/lib/session/reducer'
import type { UserItem } from '~/lib/session/types'
import { EventBuilder, sessionInfo } from '~/lib/transport/events'
import { MockTransport } from '~/lib/transport/mock'
import { useSessionStore } from '~/stores/sessions'
import { formatAgo } from '~/utils/format'

vi.mock('vue', () => ({ markRaw: <T>(v: T) => v, toRaw: <T>(v: T) => v }))

const T0 = Date.parse('2026-11-02T12:00:00Z')
const flush = () => new Promise(r => setTimeout(r, 0))

describe('reducer: read receipts and rewinds', () => {
  it('marks the frontend ids a user message carries as read and keeps when the agent took it', () => {
    const s = createSessionState('s1')
    const b = new EventBuilder('s1', T0)
    b.queue([{ uuid: 'c-a', text: 'a', queuedAt: T0 - 5000 }, { uuid: 'c-b', text: 'b' }])
    reduceAll(s, b.events)
    expect(s.queue).toEqual([{ id: 'c-a', text: 'a', at: T0 - 5000 }, { id: 'c-b', text: 'b' }])

    const b2 = new EventBuilder('s1', T0 + 1000, b.seq)
    b2.user('t1', 'a\nb', ['c-a', 'c-b'])
    reduceAll(s, b2.events)
    const item = s.items.find(i => i.kind === 'user') as UserItem
    expect(item).toMatchObject({ id: 't1', at: T0 + 1000, clientIds: ['c-a', 'c-b'] })
    expect(s.delivered).toEqual({ 'c-a': 't1', 'c-b': 't1' })
    // The client's queue no longer holds them even before its next QueueChanged.
    expect(s.queue).toEqual([])
  })

  it('drops the rewound message and what follows on a conversation rewind, not on a code-only one', () => {
    const s = createSessionState('s1')
    const b = new EventBuilder('s1', T0)
    for (const [u, t, a] of [['u1', 'one', 'a1'], ['u2', 'two', 'a2']] as const) {
      b.user(u, t, ['c' + u])
      b.turnStarted(u, 'm')
      b.assistant(a, 'm', [{ text: 'answer ' + t }])
      b.turnFinished('success')
    }
    b.rewound('u2', RewindRequest_Mode.CODE, '', 1)
    reduceAll(s, b.events)
    expect(s.items.filter(i => i.kind === 'user').map(i => i.id)).toEqual(['u1', 'u2'])

    const b2 = new EventBuilder('s1', T0, b.seq)
    b2.rewound('u2', RewindRequest_Mode.CODE_AND_CONVERSATION, 'two', 1)
    reduceAll(s, b2.events)
    // The code-only marker came after the message, so it goes too; the new marker stays.
    expect(s.items.map(i => i.kind + ':' + i.id)).toEqual(['user:u1', 'assistant:a1', expect.stringMatching(/^turn:/), expect.stringMatching(/^rewound:/)])
    // The index follows the cut (later events still find items).
    expect(s.index['user:u2']).toBeUndefined()
    expect(s.index['assistant:a1']).toBe(1)
  })

  it('reads the parent of a sub-session', () => {
    expect(sessionMeta(sessionInfo({ id: 'c', parentSessionId: 'p' })).parentId).toBe('p')
    expect(createSessionState('x').meta.parentId).toBe('')
  })
})

describe('delivery states and actions', () => {
  it('orders handed-over and client-queued messages and numbers the waiting ones', () => {
    const s = createSessionState('s1')
    s.queue = [{ id: 'c1', text: 'in client', at: 100 }, { id: 'c2', text: 'read already' }]
    s.delivered = { c2: 'u9', o3: 'u10' }
    const out = pendingMessages(s, [
      { id: 'o1', text: 'flying', at: 10, state: 'sending' },
      { id: 'o2', text: 'accepted', at: 20, state: 'sent' },
      { id: 'c1', text: 'in client', at: 50, state: 'sent' },
      { id: 'o3', text: 'read', at: 30, state: 'sent' }
    ])
    expect(out.map(m => [m.id, m.delivery, m.where, m.position, m.at])).toEqual([
      ['o1', 'sending', 'outbox', 0, 10],
      ['o2', 'sent', 'outbox', 0, 20],
      ['c1', 'queued', 'client', 1, 100]
    ])
  })

  it('shows cancel and send now only on queued messages and keeps rewind for read ones', () => {
    const idle = { busy: false, readOnly: false }
    expect(messageActions('read', idle)).toMatchObject({ copy: 'enabled', rewind: 'enabled', fork: 'enabled', cancel: 'hidden', sendNow: 'hidden' })
    expect(messageActions('read', { busy: true, readOnly: false })).toMatchObject({ rewind: 'disabled', rewindHint: 'Stop the turn to rewind' })
    expect(messageActions('queued', { busy: true, readOnly: false })).toMatchObject({ rewind: 'disabled', rewindHint: 'Not sent yet', fork: 'enabled', cancel: 'enabled', sendNow: 'enabled' })
    expect(messageActions('sending', idle)).toMatchObject({ rewind: 'disabled', fork: 'disabled', cancel: 'hidden', sendNow: 'hidden' })
    expect(messageActions('sent', idle)).toMatchObject({ rewind: 'disabled', cancel: 'hidden' })
    expect(messageActions('read', { busy: false, readOnly: true })).toMatchObject({ copy: 'enabled', rewind: 'hidden', fork: 'hidden' })
  })

  it('formats relative times', () => {
    expect(formatAgo(10_000)).toBe('just now')
    expect(formatAgo(60_000)).toBe('1 minute ago')
    expect(formatAgo(3 * 60_000 + 5000)).toBe('3 minutes ago')
    expect(formatAgo(2 * 3_600_000)).toBe('2 hours ago')
    expect(formatAgo(26 * 3_600_000)).toBe('yesterday')
    expect(formatAgo(3 * 86_400_000)).toBe('3 days ago')
  })
})

async function setup() {
  setActivePinia(createPinia())
  const store = useSessionStore()
  const transport = new MockTransport({ tick: 0 })
  store.transport = transport
  const list = await transport.listSessions()
  for (const info of list) store.sessions[info.id] = createSessionState(info.id, sessionMeta(info))
  store.order = list.map(i => i.id)
  return { store, transport }
}

describe('store: sending, queue actions, rewind and fork', () => {
  it('tracks a message from sending to read, queues while busy, and sends a queued one now', async () => {
    const { store, transport } = await setup()
    const id = 's-proto'
    store.subscribe(id)
    await flush()
    // Hold the first call to see it in flight.
    const real = transport.sendUserMessage.bind(transport)
    let release = () => {}
    transport.sendUserMessage = async (...args) => {
      await new Promise<void>(r => (release = r))
      transport.sendUserMessage = real
      return real(...args)
    }
    const sending = store.send(id, 'build it')
    await flush()
    // In flight: the outbox shows it as sending.
    expect(pendingMessages(store.sessions[id]!, store.outbox[id]).map(m => m.delivery)).toEqual(['sending'])
    release()
    await sending
    const s = store.sessions[id]!
    const first = s.items.findLast(i => i.kind === 'user') as UserItem
    expect(first.text).toBe('build it')
    // Read: the user message carried its id, and the outbox let it go.
    expect(s.delivered[first.clientIds![0]!]).toBe(first.id)
    expect(store.outbox[id]).toEqual([])
    expect(store.isBusy(id)).toBe(true)

    // While the turn waits for a permission, new messages queue on the client.
    await store.send(id, 'then this')
    await store.send(id, 'and that')
    let pending = pendingMessages(s, store.outbox[id])
    expect(pending.map(m => [m.text, m.delivery, m.position])).toEqual([['then this', 'queued', 1], ['and that', 'queued', 2]])

    // Cancel one; send the other now: the turn stops and the message starts the next one.
    const [a, b] = pending
    expect(await store.removeQueued(id, b!.id)).toBe(true)
    await store.sendNow(id, a!.id)
    await flush()
    expect(s.items.filter(i => i.kind === 'turn').map(i => (i as { result: string }).result)).toContain('interrupted')
    const last = s.items.findLast(i => i.kind === 'user') as UserItem
    expect(last).toMatchObject({ text: 'then this', clientIds: [a!.id] })
    expect(s.delivered[a!.id]).toBe(last.id)
    pending = pendingMessages(s, store.outbox[id])
    expect(pending).toEqual([])
  })

  it('cancels and edits messages in the client queue', async () => {
    const { store, transport } = await setup()
    const id = 's-proto'
    store.subscribe(id)
    await flush()
    await store.send(id, 'start')
    // A message the client queued (sent while the turn runs, e.g. from another frontend).
    await transport.sendUserMessage(id, 'q-1', 'queued in client', 1)
    await transport.sendUserMessage(id, 'q-2', 'second', 1)
    const s = store.sessions[id]!
    expect(pendingMessages(s, store.outbox[id]).map(m => [m.id, m.delivery, m.where])).toEqual([['q-1', 'queued', 'client'], ['q-2', 'queued', 'client']])
    expect(await store.removeQueued(id, 'q-1')).toBe(true)
    expect(s.queue.map(q => q.id)).toEqual(['q-2'])
    await store.editQueued(id, 'q-2')
    expect(s.queue).toEqual([])
    expect(store.drafts[id]).toBe('second')
  })

  it('rewinds to before a message and puts it back into the composer', async () => {
    const { store, transport } = await setup()
    const id = 's-proto'
    store.subscribe(id)
    await flush()
    await store.send(id, 'first try')
    await transport.interrupt(id)
    await flush()
    const s = store.sessions[id]!
    const msg = s.items.findLast(i => i.kind === 'user') as UserItem
    await store.rewind(id, msg.id)
    await flush()
    expect(store.drafts[id]).toBe('first try')
    expect(s.items.some(i => i.kind === 'user' && i.id === msg.id)).toBe(false)
    expect(s.items[s.items.length - 1]!.kind).toBe('rewound')
  })

  it('forks from a message into a sub-session with the conversation before it', async () => {
    const { store, transport } = await setup()
    const id = 's-proto'
    store.subscribe(id)
    await flush()
    await store.send(id, 'one')
    await transport.interrupt(id)
    await store.send(id, 'two')
    await transport.interrupt(id)
    await flush()
    const users = store.sessions[id]!.items.filter(i => i.kind === 'user') as UserItem[]
    const two = users.findLast(u => u.text === 'two')!
    const fork = await store.fork(id, { at: two.id, draft: two.text })
    expect(fork).toBeTruthy()
    const f = store.sessions[fork!]!
    expect(f.meta).toMatchObject({ parentId: id, title: 'ConnectRPC proto contracts (fork)' })
    expect(store.currentId).toBe(fork)
    expect(store.drafts[fork!]).toBe('two')
    await flush()
    const texts = (f.items.filter(i => i.kind === 'user') as UserItem[]).map(u => u.text)
    expect(texts).toContain('one')
    expect(texts).not.toContain('two')
  })
})

describe('store: sub-sessions', () => {
  it('detaches a sub-session, archives children with their parent and detaches them when it is deleted', async () => {
    const { store } = await setup()
    const child = (await store.fork('s-proto'))!
    const grandchild = (await store.fork(child))!
    const other = (await store.fork('s-tui'))!
    expect(store.sessions[grandchild]!.meta.parentId).toBe(child)
    expect(store.childrenOf('s-proto').sort()).toEqual([child, grandchild].sort())

    await store.detach(other)
    expect(store.sessions[other]!.meta.parentId).toBe('')

    await store.archive('s-proto')
    for (const x of ['s-proto', child, grandchild]) expect(store.sessions[x]!.meta.archived).toBe(true)
    expect(store.sessions[other]!.meta.archived).toBe(false)

    const c2 = (await store.fork('s-tui'))!
    await store.deleteSession('s-tui')
    expect(store.sessions['s-tui']).toBeUndefined()
    expect(store.sessions[c2]!.meta.parentId).toBe('')
    expect(store.order).toContain(c2)
  })
})
