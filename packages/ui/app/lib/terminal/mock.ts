// MockTerminalsApi: terminals for the mock transport, each running FakeShell, a tiny line-editing shell that
// echoes input and knows ls, pwd, echo, cd, clear, help and exit. Its sockets speak the real wire format
// (binary output, JSON control messages, one-time tickets, replay on reattach), so a TerminalConnection behaves
// exactly as against a client.

import { TerminalConnection, TerminalGoneError, type OpenTerminalSocket, type TerminalConnectionOptions, type TerminalLink, type TerminalSocketHandlers } from './connection'
import type { CreateTerminalOptions, TerminalInfo, TerminalTicket, TerminalsApi } from './types'

const MOCK_HOME = '/Users/xlsft'
const MAX_TERMINALS = 16
const BUFFER_SIZE = 256 * 1024
const SIGHUP_EXIT = 128 + 1

const ESC = '\x1b'
const BOLD_GREEN = `${ESC}[1;32m`
const BOLD_BLUE = `${ESC}[1;34m`
const RESET = `${ESC}[0m`

// What the shell answers: output, and the exit code when the shell ends.
interface ShellOutput {
  out: string
  exit?: number
}

/** A pretend shell with line editing; feed it input, it returns what the terminal shows. */
export class FakeShell {
  cwd: string
  private home: string
  private line = ''
  // Inside an escape sequence the UI sent (arrow keys and the like): ignored.
  private esc: '' | 'esc' | 'csi' = ''
  private status = 0

  constructor(cwd: string, home = MOCK_HOME) {
    this.cwd = cwd
    this.home = home
  }

  banner() {
    return `Mock shell: try ${BOLD_GREEN}ls${RESET}, ${BOLD_GREEN}pwd${RESET}, ${BOLD_GREEN}echo${RESET}, ${BOLD_GREEN}cd${RESET} or ${BOLD_GREEN}help${RESET}.\r\n${this.prompt()}`
  }

  prompt() {
    return `${BOLD_GREEN}mock${RESET}:${BOLD_BLUE}${this.display(this.cwd)}${RESET}$ `
  }

  input(data: string): ShellOutput {
    let out = ''
    for (const ch of data) {
      if (this.esc === 'esc') {
        this.esc = ch === '[' || ch === 'O' ? 'csi' : ''
        continue
      }
      if (this.esc === 'csi') {
        if (ch >= '@' && ch <= '~') this.esc = ''
        continue
      }
      switch (ch) {
        case ESC:
          this.esc = 'esc'
          break
        case '\r':
        case '\n': {
          const cmd = this.line
          this.line = ''
          out += '\r\n'
          const res = this.run(cmd)
          out += res.out
          if (res.exit !== undefined) return { out, exit: res.exit }
          out += this.prompt()
          break
        }
        case '\x7f':
        case '\b':
          if (this.line) {
            this.line = [...this.line].slice(0, -1).join('')
            out += '\b \b'
          }
          break
        case '\x03': // Ctrl+C
          this.line = ''
          this.status = 130
          out += `^C\r\n${this.prompt()}`
          break
        case '\x04': // Ctrl+D on an empty line
          if (!this.line) return { out: `${out}exit\r\n`, exit: 0 }
          break
        default:
          if (ch >= ' ') {
            this.line += ch
            out += ch
          }
      }
    }
    return { out }
  }

  private run(line: string): ShellOutput {
    const [cmd = '', ...args] = splitWords(line)
    const ok = (out: string) => {
      this.status = 0
      return { out }
    }
    switch (cmd) {
      case '':
        return { out: '' }
      case 'pwd':
        return ok(`${this.cwd}\r\n`)
      case 'echo':
        return ok(`${args.join(' ').replaceAll('$?', String(this.status))}\r\n`)
      case 'ls':
        return ok(`${BOLD_BLUE}docs${RESET}  ${BOLD_BLUE}packages${RESET}  ${BOLD_BLUE}src${RESET}  README.md  package.json\r\n`)
      case 'cd':
        this.cwd = this.resolve(args[0] ?? '~')
        return ok('')
      case 'clear':
        return ok(`${ESC}[H${ESC}[2J`)
      case 'help':
        return ok('Commands: ls, pwd, echo <text>, cd <dir>, clear, exit [code]\r\n')
      case 'exit':
        return { out: '', exit: Number.parseInt(args[0] ?? '0', 10) || 0 }
      default:
        this.status = 127
        return { out: `mock: command not found: ${cmd}\r\n` }
    }
  }

