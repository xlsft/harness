import { describe, expect, it } from 'vitest'
import { locateAnchor, type LineAnchor } from '../app/utils/diffAnchor'

const lines = (...rows: [LineAnchor['side'], number, string][]) => rows.map(([side, line, code]) => ({ side, line, code }))

describe('locateAnchor', () => {
  const anchor: LineAnchor = { side: 'new', line: 3, code: 'return a + b' }

  it('stays on its line while the text is unchanged', () => {
    expect(locateAnchor(lines(['new', 2, 'x'], ['new', 3, 'return a + b']), anchor)).toBe(1)
  })

  it('follows its text when lines were added above it', () => {
    expect(locateAnchor(lines(['new', 3, 'added'], ['new', 4, 'added'], ['new', 5, 'return a + b']), anchor)).toBe(2)
  })

  it('picks the nearest of several lines with the same text', () => {
    expect(locateAnchor(lines(['new', 1, 'return a + b'], ['new', 9, 'x'], ['new', 4, 'return a + b']), anchor)).toBe(2)
  })

  it('ignores the other side and reports a line that is gone', () => {
    expect(locateAnchor(lines(['old', 3, 'return a + b'], ['new', 3, 'return a - b']), anchor)).toBe(-1)
  })
})
