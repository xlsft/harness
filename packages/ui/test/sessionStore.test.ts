import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createSessionState, sessionMeta } from '~/lib/session/reducer'
import { MockTransport } from '~/lib/transport/mock'
import type { SessionPatch } from '~/lib/transport/types'
import { useSessionStore } from '~/stores/sessions'

// The store imports markRaw from 'vue', which Nuxt provides and this package does not depend on directly
// (pinia brings its own copy).
vi.mock('vue', () => ({ markRaw: <T>(v: T) => v }))

// The store against the mock transport; `failing` makes the client refuse user-state changes.
async function setup() {
  setActivePinia(createPinia())
  const store = useSessionStore()
  const transport = new MockTransport({ tick: 0 })
  const calls: { id: string, patch: SessionPatch }[] = []
  let failing = false
  const update = transport.updateSession.bind(transport)
  transport.updateSession = async (id, patch) => {
    calls.push({ id, patch })
    if (failing) throw new Error('harness server unreachable; nothing was changed')
    return update(id, patch)
  }
  const del = transport.deleteSession.bind(transport)
  transport.deleteSession = async (id) => {
    if (failing) throw new Error('harness server unreachable; nothing was changed')
    return del(id)
  }
  store.transport = transport
  const list = await transport.listSessions()
  for (const info of list) store.sessions[info.id] = createSessionState(info.id, sessionMeta(info))
  store.order = list.map(i => i.id)
  return { store, transport, calls, fail: (on: boolean) => (failing = on) }
}

describe('session user state', () => {
  let ctx: Awaited<ReturnType<typeof setup>>
  beforeEach(async () => {
    ctx = await setup()
  })

  it('pins, marks unread and renames through the client', async () => {
    const { store, calls, transport } = ctx
    await store.setPinned('s-proto', true)
    await store.setUnread('s-proto', true)
    await store.rename('s-proto', '  Proto contracts ')
    expect(calls.map(c => c.patch)).toEqual([{ pinned: true }, { unread: true }, { title: 'Proto contracts' }])
    expect(store.sessions['s-proto']!.meta).toMatchObject({ pinned: true, unread: true, title: 'Proto contracts' })
    // The state comes back from the client, not from this browser.
    const listed = (await transport.listSessions()).find(i => i.id === 's-proto')!
    expect(listed).toMatchObject({ pinned: true, unread: true, title: 'Proto contracts' })
    // An empty or unchanged title is not sent.
    await store.rename('s-proto', '   ')
    await store.rename('s-proto', 'Proto contracts')
    expect(calls).toHaveLength(3)
  })

  it('rolls an optimistic change back when the client refuses it', async () => {
    const { store, fail } = ctx
    fail(true)
    const p = store.setPinned('s-tui', true)
    expect(store.sessions['s-tui']!.meta.pinned).toBe(true)
    await expect(p).rejects.toThrow(/unreachable/)
    expect(store.sessions['s-tui']!.meta.pinned).toBe(false)
    await expect(store.rename('s-tui', 'Other')).rejects.toThrow()
    expect(store.sessions['s-tui']!.meta.title).toBe('TUI on charm.land')
  })

  it('archives (hidden from the list, unpinned) and unarchives', async () => {
    const { store, transport } = ctx
    await store.setPinned('s-import', true)
    store.currentId = 's-import'
    await store.archive('s-import')
    expect(store.currentId).toBe('')
    expect(store.sessionList.map(s => s.meta.id)).not.toContain('s-import')
    expect(store.sessions['s-import']!.meta).toMatchObject({ archived: true, pinned: false })
    expect((await transport.listSessions()).map(i => i.id)).not.toContain('s-import')
    await store.updateSession('s-import', { archived: false })
    expect(store.sessionList.map(s => s.meta.id)).toContain('s-import')
  })

  it('deletes a session, and brings it back when the client refuses', async () => {
    const { store, transport, fail } = ctx
    const before = [...store.order]
    fail(true)
    await expect(store.deleteSession('s-s6')).rejects.toThrow()
    expect(store.order).toEqual(before)
    fail(false)
    await store.deleteSession('s-s6')
    expect(store.order).not.toContain('s-s6')
    expect(store.sessions['s-s6']).toBeUndefined()
    expect((await transport.listSessions()).map(i => i.id)).not.toContain('s-s6')
  })

  it('marks a session unread when a turn finishes while another one is open, and read when opened', async () => {
    const { store, calls } = ctx
    store.open('s-client-loop')
    store.open('s-referral')
    await store.send('s-client-loop', 'hello')
    await waitFor(() => store.sessions['s-client-loop']!.meta.unread)
    expect(calls.some(c => c.id === 's-client-loop' && c.patch.unread === true)).toBe(true)
    store.open('s-client-loop')
    await waitFor(() => !store.sessions['s-client-loop']!.meta.unread)
    expect(calls.at(-1)).toEqual({ id: 's-client-loop', patch: { unread: false } })
  })
})

describe('sessions of other machines', () => {
  const REMOTE = 's-relay-backoff'
  let ctx: Awaited<ReturnType<typeof setup>>
  beforeEach(async () => {
    ctx = await setup()
  })

  it('reads a remote session again on every open: its stream ends after the server transcript', async () => {
    const { store } = ctx
    expect(store.sessions[REMOTE]!.meta).toMatchObject({ remote: true, deviceName: 'workstation', deviceOnline: true, remoteControllable: false })
    store.open(REMOTE)
    await waitFor(() => !store.unsubscribers[REMOTE])
    const items = store.sessions[REMOTE]!.items.length
    expect(items).toBeGreaterThan(0)
    store.open('s-proto')
    store.open(REMOTE)
    await waitFor(() => !store.unsubscribers[REMOTE] && store.sessions[REMOTE]!.items.length > 0)
    expect(store.sessions[REMOTE]!.items).toHaveLength(items)
    expect(store.error).toBe('')
  })

  it('never marks a remote session unread from here, and drives it only while it is remote-controllable', async () => {
    const { store, transport, calls } = ctx
    store.sessions[REMOTE]!.meta.status = 'working'
    await store.refreshList()
    expect(calls.some(c => c.id === REMOTE)).toBe(false)

    store.continueRemotely(REMOTE)
    expect(store.remoteControl[REMOTE]).toBeUndefined()
    store.open(REMOTE)
    await waitFor(() => !store.unsubscribers[REMOTE])
    // The list poll says it is now open on its machine.
    store.sessions[REMOTE]!.meta.remoteControllable = true
    const subscribe = vi.spyOn(transport, 'subscribe')
    store.continueRemotely(REMOTE)
    expect(store.remoteControl[REMOTE]).toBe(true)
    // The snapshot is replaced by a new stream (the relay's, on a real client).
    expect(subscribe).toHaveBeenCalledWith(REMOTE, 0, expect.anything())
  })

  it('continues a remote session here as a fork of its transcript', async () => {
    const { store, transport } = ctx
    const create = vi.spyOn(transport, 'createSession')
    await store.createSession({ cwd: '~/Projects/xlsft/harness', forkOf: REMOTE })
    expect(create).toHaveBeenCalledWith(expect.objectContaining({ cwd: '~/Projects/xlsft/harness', resumeSessionId: REMOTE, fork: true }))
  })
})

async function waitFor(cond: () => boolean, ms = 3000) {
  const end = Date.now() + ms
  while (!cond()) {
    if (Date.now() > end) throw new Error('timed out')
    await new Promise(r => setTimeout(r, 5))
  }
}
