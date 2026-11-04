import { describe, expect, it } from 'vitest'
import type { ToolItem } from '~/lib/session/types'
import { keepAwakeAvailable } from '~/utils/keepAwake'
import { toolIcon, toolMeta, toolTarget } from '~/utils/tools'

const tool = (name: string, input: Record<string, unknown>, result: Record<string, unknown> | null = null, status: ToolItem['status'] = 'ok'): ToolItem => ({
  kind: 'tool', id: 't', name, input, summary: '', status, output: '', result, resultSummary: '', durationMs: 900, startedAt: 0, messageId: 'm', grouped: false
})

describe('who gets the keep-awake switch', () => {
  const local = { remote: false, remoteControllable: false, relayEnabled: false }
  const web = { desktop: false, controlling: false }
  it('shows in the desktop app', () => {
    expect(keepAwakeAvailable(local, { ...web, desktop: true })).toBe(true)
  })
  it('shows on the web only for a session on the relay', () => {
    expect(keepAwakeAvailable(local, web)).toBe(false)
    expect(keepAwakeAvailable({ ...local, relayEnabled: true }, web)).toBe(true)
  })
  it('shows for a session of another machine only while driving it through the relay', () => {
    const remote = { remote: true, remoteControllable: true, relayEnabled: false }
    for (const desktop of [false, true]) {
      expect(keepAwakeAvailable(remote, { desktop, controlling: true })).toBe(true)
      expect(keepAwakeAvailable(remote, { desktop, controlling: false })).toBe(false)
      expect(keepAwakeAvailable({ ...remote, remoteControllable: false }, { desktop, controlling: true })).toBe(false)
    }
  })
})

describe('session and system tool rows', () => {
  it('names what changes', () => {
    expect(toolTarget(tool('SettingsSet', { key: 'theme', value: 'dark' }))).toBe('theme = dark')
    expect(toolTarget(tool('SettingsGet', {}))).toBe('Settings')
    expect(toolTarget(tool('OpenInEditor', { path: 'a.go', line: 3 }))).toBe('a.go:3')
    expect(toolTarget(tool('RevealPath', { path: 'dir' }))).toBe('dir')
    expect(toolTarget(tool('KeepAwake', { enabled: false }))).toBe('Allow sleep')
    expect(toolTarget(tool('SessionSetModel', { model: 'm1' }))).toBe('Model: m1')
    expect(toolTarget(tool('SessionSetEffort', { effort: 'high' }))).toBe('Effort: high')
    expect(toolTarget(tool('SessionSetMode', { mode: 'plan' }))).toBe('Mode: Plan')
    expect(toolTarget(tool('SessionSetMode', { mode: 'acceptEdits' }))).toBe('Mode: Accept edits')
    expect(toolTarget(tool('SessionSetMode', { mode: 'dontAsk' }))).toBe('Mode: Don\'t ask')
    expect(toolTarget(tool('SessionSetTitle', { title: 'T' }))).toBe('Title: T')
  })
  it('has its own icons', () => {
    expect(toolIcon(tool('SettingsSet', {}))).toBe('settings')
    expect(toolIcon(tool('KeepAwake', {}))).toBe('sun')
    expect(toolIcon(tool('SessionSetMode', {}))).toBe('sliders')
  })
})

describe('Wait rows', () => {
  it('shows how long it may wait, then the target (a file by name)', () => {
    expect(toolTarget(tool('Wait', { port: 3000, timeout: 5000 }))).toBe('up to 5s · port 3000')
    expect(toolTarget(tool('Wait', { port: 443, host: 'example.com', timeout: 90000 }))).toBe('up to 1m 30s · port example.com:443')
    expect(toolTarget(tool('Wait', { task_id: 'b1f2', pattern: 'ready', timeout: 60000 }))).toBe('up to 1m · task b1f2 /ready/')
    expect(toolTarget(tool('Wait', { path: '/p/out.log', changed: true, timeout: 5000 }))).toBe('up to 5s · out.log to change')
    expect(toolIcon(tool('Wait', { port: 3000 }))).toBe('clock')
  })

  it('shows the elapsed time; running out the time is a normal end', () => {
    expect(toolMeta(tool('Wait', { port: 3000 }, { met: true, elapsed_ms: 2100 }), 0)).toEqual({ text: 'ready · 2.1s', cls: 'text-ok' })
    expect(toolMeta(tool('Wait', { port: 3000 }, { met: false, elapsed_ms: 30000 }), 0)).toEqual({ text: 'waited 30.0s', cls: 'text-muted' })
  })
})

describe('TaskOutput rows', () => {
  it('names the task and shows its status', () => {
    expect(toolTarget(tool('TaskOutput', { task_id: 'b1f2' }))).toBe('b1f2')
    expect(toolTarget(tool('TaskOutput', { task_id: 'b1f2', pattern: 'Listening' }))).toBe('b1f2 /Listening/')
    expect(toolIcon(tool('TaskOutput', { task_id: 'b1f2' }))).toBe('term')
    expect(toolMeta(tool('TaskOutput', {}, { status: 'running' }), 0).text).toBe('running')
    expect(toolMeta(tool('TaskOutput', {}, { status: 'running', timed_out: true }), 0).text).toBe('running · timed out')
    expect(toolMeta(tool('TaskOutput', {}, { status: 'exited', exit_code: 0 }), 0)).toEqual({ text: 'exit 0', cls: 'text-ok' })
    expect(toolMeta(tool('TaskOutput', {}, { status: 'exited', exit_code: 2 }), 0)).toEqual({ text: 'exit 2', cls: 'text-bad' })
    expect(toolMeta(tool('TaskOutput', {}, { status: 'killed', exit_code: -1 }), 0).text).toBe('killed')
  })
})

describe('tools that move the session', () => {
  it('say where to, with an icon of their own', () => {
    expect(toolTarget(tool('EnterWorktree', { name: 'feat', base_ref: 'main' }))).toBe('Enter worktree feat from main')
    expect(toolTarget(tool('ExitWorktree', { action: 'keep' }))).toBe('Leave worktree')
    expect(toolTarget(tool('ExitWorktree', { action: 'remove', discard_changes: true }))).toBe('Remove worktree, discarding changes')
    expect(toolTarget(tool('ChangeDirectory', { path: '/a' }))).toBe('Move to /a')
    expect(toolTarget(tool('RequestDirectory', { path: '/a' }))).toBe('Ask for /a')
    expect(toolTarget(tool('ProposeBranch', { name: 'b' }))).toBe('Suggest branch b')
    expect(toolTarget(tool('SyncWithBase', {}))).toBe('Sync with base')
    expect(toolIcon(tool('EnterWorktree', {}))).toBe('branch')
    expect(toolIcon(tool('RequestDirectory', {}))).toBe('folder')
  })
})
