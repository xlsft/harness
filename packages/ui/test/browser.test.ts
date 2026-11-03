import { describe, expect, it } from 'vitest'
import { create } from '@bufbuild/protobuf'
import { SessionEventSchema } from '~/gen/harness/agent/v1/agent_pb'
import { BrowserActionSchema, BrowserAction_Phase } from '~/gen/harness/agent/v1/browser_pb'
import { reduce, createSessionState } from '~/lib/session/reducer'
import { MockBrowserApi } from '~/lib/browser/mock'
import { BrowserConnection, browserSocketUrl, encodeFrame, parseFrame, type BrowserLink, type BrowserSocketHandlers } from '~/lib/browser/connection'
import { actionLabel, modifierMask, type BrowserFrame, type BrowserStateVM } from '~/lib/browser/types'

function nextState(conn: BrowserConnection, pred: (s: BrowserStateVM) => boolean) {
  return new Promise<BrowserStateVM>((resolve) => {
    if (conn.browser && pred(conn.browser)) return resolve(conn.browser)
    const off = conn.onState((s) => {
      if (pred(s)) {
        off()
        resolve(s)
      }
    })
  })
}

function nextFrame(conn: BrowserConnection) {
  return new Promise<BrowserFrame>((resolve) => {
    const off = conn.onFrame((f) => {
      off()
      resolve(f)
    })
  })
}

describe('browser wire format', () => {
  it('encodes and parses frames', () => {
    const data = encodeFrame({ tabId: 't1', seq: 7, width: 1280, height: 800, scale: 2, scrollX: 0, scrollY: 40, timestamp: 12.5, mime: 'image/jpeg' }, new Uint8Array([0xff, 0xd8, 1]))
    const f = parseFrame(data.buffer as ArrayBuffer)!
    expect(f.tabId).toBe('t1')
    expect(f.seq).toBe(7)
    expect(f.scale).toBe(2)
    expect(f.scrollY).toBe(40)
    expect([...f.data]).toEqual([0xff, 0xd8, 1])
    expect(parseFrame(new Uint8Array([0, 0]))).toBeUndefined()
    expect(parseFrame(new Uint8Array([0, 0, 0, 200, 1]))).toBeUndefined()
  })

  it('builds the socket URL', () => {
    expect(browserSocketUrl('http://127.0.0.1:7777')).toBe('ws://127.0.0.1:7777/harness.agent.v1.Browser/socket')
    expect(browserSocketUrl('https://host/prefix/')).toBe('wss://host/prefix/harness.agent.v1.Browser/socket')
  })

  it('computes modifier masks and labels', () => {
    expect(modifierMask({ ctrlKey: true, shiftKey: true })).toBe(10)
    expect(actionLabel({ kind: 'click', label: 'Add provider' })).toBe('click · Add provider')
    expect(actionLabel({ kind: 'double_click', label: '' })).toBe('double click')
  })
})

describe('MockBrowserApi', () => {
  it('reports availability and downloads', async () => {
    const api = new MockBrowserApi({ available: false })
    expect((await api.availability()).available).toBe(false)
    const progress: number[] = []
    const res = await api.install(n => progress.push(n)).done
    expect(progress.length).toBeGreaterThan(1)
    expect(res?.source).toBe('downloaded')
    expect((await api.availability()).available).toBe(true)
  })

  it('streams frames and state, and takes navigation and tab commands', async () => {
    const api = new MockBrowserApi({ latencyMs: 1 })
    const conn = api.open('s1', { url: 'localhost:3000/providers' })
    const frame = nextFrame(conn)
    await conn.connect()
    const f = await frame
    expect(f.width).toBe(1280)
    expect(f.data.length).toBeGreaterThan(50)
    expect(conn.browser?.tabs.some(t => t.url === 'http://localhost:3000/providers')).toBe(true)

    conn.navigate('example.com/a')
    let s = await nextState(conn, st => st.tabs.find(t => t.active)?.url === 'https://example.com/a' && !st.tabs.find(t => t.active)?.loading)
    expect(s.tabs.find(t => t.active)?.canGoBack).toBe(true)

    conn.back()
    s = await nextState(conn, st => st.tabs.find(t => t.active)?.url === 'http://localhost:3000/providers')
    expect(s.tabs.find(t => t.active)?.canGoForward).toBe(true)

    conn.newTab('https://github.com/')
    s = await nextState(conn, st => st.tabs.length === 3)
    const newest = s.activeTabId
    conn.switchTab(s.tabs[0]!.id)
    await nextState(conn, st => st.activeTabId === st.tabs[0]!.id)
    conn.closeTab(newest)
    await nextState(conn, st => st.tabs.length === 2)

    conn.setViewport('mobile')
    s = await nextState(conn, st => st.viewport.preset === 'mobile')
    expect(s.viewport.width).toBe(375)
    expect(s.viewport.touch).toBe(true)
    const small = await nextFrame(conn)
    expect(small.width).toBe(375)

    conn.takeover(true)
    await nextState(conn, st => st.takenOver)

    const errors: string[] = []
    conn.onError(m => errors.push(m))
    conn.navigate('javascript:alert(1)')
    conn.switchTab('t99')
    await new Promise(r => setTimeout(r, 5))
    expect(errors.length).toBe(2)

    const marked = nextFrame(conn)
    conn.mouse({ event: 'down', x: 30, y: 40, button: 'left', clickCount: 1 })
    expect((await marked).data.length).toBeGreaterThan(50)

    conn.close()
    expect(conn.state).toBe('closed')
  })

  it('delivers agent actions and the close of the browser', async () => {
    const api = new MockBrowserApi({ latencyMs: 1 })
    const conn = api.open('s2')
    await conn.connect()
    const got = new Promise<ReturnType<typeof String>>((resolve) => {
      conn.onAction(a => resolve(`${a.phase} ${a.kind} ${a.label} ${a.x}`))
    })
    api.agentAct('s2', { toolUseId: 'tu1', kind: 'click', label: 'Add provider', x: 10, y: 20 })
    expect(await got).toBe('started click Add provider 10')
    await api.close('s2')
    expect(conn.state).toBe('closed')
    expect(conn.closedReason).toBe('closed')
    expect((await api.listTabs('s2')).running).toBe(false)
  })
})

