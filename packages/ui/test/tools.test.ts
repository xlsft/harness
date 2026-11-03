import { describe, expect, it } from 'vitest'
import type { ToolItem } from '~/lib/session/types'
import { toolIcon, toolMeta, toolTarget } from '~/utils/tools'

const tool = (name: string, input: Record<string, unknown>, result: Record<string, unknown> | null = null, status: ToolItem['status'] = 'ok'): ToolItem => ({
  kind: 'tool', id: 't', name, input, summary: '', status, output: '', result, resultSummary: '', durationMs: 900, startedAt: 0, messageId: 'm', grouped: false
})

describe('Wait rows', () => {
  it('names the target', () => {
    expect(toolTarget(tool('Wait', { port: 3000, timeout: 5000 }))).toBe('port 3000')
    expect(toolTarget(tool('Wait', { port: 443, host: 'example.com', timeout: 5000 }))).toBe('port example.com:443')
    expect(toolTarget(tool('Wait', { task_id: 'b1f2', pattern: 'ready', timeout: 5000 }))).toBe('task b1f2 /ready/')
    expect(toolTarget(tool('Wait', { path: '/p/out.log', changed: true, timeout: 5000 }))).toBe('/p/out.log (change)')
    expect(toolIcon(tool('Wait', { port: 3000 }))).toBe('clock')
  })

  it('shows the elapsed time, or that it timed out', () => {
    expect(toolMeta(tool('Wait', { port: 3000 }, { met: true, elapsed_ms: 2100 }), 0)).toEqual({ text: '2.1s', cls: 'text-ok' })
    expect(toolMeta(tool('Wait', { port: 3000 }, { met: false, elapsed_ms: 30000 }), 0)).toEqual({ text: 'timed out · 30.0s', cls: 'text-bad' })
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
