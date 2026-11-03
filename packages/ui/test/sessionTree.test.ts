import { describe, expect, it } from 'vitest'
import { createSessionState } from '~/lib/session/reducer'
import { descendants, limitTree, sessionTree } from '~/lib/session/tree'

const s = (id: string, parentId = '') => createSessionState(id, { title: id, parentId })
const rows = (list: ReturnType<typeof sessionTree>) => list.map(r => `${'  '.repeat(r.depth)}${r.session.meta.id}${r.children ? `(${r.children}${r.collapsed ? ' folded' : ''})` : ''}`)

describe('sidebar tree', () => {
  const list = [s('a'), s('b'), s('a1', 'a'), s('c'), s('a2', 'a'), s('a1x', 'a1'), s('orphan', 'gone')]

  it('lists sub-sessions under their parent in list order, orphans at the top level', () => {
    expect(rows(sessionTree(list))).toEqual(['a(2)', '  a1(1)', '    a1x', '  a2', 'b', 'c', 'orphan'])
  })

  it('folds a parent: its sub-sessions (at any depth) are hidden', () => {
    expect(rows(sessionTree(list, { a: true }))).toEqual(['a(2 folded)', 'b', 'c', 'orphan'])
    expect(rows(sessionTree(list, { a1: true }))).toEqual(['a(2)', '  a1(1 folded)', '  a2', 'b', 'c', 'orphan'])
    // Folding a row without sub-sessions changes nothing.
    expect(rows(sessionTree(list, { b: true }))).toEqual(rows(sessionTree(list)))
  })

  it('makes a detached session a top-level row', () => {
    const detached = list.map(x => x.meta.id === 'a2' ? s('a2') : x)
    expect(rows(sessionTree(detached))).toEqual(['a(1)', '  a1(1)', '    a1x', 'b', 'c', 'a2', 'orphan'])
  })

  it('survives a parent cycle', () => {
    expect(rows(sessionTree([s('x', 'y'), s('y', 'x')]))).toEqual(['x(1)', '  y(1)'])
  })

  it('limits by top-level rows and keeps sub-sessions with their parent', () => {
    const t = sessionTree(list)
    expect(rows(limitTree(t, 1).rows)).toEqual(['a(2)', '  a1(1)', '    a1x', '  a2'])
    expect(limitTree(t, 1).more).toBe(true)
    expect(limitTree(t, 10).more).toBe(false)
  })

  it('finds every sub-session of a session', () => {
    expect(descendants(list, 'a').sort()).toEqual(['a1', 'a1x', 'a2'])
    expect(descendants(list, 'b')).toEqual([])
  })
})
