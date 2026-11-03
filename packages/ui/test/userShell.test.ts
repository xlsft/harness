import { createPinia, setActivePinia } from 'pinia'
import { describe, expect, it, vi } from 'vitest'
import { createSessionState, reduceAll, sessionMeta } from '~/lib/session/reducer'
import type { UserShellItem } from '~/lib/session/types'
import { EventBuilder } from '~/lib/transport/events'
import { MockTransport } from '~/lib/transport/mock'
import { runMockShell } from '~/lib/transport/mockShell'
import { useSessionStore } from '~/stores/sessions'
import { composerRoute } from '~/utils/composer'

vi.mock('vue', () => ({ markRaw: <T>(v: T) => v }))

const T0 = Date.parse('2026-11-02T12:00:00Z')

describe('user shell events', () => {
  it('maps Started, streamed Output and Finished to one shell item', () => {
    const b = new EventBuilder('s1', T0)
    const s = createSessionState('s1')
    b.userShellStarted('sh1', 'make test', '/repo')
    b.wait(100).userShellOutput('sh1', 'ok 1\n')
    b.wait(100).userShellOutput('sh1', 'ok 2\n')
    reduceAll(s, b.events)
    const item = s.items[0] as UserShellItem
    expect(item).toMatchObject({ kind: 'shell', id: 'sh1', command: 'make test', cwd: '/repo', state: 'running', output: 'ok 1\nok 2\n', startedAt: T0 })
    // A shell command is not a turn.
    expect(s.meta.status).toBe('idle')

    b.wait(300).userShellFinished('sh1', { command: 'make test', cwd: '/repo', exitCode: 2, durationMs: 500, stdout: 'ok 1\nok 2\n', stderr: 'FAIL\n' })
    reduceAll(s, b.events)
    expect(s.items).toHaveLength(1)
    expect(item).toMatchObject({ state: 'error', exitCode: 2, durationMs: 500, output: 'ok 1\nok 2\n', stderr: 'FAIL\n' })
  })

  it('maps stop, time limit and spawn failure, and builds the item from Finished alone (replays)', () => {
    const b = new EventBuilder('s1', T0)
    const s = createSessionState('s1')
    b.userShellFinished('a', { command: 'sleep 30', cwd: '/r', exitCode: -1, durationMs: 1200, reason: 'stopped' })
    b.userShellFinished('b', { command: 'yes', cwd: '/r', exitCode: -1, durationMs: 600000, reason: 'time_limit', truncated: true })
    b.userShellFinished('c', { command: 'x', cwd: '/r', exitCode: -1, durationMs: 0, reason: 'failed', error: 'no shell' })
    b.userShellFinished('d', { command: 'echo hi', cwd: '/r', exitCode: 0, durationMs: 3, stdout: 'hi\n' })
    reduceAll(s, b.events)
    const items = s.items as UserShellItem[]
    expect(items.map(i => i.state)).toEqual(['stopped', 'timeout', 'failed', 'ok'])
    expect(items[0]!.startedAt).toBe(T0 - 1200)
    expect(items[1]!.truncated).toBe(true)
    expect(items[2]!.error).toBe('no shell')
    expect(items[3]!.command).toBe('echo hi')
  })

  it('skips replayed duplicates', () => {
    const b = new EventBuilder('s1', T0)
    const s = createSessionState('s1')
    b.userShellStarted('sh1', 'pwd', '/r')
    b.userShellFinished('sh1', { command: 'pwd', cwd: '/r', exitCode: 0, durationMs: 2, stdout: '/r\n' })
    reduceAll(s, b.events)
    reduceAll(s, b.events)
    expect(s.items).toHaveLength(1)
  })
})

describe('composer routing', () => {
  it('sends shell mode to RunShellCommand, never as a "!" message', () => {
    expect(composerRoute('shell', '  git status ')).toEqual({ kind: 'shell', command: 'git status' })
    expect(composerRoute('memory', 'use pnpm', 'AGENTS.md')).toEqual({ kind: 'message', text: '# use pnpm (save to AGENTS.md)' })
    expect(composerRoute('normal', 'hello')).toEqual({ kind: 'message', text: 'hello' })
    expect(composerRoute('shell', '   ')).toBeNull()
  })

  it('runs the command through the store without a message or a turn', async () => {
    setActivePinia(createPinia())
    const store = useSessionStore()
    const transport = new MockTransport({ tick: 0 })
    store.transport = transport
    const [info] = await transport.listSessions()
    const id = info!.id
    store.sessions[id] = createSessionState(id, sessionMeta(info!))
    const sent = vi.spyOn(transport, 'sendUserMessage')
    const events: string[] = []
    transport.subscribe(id, Number.MAX_SAFE_INTEGER, { onEvent: (ev) => {
      if (ev.event.case !== 'assistantDelta') events.push(ev.event.case ?? '')
    } })
    store.subscribe(id)
    await new Promise(resolve => setTimeout(resolve, 0))

    await store.runShell(id, 'echo hello')
    expect(sent).not.toHaveBeenCalled()
    expect(events).toEqual(['userShellStarted', 'userShellFinished'])
    expect(store.sessions[id]!.items.filter(i => i.kind === 'shell')).toEqual([expect.objectContaining({ command: 'echo hello', output: 'hello\n', state: 'ok' })])
  })
})

describe('mock shell', () => {
  it('knows echo, pwd, cd and failing commands', () => {
    expect(runMockShell('echo hello', '/r')).toMatchObject({ stdout: 'hello\n', exitCode: 0 })
    expect(runMockShell('cd src && pwd', '/r')).toMatchObject({ stdout: '/r/src\n', cwd: '/r/src' })
    expect(runMockShell('false && echo no', '/r')).toMatchObject({ stdout: '', exitCode: 1 })
    expect(runMockShell('nope', '/r')).toMatchObject({ stderr: 'sh: nope: command not found\n', exitCode: 127 })
    expect(runMockShell('echo oops >&2; exit 3', '/r')).toMatchObject({ stderr: 'oops\n', exitCode: 3 })
  })
})
