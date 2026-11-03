// Sub-sessions in the sidebar: a fork (SessionMeta.parentId) is listed under its parent, indented, and the parent
// row can collapse its children. A session whose parent is not in the same list (archived, deleted, in another
// group or pinned apart) is listed at the top level.

import type { SessionState } from './types'

export interface TreeRow {
  session: SessionState
  // 0 for top-level rows, 1 for a parent's sub-sessions, and so on.
  depth: number
  // Sub-sessions listed under this row (shown or collapsed).
  children: number
  collapsed: boolean
}

// Rows in list order, each parent followed by its sub-sessions (in list order). `collapsed` maps session ids
// whose children are hidden.
export function sessionTree(list: SessionState[], collapsed: Record<string, boolean> = {}): TreeRow[] {
  const ids = new Set(list.map(s => s.meta.id))
  const kids = new Map<string, SessionState[]>()
  const roots: SessionState[] = []
  for (const s of list) {
    const p = s.meta.parentId
    if (p && p !== s.meta.id && ids.has(p)) {
      if (!kids.has(p)) kids.set(p, [])
      kids.get(p)!.push(s)
    } else {
      roots.push(s)
    }
  }
  const out: TreeRow[] = []
  const seen = new Set<string>()
  const walk = (s: SessionState, depth: number) => {
    if (seen.has(s.meta.id)) return
    seen.add(s.meta.id)
    const children = kids.get(s.meta.id) ?? []
    const folded = !!collapsed[s.meta.id] && children.length > 0
    out.push({ session: s, depth, children: children.length, collapsed: folded })
    if (folded) {
      // Hidden rows still count as placed (a parent cycle cannot list them twice).
      const hide = (c: SessionState) => {
        if (seen.has(c.meta.id)) return
        seen.add(c.meta.id)
        for (const k of kids.get(c.meta.id) ?? []) hide(k)
      }
      for (const c of children) hide(c)
      return
    }
    for (const c of children) walk(c, depth + 1)
  }
  for (const r of roots) walk(r, 0)
  // Sessions in a parent cycle (a → b → a) have no root: list them at the top level.
  for (const s of list) if (!seen.has(s.meta.id)) walk(s, 0)
  return out
}

// The first `limit` top-level rows with their sub-sessions; `more` when top-level rows were left out.
export function limitTree(rows: TreeRow[], limit: number): { rows: TreeRow[], more: boolean } {
  let top = 0
  const out: TreeRow[] = []
  for (const r of rows) {
    if (r.depth === 0 && ++top > limit) return { rows: out, more: true }
    out.push(r)
  }
  return { rows: out, more: false }
}

// Every sub-session under a session, at any depth (archive takes them along).
export function descendants(list: SessionState[], id: string): string[] {
  const out: string[] = []
  const stack = [id]
  while (stack.length) {
    const cur = stack.pop()!
    for (const s of list) {
      if (s.meta.parentId === cur && s.meta.id !== id && !out.includes(s.meta.id)) {
        out.push(s.meta.id)
        stack.push(s.meta.id)
      }
    }
  }
  return out
}
