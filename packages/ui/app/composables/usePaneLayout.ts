// Arrangement of the open right panes: a tree of rows and columns, rendered with Reka UI's Splitter.
// Which panes are open (usePanes) and how they are arranged are kept per session in localStorage. The tree is reconciled with the open set on every change: closed panes drop out,
// newly opened ones join the outer row on the right.
import type { PaneName } from '~/composables/usePanes'

export type SplitDir = 'row' | 'col'
export type LayoutNode = { kind: 'pane', pane: PaneName } | { kind: 'split', dir: SplitDir, children: LayoutNode[] }
// Where a dragged pane lands on a target pane: one of its edges, or its place (swap).
export type DropRegion = 'left' | 'right' | 'top' | 'bottom' | 'center'

const STORAGE_KEY = 'harness-pane-layouts'

const leaf = (pane: PaneName): LayoutNode => ({ kind: 'pane', pane })

export function panesOf(n: LayoutNode | null): PaneName[] {
  if (!n) return []
  return n.kind === 'pane' ? [n.pane] : n.children.flatMap(panesOf)
}

// Stable id of a node (its panes in order), used for Splitter panel ids and saved sizes.
export const nodeKey = (n: LayoutNode): string => panesOf(n).join('+')

// Collapses one-child splits and merges a split into a parent of the same direction.
function simplify(n: LayoutNode | null): LayoutNode | null {
  if (!n || n.kind === 'pane') return n
  const children = n.children.map(simplify).filter((c): c is LayoutNode => !!c)
    .flatMap(c => c.kind === 'split' && c.dir === n.dir ? c.children : [c])
  if (children.length === 0) return null
  if (children.length === 1) return children[0]!
  return { kind: 'split', dir: n.dir, children }
}

function without(n: LayoutNode | null, pane: PaneName): LayoutNode | null {
  if (!n) return null
  if (n.kind === 'pane') return n.pane === pane ? null : n
  return simplify({ ...n, children: n.children.map(c => without(c, pane)).filter((c): c is LayoutNode => !!c) })
}

function mapLeaves(n: LayoutNode, f: (pane: PaneName) => LayoutNode): LayoutNode {
  return n.kind === 'pane' ? f(n.pane) : { ...n, children: n.children.map(c => mapLeaves(c, f)) }
}

/** The tree restricted to the open panes, with open panes missing from it appended to the outer row. */
export function reconcile(tree: LayoutNode | null, open: PaneName[]): LayoutNode | null {
  let t = tree
  for (const p of panesOf(tree)) {
    if (!open.includes(p)) t = without(t, p)
  }
  const seen = new Set(panesOf(t))
  for (const p of open) {
    if (seen.has(p)) continue
    t = !t ? leaf(p) : t.kind === 'split' && t.dir === 'row' ? { ...t, children: [...t.children, leaf(p)] } : { kind: 'split', dir: 'row', children: [t, leaf(p)] }
  }
  return simplify(t)
}

// Narrowest a pane can usefully be; the layout adapts to the pane area's width with these.
export const PANE_MIN: Partial<Record<PaneName, number>> = { details: 240 }
const DEFAULT_MIN = 320
const minOf = (p: PaneName) => PANE_MIN[p] ?? DEFAULT_MIN

/** Width a node needs: side by side the minimums add up, stacked the widest one counts. */
export function minWidth(n: LayoutNode | null): number {
  if (!n) return 0
  if (n.kind === 'pane') return minOf(n.pane)
  const ws = n.children.map(minWidth)
  return n.dir === 'row' ? ws.reduce((a, b) => a + b, 0) : Math.max(...ws)
}

/**
 * Adds a newly opened pane: a new column when the pane area has room for it (details stays the first
 * column, next to the transcript), otherwise stacked under the last content column. width 0 (not measured yet) means room.
 */
export function placeNew(tree: LayoutNode | null, pane: PaneName, width: number): LayoutNode {
  if (!tree) return leaf(pane)
  const cols = tree.kind === 'split' && tree.dir === 'row' ? tree.children : [tree]
  const isDetails = (c: LayoutNode) => c.kind === 'pane' && c.pane === 'details'
  if (pane === 'details') return simplify({ kind: 'split', dir: 'row', children: [leaf(pane), ...cols] })!
  const fits = !width || minWidth(tree) + minOf(pane) <= width
  const content = cols.filter(c => !isDetails(c))
  const last = content[content.length - 1]
  if (fits || !last) {
    return simplify({ kind: 'split', dir: 'row', children: [...cols, leaf(pane)] })!
  }
  const stacked: LayoutNode = last.kind === 'split' && last.dir === 'col'
    ? { ...last, children: [...last.children, leaf(pane)] }
    : { kind: 'split', dir: 'col', children: [last, leaf(pane)] }
  return simplify({ kind: 'split', dir: 'row', children: cols.map(c => c === last ? stacked : c) })!
}

/** The tree as shown in `width`: details steps aside when the panes would not fit with it. */
export function fitToWidth(tree: LayoutNode | null, width: number): LayoutNode | null {
  if (!tree || !width || minWidth(tree) <= width) return tree
  const ps = panesOf(tree)
  if (!ps.includes('details') || ps.length < 2) return tree
  return reconcile(tree, ps.filter(p => p !== 'details'))
}

