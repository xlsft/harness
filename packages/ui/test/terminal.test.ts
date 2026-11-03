import { describe, expect, it } from 'vitest'
import { FakeShell, MockTerminalsApi } from '~/lib/terminal/mock'
import { TerminalConnection, TerminalGoneError, terminalSocketUrl, type TerminalLink, type TerminalSocketHandlers } from '~/lib/terminal/connection'
import { MockTransport } from '~/lib/transport/mock'

const tick = (ms = 0) => new Promise(r => setTimeout(r, ms))
// eslint-disable-next-line no-control-regex
const plain = (s: string) => s.replace(/\x1b\[[0-9;]*[A-Za-z]/g, '')

// Collects a connection's output as text.
function collect(conn: TerminalConnection) {
  const decoder = new TextDecoder()
  const seen = { text: '', attaches: 0, exits: [] as number[], resizes: [] as [number, number][] }
  conn.onData((d) => {
    seen.text += decoder.decode(d, { stream: true })
  })
  conn.onAttach(() => {
    seen.attaches++
    seen.text = ''
  })
  conn.onExit(code => seen.exits.push(code))
  conn.onResize((c, r) => seen.resizes.push([c, r]))
  return seen
}

describe('FakeShell', () => {
  it('echoes input, edits the line and runs ls, pwd, echo and cd', () => {
    const sh = new FakeShell('/Users/xlsft/Projects/demo')
    expect(plain(sh.prompt())).toBe('mock:~/Projects/demo$ ')
    expect(sh.input('pwdd').out).toBe('pwdd')
    expect(sh.input('\x7f').out).toBe('\b \b')
    expect(plain(sh.input('\r').out)).toBe('\r\n/Users/xlsft/Projects/demo\r\nmock:~/Projects/demo$ ')
    expect(plain(sh.input('echo "hello  world" again\r').out)).toContain('\r\nhello  world again\r\n')
    expect(plain(sh.input('ls\r').out)).toContain('README.md')
    sh.input('cd ../other\r')
    expect(sh.cwd).toBe('/Users/xlsft/Projects/other')
    // Arrow keys are swallowed; unknown commands fail.
    expect(sh.input('\x1b[A').out).toBe('')
    expect(plain(sh.input('nope\r').out)).toContain('mock: command not found: nope')
    expect(plain(sh.input('echo $?\r').out)).toContain('127')
    expect(sh.input('exit 4\r')).toEqual({ out: 'exit 4\r\n', exit: 4 })
  })
})

describe('MockTerminalsApi', () => {
  it('creates a terminal, streams I/O and reports the exit', async () => {
    const api = new MockTerminalsApi()
    const { terminal, ticket } = await api.create({ cwd: '/Users/xlsft/proj', cols: 100, rows: 30 })
    expect(terminal).toMatchObject({ cwd: '/Users/xlsft/proj', title: 'proj', running: true, cols: 100, rows: 30, sessionId: '' })
    const conn = api.connect(terminal.id, ticket)
    const seen = collect(conn)
    conn.write('pw') // queued until the socket opens
    await conn.connect()
    expect(conn.state).toBe('open')
    conn.write('d\r')
    expect(plain(seen.text)).toContain('Mock shell')
    expect(plain(seen.text)).toContain('$ pwd\r\n/Users/xlsft/proj\r\n')

    conn.resize(120, 40)
    expect((await api.list())[0]).toMatchObject({ cols: 120, rows: 40 })

    conn.write(new TextEncoder().encode('exit 3\r'))
    expect(seen.exits).toEqual([3])
    expect(conn.state).toBe('exited')
    expect(conn.exitCode).toBe(3)
    expect((await api.list())[0]).toMatchObject({ running: false, exitCode: 3 })
  })

  it('replays the output on reconnect and keeps the shell running while disconnected', async () => {
    const api = new MockTerminalsApi()
    const { terminal, ticket } = await api.create({})
    const conn = api.connect(terminal.id, ticket, { baseDelayMs: 1 })
    const seen = collect(conn)
    await conn.connect()
    conn.write('echo first\r')
    expect(seen.attaches).toBe(1)

    // The network drops: the connection gets a new ticket, reconnects and the pane gets the replay.
    const states: string[] = []
    conn.onStateChange(s => states.push(s))
    api.dropSockets(terminal.id)
    expect(conn.state).toBe('reconnecting')
    conn.write('echo second\r') // typed while reconnecting
    await tick(20)
    expect(conn.state).toBe('open')
    expect(states).toEqual(['reconnecting', 'open'])
    expect(seen.attaches).toBe(2)
    expect(plain(seen.text)).toContain('first\r\n')
    expect(plain(seen.text)).toContain('second\r\n')

    // A second pane on the same terminal gets the same replay and sees the other's resize.
    const other = api.connect(terminal.id)
    const seen2 = collect(other)
    await other.connect()
    expect(plain(seen2.text)).toContain('first\r\n')
    conn.resize(90, 20)
    expect(seen2.resizes).toEqual([[90, 20]])
    expect(seen.resizes).toEqual([])

    // close() disconnects this pane only.
    conn.close()
    expect(conn.state).toBe('closed')
    expect((await api.list())[0]!.running).toBe(true)
    other.write('echo still here\r')
    expect(plain(seen2.text)).toContain('still here')
  })

  it('kills terminals, refuses used tickets and stops reconnecting when the terminal is gone', async () => {
    const api = new MockTerminalsApi()
    const { terminal, ticket } = await api.create({})
    const conn = api.connect(terminal.id, ticket)
    const seen = collect(conn)
    await conn.connect()
    await api.kill(terminal.id)
    expect(seen.exits).toEqual([129])
    expect(await api.list()).toEqual([])
    await expect(api.kill(terminal.id)).rejects.toThrow('not_found')
    await expect(api.attach(terminal.id)).rejects.toBeInstanceOf(TerminalGoneError)

    // A reused ticket fails the handshake; the connection retries with a fresh one.
    const t2 = await api.create({})
    const first = api.connect(t2.terminal.id, t2.ticket)
    await first.connect()
    const reuse = api.connect(t2.terminal.id, t2.ticket, { baseDelayMs: 1 })
    await reuse.connect()
    expect(reuse.state).toBe('open')

    const gone = api.connect('term_nope', 'bogus', { baseDelayMs: 1 })
    await expect(gone.connect()).rejects.toBeInstanceOf(TerminalGoneError)
    expect(gone.state).toBe('closed')
  })

  it('checks session folders and caps the terminals', async () => {
    const api = new MockTerminalsApi({ sessionCwd: id => (id === 's1' ? '/work/app' : undefined) })
    expect((await api.create({ sessionId: 's1' })).terminal.cwd).toBe('/work/app')
    expect((await api.create({ sessionId: 's1', cwd: '/work/app/src' })).terminal.cwd).toBe('/work/app/src')
    await expect(api.create({ sessionId: 's1', cwd: '/work/apples' })).rejects.toThrow('permission_denied')
    await expect(api.create({ sessionId: 'gone' })).rejects.toThrow('not_found')
    expect((await api.list('s1')).length).toBe(2)

    for (let i = 0; i < 14; i++) await api.create({})
    await expect(api.create({})).rejects.toThrow('resource_exhausted')
  })

  it('is part of the mock transport', async () => {
    const transport = new MockTransport()
    const [session] = await transport.listSessions()
    const { terminal } = await transport.terminals().create({ sessionId: session!.id })
    expect(terminal.cwd).toBe(session!.cwd)
  })
})

describe('TerminalConnection', () => {
  it('gives up after too many failed attempts', async () => {
    let attaches = 0
    const link: TerminalLink = {
      attach: async () => ({ ticket: `t${++attaches}` }),
      open: (_id, _ticket, h: TerminalSocketHandlers) => {
        setTimeout(() => h.onClose(1006, ''), 0)
        return { send: () => {}, close: () => {} }
      }
    }
    const conn = new TerminalConnection(link, 'term_x', undefined, { maxAttempts: 2, baseDelayMs: 1 })
    await expect(conn.connect()).rejects.toThrow('1006')
    expect(conn.state).toBe('closed')
    expect(attaches).toBe(3)
  })

  it('builds the socket URL without the ticket', () => {
    expect(terminalSocketUrl('http://127.0.0.1:7801')).toBe('ws://127.0.0.1:7801/harness.agent.v1.Terminal/socket')
    expect(terminalSocketUrl('https://host.localhost/agent/?x=1')).toBe('wss://host.localhost/agent/harness.agent.v1.Terminal/socket')
  })
})
