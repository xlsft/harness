// BrowserConnection: the live view of a session's browser, over the client's browser WebSocket.
//
// Wire format (packages/client/browser/socket.go, proto/harness/agent/v1/browser.proto): the socket is
// GET <agent URL>/harness.agent.v1.Browser/socket with the subprotocols ["harness-browser.v1", "ticket.<ticket>"];
// the ticket is one-time and short-lived, from OpenBrowser. Binary frames from the client are screencast frames:
// [uint32 big-endian header length][header JSON][JPEG]. Text frames are JSON messages:
//   received: {"type":"state","state":...}, {"type":"action","action":...}, {"type":"error","message":"..."},
//             {"type":"closed","reason":"..."} (the browser stopped)
//   sent:     navigate, back, forward, reload, stop, newTab, closeTab, switchTab, viewport, mouse, key, paste, takeover
//
// When the socket drops (the client restarted its listener, the network blipped), the connection asks for a fresh
// ticket and reconnects with backoff. It stops for good when the browser closes, on close(), or after too many
// failed attempts in a row.

import { fromJson, type JsonValue } from '@bufbuild/protobuf'
import { BrowserActionSchema, BrowserStateSchema } from '~/gen/harness/agent/v1/browser_pb'
import { actionView, stateView, type BrowserActionVM, type BrowserFrame, type BrowserKeyInput, type BrowserMouseInput, type BrowserStateVM, type ViewportPreset } from './types'

export const BROWSER_SOCKET_PATH = '/harness.agent.v1.Browser/socket'
export const BROWSER_SUBPROTOCOL = 'harness-browser.v1'
export const BROWSER_TICKET_PREFIX = 'ticket.'

export type BrowserConnectionState = 'idle' | 'connecting' | 'open' | 'reconnecting' | 'closed'

export interface BrowserConnectionOptions {
  // Reconnect after the socket drops (default true).
  reconnect?: boolean
  // Failed attempts in a row before giving up (default 8).
  maxAttempts?: number
  // Backoff: baseDelayMs * 2^attempt, at most maxDelayMs (defaults 250 and 5000).
  baseDelayMs?: number
  maxDelayMs?: number
}

export interface BrowserSocketHandlers {
  onOpen: () => void
  onMessage: (data: string | ArrayBuffer | Uint8Array) => void
  onClose: (code: number, reason: string) => void
}

export interface BrowserSocket {
  send: (data: string) => void
  close: () => void
}

// How a connection gets tickets and sockets (connect.ts: RPCs and WebSocket; mock.ts: the fake browser).
export interface BrowserLink {
  // OpenBrowser: starts the browser if needed. first is true for the first call of a connection (the one that
  // carries the url and viewport options).
  open: (sessionId: string, first: boolean) => Promise<{ state: BrowserStateVM, ticket: string }>
  socket: (ticket: string, handlers: BrowserSocketHandlers) => BrowserSocket
}

/** Decodes a binary frame; undefined when it is malformed. */
export function parseFrame(data: ArrayBuffer | Uint8Array): BrowserFrame | undefined {
  const bytes = data instanceof Uint8Array ? data : new Uint8Array(data)
  if (bytes.length < 4) return undefined
  const n = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength).getUint32(0)
  if (4 + n > bytes.length) return undefined
  let h: { tab?: string, seq?: number, width?: number, height?: number, scale?: number, scrollX?: number, scrollY?: number, ts?: number, mime?: string }
  try {
    h = JSON.parse(new TextDecoder().decode(bytes.subarray(4, 4 + n)))
  } catch {
    return undefined
  }
  return {
    tabId: h.tab ?? '',
    seq: h.seq ?? 0,
    width: h.width ?? 0,
    height: h.height ?? 0,
    scale: h.scale ?? 1,
    scrollX: h.scrollX ?? 0,
    scrollY: h.scrollY ?? 0,
    timestamp: h.ts ?? 0,
    mime: h.mime ?? 'image/jpeg',
    data: bytes.slice(4 + n)
  }
}

/** Encodes a binary frame (the mock's side of the wire). */
export function encodeFrame(f: Omit<BrowserFrame, 'data'>, data: Uint8Array) {
  const header = new TextEncoder().encode(JSON.stringify({
    tab: f.tabId, seq: f.seq, width: f.width, height: f.height, scale: f.scale, scrollX: f.scrollX, scrollY: f.scrollY, ts: f.timestamp, mime: f.mime
  }))
  const out = new Uint8Array(4 + header.length + data.length)
  new DataView(out.buffer).setUint32(0, header.length)
  out.set(header, 4)
  out.set(data, 4 + header.length)
  return out
}