  private resolve(dir: string) {
    let path = dir === '~' || dir.startsWith('~/') ? this.home + dir.slice(1) : dir.startsWith('/') ? dir : `${this.cwd}/${dir}`
    const parts: string[] = []
    for (const p of path.split('/')) {
      if (!p || p === '.') continue
      if (p === '..') parts.pop()
      else parts.push(p)
    }
    path = `/${parts.join('/')}`
    return path
  }

  private display(path: string) {
    if (path === this.home) return '~'
    return path.startsWith(`${this.home}/`) ? `~${path.slice(this.home.length)}` : path
  }
}

/** Splits a command line into words; single and double quotes group. */
function splitWords(line: string) {
  const words: string[] = []
  let cur = ''
  let quote = ''
  let has = false
  for (const ch of line) {
    if (quote) {
      if (ch === quote) quote = ''
      else cur += ch
    } else if (ch === '"' || ch === '\'') {
      quote = ch
      has = true
    } else if (ch === ' ' || ch === '\t') {
      if (cur || has) words.push(cur)
      cur = ''
      has = false
    } else {
      cur += ch
    }
  }
  if (cur || has) words.push(cur)
  return words
}

interface MockSocket {
  h: TerminalSocketHandlers
  closed: boolean
}

class MockTerminal {
  info: TerminalInfo
  shell: FakeShell
  buffer = ''
  sockets = new Set<MockSocket>()
  private encoder = new TextEncoder()

  constructor(info: TerminalInfo) {
    this.info = info
    this.shell = new FakeShell(info.cwd)
    this.output(this.shell.banner())
  }

  output(text: string) {
    if (!text) return
    this.buffer += text
    if (this.buffer.length > BUFFER_SIZE) {
      // Cut at a line, like the client does.
      const cut = this.buffer.length - BUFFER_SIZE
      const nl = this.buffer.indexOf('\n', cut)
      this.buffer = this.buffer.slice(nl >= 0 && nl - cut < 4096 ? nl + 1 : cut)
    }
    const bytes = this.encoder.encode(text)
    for (const s of this.sockets) s.h.onMessage(bytes)
  }

  input(text: string) {
    if (!this.info.running) return
    const res = this.shell.input(text)
    this.output(res.out)
    if (res.exit !== undefined) this.exit(res.exit)
  }

  resize(cols: number, rows: number, from?: MockSocket) {
    if (!cols || !rows || (cols === this.info.cols && rows === this.info.rows)) return
    this.info.cols = cols
    this.info.rows = rows
    for (const s of this.sockets) {
      if (s !== from) s.h.onMessage(JSON.stringify({ type: 'resize', cols, rows }))
    }
  }

  exit(code: number) {
    if (!this.info.running) return
    this.info.running = false
    this.info.exitCode = code
    for (const s of [...this.sockets]) this.sendExit(s)
  }

  sendExit(s: MockSocket) {
    s.h.onMessage(JSON.stringify({ type: 'exit', code: this.info.exitCode ?? 0 }))
    this.sockets.delete(s)
    s.closed = true
    s.h.onClose(1000, 'exited')
  }
}

export interface MockTerminalsOptions {
  // The cwd of an open mock session, or undefined when it is not open.
  sessionCwd?: (sessionId: string) => string | undefined
  // Delay before a socket opens (default 0: the next macrotask).
  openDelayMs?: number
}

export class MockTerminalsApi implements TerminalsApi {
  private terms = new Map<string, MockTerminal>()
  private tickets = new Map<string, string>()
  private seq = 0
  private opts: MockTerminalsOptions
  private link: TerminalLink

  constructor(opts: MockTerminalsOptions = {}) {
    this.opts = opts
    this.link = { attach: id => this.attach(id), open: this.open }
  }

