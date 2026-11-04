import { describe, expect, it } from 'vitest'
import type { ToolItem } from '~/lib/session/types'
import { createSessionState, reduceAll } from '~/lib/session/reducer'
import { decodeBase64, findingsOf, formatFileSize, NOTIFICATION_FRESH_MS, sentFilesOf, takeUserNotification } from '~/lib/session/userTools'
import { EventBuilder } from '~/lib/transport/events'
import { toolIcon, toolTarget } from '~/utils/tools'

const T0 = Date.parse('2026-11-04T12:00:00Z')

const tool = (name: string, input: Record<string, unknown>, result: Record<string, unknown> | null = null, status: ToolItem['status'] = 'ok'): ToolItem => ({
  kind: 'tool', id: 't', name, input, summary: '', status, output: '', result, resultSummary: '', durationMs: 1, startedAt: 0, messageId: 'm', grouped: false
})

describe('UserNotification', () => {
  it('keeps the latest in the session state, with the event time', () => {
    const b = new EventBuilder('s1', T0)
    const s = createSessionState('s1')
    b.userNotification('t1', 'Done', 'The build ended')
    reduceAll(s, b.events)
    expect(s.userNotification).toMatchObject({ id: 't1', title: 'Done', body: 'The build ended' })
    expect(s.userNotification!.at).toBeGreaterThanOrEqual(T0)
  })

  it('is shown once, and only while it is fresh', () => {
    const n = { id: 't1', title: 'a', body: 'b', at: T0 }
    expect(takeUserNotification(n, [], T0 + 1000)).toEqual(['t1'])
    expect(takeUserNotification(n, ['t1'], T0 + 1000)).toBeNull()
    expect(takeUserNotification(n, [], T0 + NOTIFICATION_FRESH_MS + 1)).toBeNull()
    expect(takeUserNotification(null, [], T0)).toBeNull()
  })
})

describe('SendUserFile', () => {
  const file = { name: 'a.md', path: '/p/a.md', size: 3, mime: 'text/markdown', kind: 'markdown', display: 'inline', sha256: 'x', data: btoa('# a') }

  it('reads the files of a finished call only', () => {
    expect(sentFilesOf(tool('SendUserFile', {}, { files: [file] }))).toEqual([file])
    expect(sentFilesOf(tool('SendUserFile', {}, null, 'running'))).toBeNull()
    expect(sentFilesOf(tool('SendUserFile', {}, { files: [file] }, 'error'))).toBeNull()
    expect(sentFilesOf(tool('Read', {}, { files: [file] }))).toBeNull()
  })

  it('decodes the copy in the result, and labels sizes', () => {
    expect(new TextDecoder().decode(decodeBase64(file.data))).toBe('# a')
    expect(formatFileSize(812)).toBe('812 B')
    expect(formatFileSize(14 << 10)).toBe('14 KB')
    expect(formatFileSize(3.2 * (1 << 20))).toBe('3.2 MB')
  })
})

describe('ReportFindings', () => {
  it('reads the list; an empty list is a result, a missing one is not', () => {
    expect(findingsOf(tool('ReportFindings', {}, { findings: [] }))).toEqual([])
    expect(findingsOf(tool('ReportFindings', {}, null))).toBeNull()
    expect(findingsOf(tool('ReportFindings', {}, { findings: [] }, 'error'))).toBeNull()
  })
})

describe('rows of the new tools', () => {
  it('names what they act on', () => {
    expect(toolTarget(tool('PushNotification', { title: 'Done' }))).toBe('Done')
    expect(toolTarget(tool('SendUserFile', { files: ['a.md', 'b.png'] }))).toBe('a.md, b.png')
    expect(toolIcon(tool('SendUserFile', {}))).toBe('file')
    expect(toolIcon(tool('PushNotification', {}))).toBe('alert')
    expect(toolIcon(tool('ReadNotifications', {}))).toBe('alert')
    expect(toolIcon(tool('ReportFindings', {}))).toBe('search')
  })
})
