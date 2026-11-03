// TerminalConnection: the interactive side of a terminal, over the client's terminal WebSocket.
//
// Wire format (packages/client/terminal/socket.go): the socket is GET <agent URL>/harness.agent.v1.Terminal/socket
// with the subprotocols ["harness-terminal.v1", "ticket.<ticket>"]; the ticket is one-time and short-lived, from
// TerminalsApi.create/attach (browsers cannot send the bearer token on a WebSocket). Binary frames the UI sends
// are input; binary frames it receives are output, starting with a replay of the recent output. Text frames are
// JSON control messages:
//   received: {"type":"exit","code":0}, {"type":"resize","cols":80,"rows":24} (another viewer resized),
//             {"type":"skipped","bytes":1234} (this viewer fell behind and missed output)
//   sent:     {"type":"resize","cols":80,"rows":24}, {"type":"input","data":"..."} (input is sent binary here)
//
// When the socket drops (the client restarted its listener, the network blipped), the connection gets a fresh
// ticket and reconnects with backoff; each (re)connect replays the recent output, announced by onAttach so the
// pane can clear its screen first. It stops for good on the shell's exit, on close(), when the terminal is gone,
// or after too many failed attempts in a row.

export const TERMINAL_SOCKET_PATH = '/harness.agent.v1.Terminal/socket'
export const TERMINAL_SUBPROTOCOL = 'harness-terminal.v1'
export const TICKET_PROTOCOL_PREFIX = 'ticket.'

export type TerminalConnectionState = 'idle' | 'connecting' | 'open' | 'reconnecting' | 'exited' | 'closed'

export interface TerminalConnectionOptions {
  // Reconnect after the socket drops (default true).
  reconnect?: boolean
  // Failed attempts in a row before giving up (default 8).
  maxAttempts?: number
  // Backoff: baseDelayMs * 2^attempt, at most maxDelayMs (defaults 250 and 5000).
  baseDelayMs?: number
  maxDelayMs?: number
}

// What a socket implementation reports. Binary data may come as an ArrayBuffer or a Uint8Array.
export interface TerminalSocketHandlers {
  onOpen: () => void
  onMessage: (data: string | ArrayBuffer | Uint8Array) => void
  onClose: (code: number, reason: string) => void
}

export interface TerminalSocket {
  send: (data: string | Uint8Array<ArrayBuffer>) => void
  close: () => void
}

export type OpenTerminalSocket = (terminalId: string, ticket: string, handlers: TerminalSocketHandlers) => TerminalSocket

// How a connection gets tickets and sockets (connect.ts: RPCs and WebSocket; mock.ts: the fake shell).
export interface TerminalLink {
  attach: (terminalId: string) => Promise<{ ticket: string }>
  open: OpenTerminalSocket
}

/** Thrown by TerminalLink.attach when the terminal no longer exists: the connection stops reconnecting. */
export class TerminalGoneError extends Error {
  constructor(terminalId: string) {
    super(`terminal ${terminalId} no longer exists`)
    this.name = 'TerminalGoneError'
  }
}

// Input typed while (re)connecting is sent once open, up to this many bytes.
const MAX_QUEUED_INPUT = 64 * 1024

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

interface Control {
  type: string
  cols?: number
  rows?: number
  code?: number
  bytes?: number
}

export class TerminalConnection {
  readonly terminalId: string
  state: TerminalConnectionState = 'idle'
  // Set on the exit.
  exitCode?: number
  // Why the connection stopped, when it gave up.
  error?: unknown

  private link: TerminalLink
  private opts: Required<TerminalConnectionOptions>
  private ticket?: string
  private socket?: TerminalSocket
  private attempts = 0
  private everOpen = false
  private timer?: ReturnType<typeof setTimeout>
  private size?: { cols: number, rows: number }
  private queue: Uint8Array<ArrayBuffer>[] = []
  private queued = 0
  private ready?: Promise<void>
  private settle?: { resolve: () => void, reject: (err: unknown) => void }
  private encoder = new TextEncoder()

  private dataEv = new Emitter<[Uint8Array]>()
  private exitEv = new Emitter<[number]>()
  private attachEv = new Emitter<[]>()
  private resizeEv = new Emitter<[number, number]>()
  private skippedEv = new Emitter<[number]>()
  private stateEv = new Emitter<[TerminalConnectionState]>()

  constructor(link: TerminalLink, terminalId: string, ticket?: string, opts: TerminalConnectionOptions = {}) {
    this.link = link
    this.terminalId = terminalId
    this.ticket = ticket
    this.opts = { reconnect: true, maxAttempts: 8, baseDelayMs: 250, maxDelayMs: 5000, ...opts }
  }

  /** Output bytes (the replay after each attach, then live output). Returns an unsubscribe function. */
  onData(cb: (data: Uint8Array) => void) {
    return this.dataEv.on(cb)
  }

  /** The shell exited; the connection is over. */
  onExit(cb: (code: number) => void) {
    return this.exitEv.on(cb)
  }

  /** A (re)connect: the recent output is replayed next, so clear the screen (xterm reset) first. */
  onAttach(cb: () => void) {
    return this.attachEv.on(cb)
  }

  /** Another viewer resized the terminal. */
  onResize(cb: (cols: number, rows: number) => void) {
    return this.resizeEv.on(cb)
  }

  /** This viewer fell behind and the client skipped this many bytes of output. */
  onSkipped(cb: (bytes: number) => void) {
    return this.skippedEv.on(cb)
  }

  onStateChange(cb: (state: TerminalConnectionState) => void) {
    return this.stateEv.on(cb)
  }