type Listener<A extends unknown[]> = (...args: A) => void

class Emitter<A extends unknown[]> {
  private set = new Set<Listener<A>>()
  on(cb: Listener<A>) {
    this.set.add(cb)
    return () => void this.set.delete(cb)
  }

  emit(...args: A) {
    for (const cb of [...this.set]) cb(...args)
  }
}

// Commands sent while (re)connecting wait here (navigation and viewport changes, not mouse moves).
const MAX_QUEUED_COMMANDS = 32

export class BrowserConnection {
  readonly sessionId: string
  state: BrowserConnectionState = 'idle'
  // The browser's latest state (undefined until the first one arrives).
  browser?: BrowserStateVM
  // Why the connection ended: the browser's close reason, or the error it gave up on.
  closedReason = ''
  error?: unknown

  private link: BrowserLink
  private opts: Required<BrowserConnectionOptions>
  private socket?: BrowserSocket
  private attempts = 0
  private everOpen = false
  private opened = false
  private timer?: ReturnType<typeof setTimeout>
  private queue: string[] = []
  private ready?: Promise<void>
  private settle?: { resolve: () => void, reject: (err: unknown) => void }

  private frameEv = new Emitter<[BrowserFrame]>()
  private stateEv = new Emitter<[BrowserStateVM]>()
  private actionEv = new Emitter<[BrowserActionVM]>()
  private errorEv = new Emitter<[string]>()
  private connEv = new Emitter<[BrowserConnectionState]>()

  constructor(link: BrowserLink, sessionId: string, opts: BrowserConnectionOptions = {}) {
    this.link = link
    this.sessionId = sessionId
    this.opts = { reconnect: true, maxAttempts: 8, baseDelayMs: 250, maxDelayMs: 5000, ...opts }
  }

  /** A screencast frame of the active tab. Returns an unsubscribe function. */
  onFrame(cb: (frame: BrowserFrame) => void) {
    return this.frameEv.on(cb)
  }

  /** Tabs, URLs, titles, loading, viewport, console errors: on connect and on every change. */
  onState(cb: (state: BrowserStateVM) => void) {
    return this.stateEv.on(cb)
  }

  /** The agent acts in the browser (the overlay). The same actions come as BrowserAction session events. */
  onAction(cb: (action: BrowserActionVM) => void) {
    return this.actionEv.on(cb)
  }

  /** A command failed (a bad URL, a closed tab). */
  onError(cb: (message: string) => void) {
    return this.errorEv.on(cb)
  }

  onConnectionChange(cb: (state: BrowserConnectionState) => void) {
    return this.connEv.on(cb)
  }

  /** Connects (once; later calls return the same promise). Resolves when the socket is first open. */
  connect(): Promise<void> {
    if (!this.ready) {
      this.ready = new Promise<void>((resolve, reject) => {
        this.settle = { resolve, reject }
      })
      this.ready.catch(() => {})
      void this.dial()
    }
    return this.ready
  }

  navigate(url: string) {
    this.command({ type: 'navigate', url })
  }

  back() {
    this.command({ type: 'back' })
  }

  forward() {
    this.command({ type: 'forward' })
  }

  reload() {
    this.command({ type: 'reload' })
  }

  stop() {
    this.command({ type: 'stop' })
  }

  newTab(url = '') {
    this.command({ type: 'newTab', url })
  }

  closeTab(tab: string) {
    this.command({ type: 'closeTab', tab })
  }

  switchTab(tab: string) {
    this.command({ type: 'switchTab', tab })
  }

  setViewport(preset: ViewportPreset, width = 0, height = 0) {
    this.command({ type: 'viewport', preset, width, height })
  }

  /** Mouse input in page CSS px; dropped while not connected. */
  mouse(input: BrowserMouseInput) {
    this.live({ type: 'mouse', ...input })
  }

  /** Keyboard input; dropped while not connected. */
  key(input: BrowserKeyInput) {
    this.live({ type: 'key', ...input })
  }

  paste(text: string) {
    this.live({ type: 'paste', text })
  }

  /** The user takes the browser over (the agent's action tools fail) or hands it back. */
  takeover(on: boolean) {
    this.command({ type: 'takeover', on })
  }

  /** Disconnects for good. The browser keeps running on the client (stop it with BrowserApi.close). */
  close() {
    if (this.state === 'closed') return
    this.stop_('', new Error('connection closed'))
  }

  // A command that must not be lost: queued while (re)connecting.
  private command(msg: Record<string, unknown>) {
    const text = JSON.stringify(msg)
    if (this.state === 'open' && this.socket) this.socket.send(text)
    else if (this.state !== 'closed' && this.queue.length < MAX_QUEUED_COMMANDS) this.queue.push(text)
  }

