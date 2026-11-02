// Arrangement of the open right panes: a tree of rows and columns, rendered with Reka UI's Splitter.
// Which panes are open stays in the URL (usePanes); how they are arranged is a per-browser preference
// kept in localStorage. The tree is reconciled with the open set on every change: closed panes drop out,
// newly opened ones join the outer row on the right.
import type { PaneName } from '~/composables/usePanes'

export type SplitDir = 'row' | 'col'
export type LayoutNode = { kind: 'pane', pane: PaneName } | { kind: 'split', dir: SplitDir, children: LayoutNode[] }
// Where a dragged pane lands on a target pane: one of its edges, or its place (swap).
export type DropRegion = 'left' | 'right' | 'top' | 'bottom' | 'center'

const STORAGE_KEY = 'harness-pane-layout'

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

function load(): LayoutNode | null {
  if (!import.meta.client) return null
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    return raw ? JSON.parse(raw) as LayoutNode : null
  } catch {
    return null
  }
}

function save(t: LayoutNode | null) {
  try {
    if (t) localStorage.setItem(STORAGE_KEY, JSON.stringify(t))
    else localStorage.removeItem(STORAGE_KEY)
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
  const stored = useState<LayoutNode | null>('pane-layout', load)
  const drag = useState<PaneDrag | null>('pane-drag', () => null)

  const tree = computed(() => reconcile(stored.value, panes.value))

  function move(src: PaneName, target: PaneName, region: DropRegion) {
    stored.value = movePane(tree.value, src, target, region)
    save(stored.value)
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

  return { tree, drag, move, startDrag }
}
