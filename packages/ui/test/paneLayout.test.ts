import { describe, expect, it } from 'vitest'
import { fitToWidth, minWidth, movePane, panesOf, placeNew, reconcile, regionAt, type LayoutNode } from '~/composables/usePaneLayout'

const row = (...children: LayoutNode[]): LayoutNode => ({ kind: 'split', dir: 'row', children })
const col = (...children: LayoutNode[]): LayoutNode => ({ kind: 'split', dir: 'col', children })
const p = (pane: 'terminal' | 'browser' | 'files' | 'details' | 'diff'): LayoutNode => ({ kind: 'pane', pane })

describe('reconcile', () => {
  it('appends newly opened panes to the outer row and drops closed ones', () => {
    expect(reconcile(null, ['terminal'])).toEqual(p('terminal'))
    expect(reconcile(p('terminal'), ['terminal', 'browser'])).toEqual(row(p('terminal'), p('browser')))
    expect(reconcile(col(p('terminal'), p('browser')), ['terminal', 'browser', 'details']))
      .toEqual(row(col(p('terminal'), p('browser')), p('details')))
    expect(reconcile(row(col(p('terminal'), p('browser')), p('details')), ['browser', 'details']))
      .toEqual(row(p('browser'), p('details')))
    expect(reconcile(p('terminal'), [])).toBeNull()
  })
})

describe('movePane', () => {
  const start = row(p('terminal'), p('browser'), p('details'))

  it('stacks a pane under another (new column split)', () => {
    expect(movePane(start, 'browser', 'terminal', 'bottom')).toEqual(row(col(p('terminal'), p('browser')), p('details')))
  })

  it('becomes a sibling when the parent already runs that way', () => {
    expect(movePane(start, 'details', 'terminal', 'left')).toEqual(row(p('details'), p('terminal'), p('browser')))
  })

  it('swaps on the centre and keeps every pane exactly once', () => {
    const t = movePane(start, 'terminal', 'details', 'center')
    expect(t).toEqual(row(p('details'), p('browser'), p('terminal')))
    const nested = movePane(movePane(start, 'browser', 'terminal', 'top'), 'details', 'browser', 'right')
    expect(panesOf(nested).sort()).toEqual(['browser', 'details', 'terminal'])
    expect(nested).toEqual(col(row(p('browser'), p('details')), p('terminal')))
  })

  it('ignores a drop on itself', () => {
    expect(movePane(start, 'browser', 'browser', 'left')).toBe(start)
  })
})

describe('regionAt', () => {
  const r = { left: 0, top: 0, width: 100, height: 100 } as DOMRect
  it('picks the nearest edge within 30%, else the centre', () => {
    expect(regionAt(r, 5, 50)).toBe('left')
    expect(regionAt(r, 95, 50)).toBe('right')
    expect(regionAt(r, 50, 10)).toBe('top')
    expect(regionAt(r, 50, 90)).toBe('bottom')
    expect(regionAt(r, 50, 50)).toBe('center')
  })
})

describe('placeNew', () => {
  it('adds a column after the others (details stays first) when there is room', () => {
    expect(placeNew(row(p('details'), p('terminal')), 'browser', 1200)).toEqual(row(p('details'), p('terminal'), p('browser')))
    expect(placeNew(null, 'diff', 0)).toEqual(p('diff'))
  })

  it('stacks under the last content column when a column does not fit', () => {
    // terminal 320 + details 240 = 560; one more column needs 880
    expect(placeNew(row(p('details'), p('terminal')), 'browser', 700)).toEqual(row(p('details'), col(p('terminal'), p('browser'))))
    expect(placeNew(row(p('details'), col(p('terminal'), p('browser'))), 'files', 700))
      .toEqual(row(p('details'), col(p('terminal'), p('browser'), p('files'))))
  })

  it('keeps details as the first column', () => {
    expect(placeNew(row(p('terminal'), p('browser')), 'details', 300)).toEqual(row(p('details'), p('terminal'), p('browser')))
  })
})

describe('fitToWidth', () => {
  const t = row(p('terminal'), p('browser'), p('details'))
  it('hides details when the panes do not fit, and shows it again when they do', () => {
    expect(minWidth(t)).toBe(880)
    expect(fitToWidth(t, 800)).toEqual(row(p('terminal'), p('browser')))
    expect(fitToWidth(t, 900)).toEqual(t)
    expect(fitToWidth(t, 0)).toEqual(t)
  })

  it('keeps details when it is the only pane', () => {
    expect(fitToWidth(p('details'), 100)).toEqual(p('details'))
  })

  it('counts stacked panes once', () => {
    expect(minWidth(row(col(p('terminal'), p('browser')), p('details')))).toBe(560)
  })
})