  // Input is only meaningful for the page shown now.
  private live(msg: Record<string, unknown>) {
    if (this.state === 'open' && this.socket) this.socket.send(JSON.stringify(msg))
  }

  private setState(s: BrowserConnectionState) {
    if (this.state === s) return
    this.state = s
    this.connEv.emit(s)
  }

  private over() {
    return this.state === 'closed'
  }

  private stop_(reason: string, err?: unknown) {
    clearTimeout(this.timer)
    const sock = this.socket
    this.socket = undefined
    this.queue = []
    this.closedReason = reason
    sock?.close()
    if (err !== undefined && !this.everOpen) this.settle?.reject(err)
    this.setState('closed')
  }

  private async dial() {
    if (this.over()) return
    this.setState(this.everOpen ? 'reconnecting' : 'connecting')
    let ticket: string
    try {
      const res = await this.link.open(this.sessionId, !this.opened)
      this.opened = true
      ticket = res.ticket
      this.setBrowser(res.state)
    } catch (err) {
      this.retry(err)
      return
    }
    if (this.over()) return
    const sock: BrowserSocket = this.link.socket(ticket, {
      onOpen: () => {
        if (this.socket !== sock) return
        this.attempts = 0
        this.everOpen = true
        this.setState('open')
        for (const m of this.queue) sock.send(m)
        this.queue = []
        this.settle?.resolve()
      },
      onMessage: (data) => {
        if (this.socket !== sock) return
        if (typeof data === 'string') {
          this.message(data)
        } else {
          const f = parseFrame(data)
          if (f) this.frameEv.emit(f)
        }
      },
      onClose: (code, reason) => {
        if (this.socket !== sock) return
        this.socket = undefined
        if (this.over()) return
        this.retry(new Error(`browser socket closed (${code}${reason ? `: ${reason}` : ''})`))
      }
    })
    this.socket = sock
  }

  private retry(err: unknown) {
    if (!this.opts.reconnect || this.attempts >= this.opts.maxAttempts) {
      this.error = err
      this.stop_(err instanceof Error ? err.message : String(err), err)
      return
    }
    const delay = Math.min(this.opts.maxDelayMs, this.opts.baseDelayMs * 2 ** this.attempts)
    this.attempts++
    this.setState(this.everOpen ? 'reconnecting' : 'connecting')
    this.timer = setTimeout(() => void this.dial(), delay)
  }

  private setBrowser(s: BrowserStateVM) {
    this.browser = s
    this.stateEv.emit(s)
  }

  private message(text: string) {
    let msg: { type?: string, state?: JsonValue, action?: JsonValue, message?: string, reason?: string }
    try {
      msg = JSON.parse(text)
    } catch {
      return
    }
    switch (msg.type) {
      case 'state':
        if (msg.state !== undefined) this.setBrowser(stateView(fromJson(BrowserStateSchema, msg.state, { ignoreUnknownFields: true })))
        break
      case 'action':
        if (msg.action !== undefined) this.actionEv.emit(actionView(fromJson(BrowserActionSchema, msg.action, { ignoreUnknownFields: true })))
        break
      case 'error':
        this.errorEv.emit(msg.message ?? '')
        break
      case 'closed':
        this.stop_(msg.reason ?? 'closed')
        break
    }
  }
}

/** The browser socket URL for an agent URL (http → ws, https → wss; a path prefix is kept). */
export function browserSocketUrl(agentUrl: string) {
  const url = new URL(agentUrl)
  url.protocol = url.protocol === 'https:' ? 'wss:' : 'ws:'
  url.pathname = url.pathname.replace(/\/+$/, '') + BROWSER_SOCKET_PATH
  url.search = ''
  url.hash = ''
  return url.toString()
}

/** Opens browser sockets with the page's WebSocket (the ticket goes in a subprotocol entry, never the URL). */
export function webSocketOpener(agentUrl: string, WS: typeof WebSocket = globalThis.WebSocket): BrowserLink['socket'] {
  const url = browserSocketUrl(agentUrl)
  return (ticket, h) => {
    const ws = new WS(url, [BROWSER_SUBPROTOCOL, BROWSER_TICKET_PREFIX + ticket])
    ws.binaryType = 'arraybuffer'
    ws.onopen = () => h.onOpen()
    ws.onmessage = ev => h.onMessage(ev.data as string | ArrayBuffer)
    ws.onclose = ev => h.onClose(ev.code, ev.reason)
    return {
      send: data => ws.send(data),
      close: () => {
        ws.onopen = ws.onmessage = ws.onclose = null
        ws.close(1000)
      }
    }
  }
}