describe('BrowserConnection', () => {
  it('reconnects with a fresh ticket and queues commands meanwhile', async () => {
    const sockets: { h: BrowserSocketHandlers, sent: string[] }[] = []
    const tickets: string[] = []
    let opens = 0
    const link: BrowserLink = {
      open: async () => {
        opens++
        return { ticket: `tk${opens}`, state: { sessionId: 's', running: true, tabs: [], activeTabId: '', viewport: { preset: 'desktop', width: 1280, height: 800, deviceScaleFactor: 1, mobile: false, touch: false }, allowedSites: [], takenOver: false } }
      },
      socket: (ticket, h) => {
        tickets.push(ticket)
        const s = { h, sent: [] as string[] }
        sockets.push(s)
        queueMicrotask(() => h.onOpen())
        return { send: d => s.sent.push(d), close: () => {} }
      }
    }
    const conn = new BrowserConnection(link, 's', { baseDelayMs: 1, maxDelayMs: 2 })
    await conn.connect()
    conn.reload()
    expect(sockets[0]!.sent).toEqual(['{"type":"reload"}'])

    sockets[0]!.h.onClose(1006, 'gone')
    expect(conn.state).toBe('reconnecting')
    conn.navigate('https://a.test/')
    conn.mouse({ event: 'move', x: 1, y: 1 }) // input is dropped while disconnected
    await new Promise(r => setTimeout(r, 20))
    expect(conn.state).toBe('open')
    expect(tickets).toEqual(['tk1', 'tk2'])
    expect(sockets[1]!.sent).toEqual(['{"type":"navigate","url":"https://a.test/"}'])

    // The browser closing ends the connection for good.
    sockets[1]!.h.onMessage('{"type":"closed","reason":"idle"}')
    expect(conn.state).toBe('closed')
    expect(conn.closedReason).toBe('idle')
  })

  it('gives up after too many failed attempts', async () => {
    const link: BrowserLink = {
      open: async () => {
        throw new Error('no browser')
      },
      socket: () => ({ send: () => {}, close: () => {} })
    }
    const conn = new BrowserConnection(link, 's', { baseDelayMs: 1, maxDelayMs: 1, maxAttempts: 2 })
    await expect(conn.connect()).rejects.toThrow('no browser')
    expect(conn.state).toBe('closed')
  })
})

describe('reducer', () => {
  const event = (seq: number, value: Parameters<typeof create<typeof BrowserActionSchema>>[1]) => create(SessionEventSchema, {
    seq: BigInt(seq),
    sessionId: 's',
    timestamp: new Date(0).toISOString(),
    event: { case: 'browserAction', value: create(BrowserActionSchema, value) }
  })

  it('tracks the agent action overlay', () => {
    const s = createSessionState('s')
    reduce(s, create(SessionEventSchema, { seq: 1n, sessionId: 's', event: { case: 'turnStarted', value: { userMessageUuid: 'u', model: 'm' } } }))
    expect(s.browserAction).toBeNull()

    reduce(s, event(2, { toolUseId: 'a', phase: BrowserAction_Phase.STARTED, kind: 'navigate', label: 'https://x.test/' }))
    expect(s.browserAction?.step).toBe(1)
    reduce(s, event(3, { toolUseId: 'a', phase: BrowserAction_Phase.FINISHED, kind: 'navigate', label: 'https://x.test/' }))
    expect(s.browserAction?.phase).toBe('finished')

    reduce(s, event(4, { toolUseId: 'b', phase: BrowserAction_Phase.STARTED, kind: 'click', label: 'Add provider', ref: 'ref_4', x: 10, y: 20, rect: [5, 15, 10, 10] }))
    expect(s.browserAction).toMatchObject({ step: 2, kind: 'click', label: 'Add provider', ref: 'ref_4', x: 10, y: 20, rect: { x: 5, y: 15, width: 10, height: 10 } })
    expect(s.browserSteps).toBe(2)

    // A late FINISHED of another call does not replace the current one.
    reduce(s, event(5, { toolUseId: 'a', phase: BrowserAction_Phase.FINISHED, kind: 'navigate', error: 'x' }))
    expect(s.browserAction?.toolUseId).toBe('b')
    expect(s.browserAction?.phase).toBe('started')
    reduce(s, event(6, { toolUseId: 'b', phase: BrowserAction_Phase.FINISHED, kind: 'click', error: 'covered' }))
    expect(s.browserAction).toMatchObject({ phase: 'finished', error: 'covered', step: 2 })

    // The turn's end clears the overlay; the next turn counts from 1.
    reduce(s, create(SessionEventSchema, { seq: 7n, sessionId: 's', event: { case: 'turnFinished', value: {} } }))
    expect(s.browserAction).toBeNull()
    reduce(s, create(SessionEventSchema, { seq: 8n, sessionId: 's', event: { case: 'turnStarted', value: { userMessageUuid: 'v', model: 'm' } } }))
    reduce(s, event(9, { toolUseId: 'c', phase: BrowserAction_Phase.STARTED, kind: 'snapshot' }))
    expect(s.browserAction?.step).toBe(1)
  })
})