  /** Connects (once; later calls return the same promise). Resolves when the socket is first open. */
  connect(): Promise<void> {
    if (!this.ready) {
      this.ready = new Promise<void>((resolve, reject) => {
        this.settle = { resolve, reject }
      })
      // Callers may ignore the promise and follow state changes instead.
      this.ready.catch(() => {})
      void this.dial()
    }
    return this.ready
  }

  /** Sends input; while (re)connecting it is queued (up to 64 KB) and sent once open. */
  write(data: string | Uint8Array) {
    const bytes = typeof data === 'string' ? this.encoder.encode(data) : new Uint8Array(data)
    if (!bytes.length) return
    if (this.state === 'open' && this.socket) {
      this.socket.send(bytes)
    } else if ((this.state === 'idle' || this.state === 'connecting' || this.state === 'reconnecting') && this.queued + bytes.length <= MAX_QUEUED_INPUT) {
      this.queue.push(bytes)
      this.queued += bytes.length
    }
  }

  /** Resizes the PTY; the size is also sent again after every reconnect. */
  resize(cols: number, rows: number) {
    cols = Math.floor(cols)
    rows = Math.floor(rows)
    if (cols <= 0 || rows <= 0) return
    if (this.size?.cols === cols && this.size?.rows === rows) return
    this.size = { cols, rows }
    if (this.state === 'open') this.sendResize()
  }

  /** Disconnects for good. The terminal keeps running on the client (kill it with TerminalsApi.kill). */
  close() {
    if (this.over()) return
    this.stop('closed', new Error('connection closed'))
  }

  // The state can change while dial() awaits a ticket; a method keeps TypeScript from narrowing it.
  private over() {
    return this.state === 'closed' || this.state === 'exited'
  }

  private setState(s: TerminalConnectionState) {
    if (this.state === s) return
    this.state = s
    this.stateEv.emit(s)
  }

  private stop(state: 'closed' | 'exited', err?: unknown) {
    clearTimeout(this.timer)
    const sock = this.socket
    this.socket = undefined
    this.queue = []
    this.queued = 0
    sock?.close()
    if (state === 'closed' && err !== undefined && !this.everOpen) this.settle?.reject(err)
    this.setState(state)
  }

  private async dial() {
    if (this.over()) return
    this.setState(this.everOpen ? 'reconnecting' : 'connecting')
    let ticket = this.ticket
    this.ticket = undefined
    if (!ticket) {
      try {
        ticket = (await this.link.attach(this.terminalId)).ticket
      } catch (err) {
        if (err instanceof TerminalGoneError) {
          this.error = err
          this.stop('closed', err)
        } else {
          this.retry(err)
        }
        return
      }
      if (this.over()) return
    }
    const sock: TerminalSocket = this.link.open(this.terminalId, ticket, {
      onOpen: () => {
        if (this.socket !== sock) return
        this.attempts = 0
        this.everOpen = true
        this.setState('open')
        this.attachEv.emit()
        if (this.size) this.sendResize()
        for (const b of this.queue) sock.send(b)
        this.queue = []
        this.queued = 0
        this.settle?.resolve()
      },
      onMessage: (data) => {
        if (this.socket !== sock) return
        if (typeof data === 'string') this.control(data)
        else this.dataEv.emit(data instanceof Uint8Array ? data : new Uint8Array(data))
      },
      onClose: (code, reason) => {
        if (this.socket !== sock) return
        this.socket = undefined
        if (this.over()) return
        this.retry(new Error(`terminal socket closed (${code}${reason ? `: ${reason}` : ''})`))
      }
    })
    this.socket = sock
  }

  private retry(err: unknown) {
    if (!this.opts.reconnect || this.attempts >= this.opts.maxAttempts) {
      this.error = err
      this.stop('closed', err)
      return
    }
    const delay = Math.min(this.opts.maxDelayMs, this.opts.baseDelayMs * 2 ** this.attempts)
    this.attempts++
    this.setState(this.everOpen ? 'reconnecting' : 'connecting')
    this.timer = setTimeout(() => void this.dial(), delay)
  }

  private control(text: string) {
    let msg: Control
    try {
      msg = JSON.parse(text) as Control
    } catch {
      return
    }
    switch (msg.type) {
      case 'exit':
        this.exitCode = msg.code ?? 0
        this.stop('exited')
        this.exitEv.emit(this.exitCode)
        break
      case 'resize':
        if (msg.cols && msg.rows) {
          this.size = { cols: msg.cols, rows: msg.rows }
          this.resizeEv.emit(msg.cols, msg.rows)
        }
        break
      case 'skipped':
        this.skippedEv.emit(msg.bytes ?? 0)
        break
    }
  }

  private sendResize() {
    if (this.size) this.socket?.send(JSON.stringify({ type: 'resize', ...this.size }))
  }
}

/** The terminal socket URL for an agent URL (http → ws, https → wss; a path prefix is kept). */
export function terminalSocketUrl(agentUrl: string) {
  const url = new URL(agentUrl)
  url.protocol = url.protocol === 'https:' ? 'wss:' : 'ws:'
  url.pathname = url.pathname.replace(/\/+$/, '') + TERMINAL_SOCKET_PATH
  url.search = ''
  url.hash = ''
  return url.toString()
}

/** Opens terminal sockets with the browser's WebSocket (the ticket goes in a subprotocol entry, never the URL). */
export function webSocketOpener(agentUrl: string, WS: typeof WebSocket = globalThis.WebSocket): OpenTerminalSocket {
  const url = terminalSocketUrl(agentUrl)
  return (_terminalId, ticket, h) => {
    const ws = new WS(url, [TERMINAL_SUBPROTOCOL, TICKET_PROTOCOL_PREFIX + ticket])
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
