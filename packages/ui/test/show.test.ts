import { describe, expect, it } from 'vitest'
import { takeShowRequest } from '~/composables/useShow'
import { createSessionState, reduceAll } from '~/lib/session/reducer'
import { EventBuilder } from '~/lib/transport/events'
import { showLabel, showPane, showTargetOf } from '~/utils/show'

const T0 = Date.parse('2026-11-02T12:00:00Z')

describe('ShowRequested in the reducer', () => {
  it('keeps the latest request of the turn, with its target', () => {
    const b = new EventBuilder('s1', T0)
    const s = createSessionState('s1')
    b.user('u1', 'where is it?')
    b.show('t1', { file: '/p/src/x.go', lines: [10, 20] })
    reduceAll(s, b.events)
    expect(s.showRequest).toEqual({ id: 't1', target: { kind: 'file', path: '/p/src/x.go', lineStart: 10, lineEnd: 20 } })

    b.show('t2', { diff: '' })
    b.show('t3', { url: 'http://localhost:3000/' })
    reduceAll(s, b.events.slice(-2))
    expect(s.showRequest).toEqual({ id: 't3', target: { kind: 'url', url: 'http://localhost:3000/' } })
  })

  it('drops a request the user moved on from, and ignores replayed duplicates', () => {
    const b = new EventBuilder('s1', T0)
    const s = createSessionState('s1')
    b.show('t1', { diff: '/p/a.go' })
    b.user('u2', 'thanks')
    reduceAll(s, b.events)
    expect(s.showRequest).toBeNull()
    // The same events again (a resubscribe replay) change nothing.
    reduceAll(s, b.events.slice(0, 1))
    expect(s.showRequest).toBeNull()
  })
})

describe('takeShowRequest', () => {
  it('applies a request once', () => {
    const req = { id: 't1', target: { kind: 'diff' as const, path: '' } }
    expect(takeShowRequest(null, [])).toBeNull()
    expect(takeShowRequest(req, ['t0'])).toEqual(['t0', 't1'])
    expect(takeShowRequest(req, ['t0', 't1'])).toBeNull()
  })

  it('keeps a bounded list of applied ids', () => {
    const many = Array.from({ length: 200 }, (_, i) => `t${i}`)
    const next = takeShowRequest({ id: 'new', target: { kind: 'url', url: 'http://x' } }, many)!
    expect(next).toHaveLength(200)
    expect(next[0]).toBe('t1')
    expect(next.at(-1)).toBe('new')
  })
})

describe('Show targets', () => {
  it('reads a Show call, preferring the validated result', () => {
    expect(showTargetOf({ target: 'file', path: 'src/x.go', line: 3, end_line: 99 }, { path: '/p/src/x.go', line: 3, end_line: 40 }, '/p'))
      .toEqual({ kind: 'file', path: '/p/src/x.go', lineStart: 3, lineEnd: 40 })
    expect(showTargetOf({ target: 'file', path: './src/x.go', line: 5 }, null, '/p/'))
      .toEqual({ kind: 'file', path: '/p/src/x.go', lineStart: 5, lineEnd: 0 })
    expect(showTargetOf({ target: 'file', path: 'x.go', end_line: 5 }, null, '/p')).toEqual({ kind: 'file', path: '/p/x.go', lineStart: 0, lineEnd: 0 })
    expect(showTargetOf({ target: 'file', path: 'x.go', line: 9, end_line: 2 }, null, '/p')).toEqual({ kind: 'file', path: '/p/x.go', lineStart: 9, lineEnd: 0 })
    expect(showTargetOf({ target: 'diff' }, null, '/p')).toEqual({ kind: 'diff', path: '' })
    expect(showTargetOf({ target: 'url', url: 'http://localhost:5173/a' }, null, '/p')).toEqual({ kind: 'url', url: 'http://localhost:5173/a' })
    expect(showTargetOf({ target: 'url', url: 'javascript:alert(1)' }, null, '/p')).toBeNull()
    expect(showTargetOf({ target: 'file' }, null, '/p')).toBeNull()
    expect(showTargetOf({ target: 'folder', path: 'x' }, null, '/p')).toBeNull()
  })

  it('labels targets and names their pane', () => {
    expect(showLabel({ kind: 'file', path: '/p/src/x.go', lineStart: 10, lineEnd: 20 }, '/p')).toBe('src/x.go:10-20')
    expect(showLabel({ kind: 'file', path: '/p/src/x.go', lineStart: 10, lineEnd: 10 }, '/p')).toBe('src/x.go:10')
    expect(showLabel({ kind: 'file', path: '/other/x.go', lineStart: 0, lineEnd: 0 }, '/p')).toBe('/other/x.go')
    expect(showLabel({ kind: 'diff', path: '/p/a.go' }, '/p')).toBe('Diff a.go')
    expect(showLabel({ kind: 'diff', path: '' }, '/p')).toBe('Diff')
    expect(showPane({ kind: 'file', path: 'x', lineStart: 0, lineEnd: 0 })).toBe('files')
    expect(showPane({ kind: 'diff', path: '' })).toBe('diff')
    expect(showPane({ kind: 'url', url: 'http://x' })).toBe('browser')
  })
})