  async create(opts: CreateTerminalOptions): Promise<TerminalTicket> {
    let cwd = opts.cwd || MOCK_HOME
    if (opts.sessionId) {
      const root = this.opts.sessionCwd?.(opts.sessionId)
      if (root === undefined) throw new Error(`[not_found] session ${opts.sessionId} is not open`)
      cwd = opts.cwd || root
      if (cwd !== root && !cwd.startsWith(`${root}/`)) throw new Error(`[permission_denied] ${cwd} is outside the session's working directories`)
    }
    if (this.terms.size >= MAX_TERMINALS) {
      const exited = [...this.terms.values()].find(t => !t.info.running)
      if (!exited) throw new Error(`[resource_exhausted] too many terminals: at most ${MAX_TERMINALS} per client; close one first`)
      this.terms.delete(exited.info.id)
    }
    const id = `term_mock${++this.seq}`
    const term = new MockTerminal({
      id,
      sessionId: opts.sessionId ?? '',
      title: opts.title || cwd.split('/').filter(Boolean).pop() || '/',
      cwd,
      shell: '/bin/mock',
      pid: 40000 + this.seq,
      running: true,
      createdAt: new Date().toISOString(),
      cols: opts.cols || 80,
      rows: opts.rows || 24
    })
    this.terms.set(id, term)
    return { terminal: { ...term.info }, ticket: this.ticket(id) }
  }

  async attach(terminalId: string): Promise<TerminalTicket> {
    const term = this.terms.get(terminalId)
    if (!term) throw new TerminalGoneError(terminalId)
    return { terminal: { ...term.info }, ticket: this.ticket(terminalId) }
  }

  async list(sessionId?: string) {
    return [...this.terms.values()].filter(t => !sessionId || t.info.sessionId === sessionId).map(t => ({ ...t.info }))
  }

  async kill(terminalId: string) {
    const term = this.terms.get(terminalId)
    if (!term) throw new Error(`[not_found] terminal ${terminalId} not found`)
    this.terms.delete(terminalId)
    term.exit(SIGHUP_EXIT)
  }

  async resize(terminalId: string, cols: number, rows: number) {
    const term = this.terms.get(terminalId)
    if (!term) throw new Error(`[not_found] terminal ${terminalId} not found`)
    term.resize(cols, rows)
  }

  connect(terminalId: string, ticket?: string, opts?: TerminalConnectionOptions) {
    const conn = new TerminalConnection(this.link, terminalId, ticket, opts)
    void conn.connect()
    return conn
  }

  /** Drops every socket of a terminal, as if the network went away (tests and demos of reconnecting). */
  dropSockets(terminalId: string) {
    const term = this.terms.get(terminalId)
    if (!term) return
    for (const s of [...term.sockets]) {
      term.sockets.delete(s)
      s.closed = true
      s.h.onClose(1006, '')
    }
  }

  private ticket(terminalId: string) {
    const t = `mock-ticket-${terminalId}-${Math.random().toString(36).slice(2)}`
    this.tickets.set(t, terminalId)
    return t
  }

  private open: OpenTerminalSocket = (terminalId, ticket, h) => {
    const s: MockSocket = { h, closed: false }
    const id = this.tickets.get(ticket)
    this.tickets.delete(ticket)
    const decoder = new TextDecoder()
    setTimeout(() => {
      if (s.closed) return
      const term = id === terminalId ? this.terms.get(terminalId) : undefined
      if (!term) {
        // The real socket fails the handshake (401/404); browsers only see an abnormal close.
        s.closed = true
        h.onClose(1006, '')
        return
      }
      h.onOpen()
      if (s.closed) return
      if (term.buffer) h.onMessage(new TextEncoder().encode(term.buffer))
      if (!term.info.running) {
        term.sendExit(s)
        return
      }
      term.sockets.add(s)
    }, this.opts.openDelayMs ?? 0)
    return {
      send: (data) => {
        const term = this.terms.get(terminalId)
        if (s.closed || !term) return
        if (typeof data !== 'string') {
          term.input(decoder.decode(data, { stream: true }))
          return
        }
        let msg: { type?: string, cols?: number, rows?: number, data?: string }
        try {
          msg = JSON.parse(data)
        } catch {
          return
        }
        if (msg.type === 'resize') term.resize(msg.cols ?? 0, msg.rows ?? 0, s)
        else if (msg.type === 'input') term.input(msg.data ?? '')
      },
      close: () => {
        s.closed = true
        this.terms.get(terminalId)?.sockets.delete(s)
      }
    }
  }
}