/** Moves pane `src` next to (or in place of) pane `target`. */
export function movePane(tree: LayoutNode | null, src: PaneName, target: PaneName, region: DropRegion): LayoutNode | null {
  if (!tree || src === target) return tree
  if (region === 'center') return mapLeaves(tree, p => leaf(p === src ? target : p === target ? src : p))
  const dir: SplitDir = region === 'left' || region === 'right' ? 'row' : 'col'
  const before = region === 'left' || region === 'top'
  const insert = (n: LayoutNode): LayoutNode => {
    if (n.kind === 'pane') {
      return n.pane === target ? { kind: 'split', dir, children: before ? [leaf(src), n] : [n, leaf(src)] } : n
    }
    // Same direction as the parent: become a sibling instead of nesting a new split.
    const i = n.children.findIndex(c => c.kind === 'pane' && c.pane === target)
    if (i >= 0 && n.dir === dir) {
      const children = [...n.children]
      children.splice(before ? i : i + 1, 0, leaf(src))
      return { ...n, children }
    }
    return { ...n, children: n.children.map(insert) }
  }
  return simplify(insert(without(tree, src)!))
}

// Arrangements per session id.
function loadAll(): Record<string, LayoutNode> {
  if (!import.meta.client) return {}
  try {
    return JSON.parse(localStorage.getItem(STORAGE_KEY) ?? '{}') as Record<string, LayoutNode>
  } catch {
    return {}
  }
}

function saveAll(all: Record<string, LayoutNode>) {
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(all))
  } catch {
    // Storage unavailable (private mode): the arrangement just is not remembered.
  }
}

/** The drop region of a point inside a pane's rect: the nearest edge within 30%, else its middle. */
export function regionAt(r: DOMRect, x: number, y: number): DropRegion {
  const fx = (x - r.left) / r.width
  const fy = (y - r.top) / r.height
  const edges: [DropRegion, number][] = [['left', fx], ['right', 1 - fx], ['top', fy], ['bottom', 1 - fy]]
  const [nearest, dist] = edges.reduce((a, b) => b[1] < a[1] ? b : a)
  return dist < 0.3 ? nearest : 'center'
}

export interface PaneDrag {
  src: PaneName
  target: PaneName | null
  region: DropRegion | null
}

// Pointer travel before a press on a pane header turns into a drag.
const DRAG_THRESHOLD = 5

export function usePaneLayout() {
  const { panes } = usePanes()
  const store = useSessionStore()
  const all = useState<Record<string, LayoutNode>>('pane-layouts', loadAll)
  const sid = computed(() => store.currentId || '_')
  // This session's arrangement (each session keeps its own).
  const stored = computed<LayoutNode | null>({
    get: () => all.value[sid.value] ?? null,
    set: (t) => {
      const next = Object.fromEntries(Object.entries(all.value).filter(([id]) => id !== sid.value))
      if (t) next[sid.value] = t
      all.value = next
      saveAll(next)
    }
  })
  const drag = useState<PaneDrag | null>('pane-drag', () => null)
  // Width of the pane area (measured by the page); 0 until known.
  const width = useState<number>('pane-area-width', () => 0)

  // A pane opened since the last arrangement gets its place now, from the space available at that moment,
  // and keeps it (stored), so it does not jump when the width changes later.
  if (import.meta.client) {
    watch(panes, (open) => {
      const known = new Set(panesOf(stored.value))
      const added = open.filter(p => !known.has(p))
      if (!added.length) return
      let t = reconcile(stored.value, open.filter(p => known.has(p)))
      for (const p of added) t = placeNew(t, p, width.value)
      stored.value = t
    }, { immediate: true })
  }

  // The full arrangement of the open panes, and what fits the pane area right now.
  const arranged = computed(() => reconcile(stored.value, panes.value))
  const tree = computed(() => fitToWidth(arranged.value, width.value))

  function move(src: PaneName, target: PaneName, region: DropRegion) {
    stored.value = movePane(arranged.value, src, target, region)
  }

  // Pointer-driven drag (works for mouse, pen and touch): panes are found under the pointer by their
  // data-pane attribute, so no per-pane listeners are needed while dragging.
  function startDrag(src: PaneName, down: PointerEvent) {
    const x0 = down.clientX
    const y0 = down.clientY
    const onMove = (e: PointerEvent) => {
      if (!drag.value) {
        if (Math.hypot(e.clientX - x0, e.clientY - y0) < DRAG_THRESHOLD) return
        drag.value = { src, target: null, region: null }
        document.body.style.cursor = 'grabbing'
        document.body.style.userSelect = 'none'
      }
      const el = document.elementFromPoint(e.clientX, e.clientY)?.closest<HTMLElement>('[data-pane]')
      const target = el?.dataset.pane as PaneName | undefined
      drag.value = target && target !== src
        ? { src, target, region: regionAt(el!.getBoundingClientRect(), e.clientX, e.clientY) }
        : { src, target: null, region: null }
    }
    const onUp = () => {
      window.removeEventListener('pointermove', onMove)
      window.removeEventListener('pointerup', onUp)
      window.removeEventListener('pointercancel', onUp)
      const d = drag.value
      if (d?.target && d.region) move(d.src, d.target, d.region)
      drag.value = null
      document.body.style.cursor = ''
      document.body.style.userSelect = ''
    }
    window.addEventListener('pointermove', onMove)
    window.addEventListener('pointerup', onUp)
    window.addEventListener('pointercancel', onUp)
  }

  return { tree, drag, move, startDrag, width }
}
