// Where a diff comment (or a draft) sits after the diff changed under it: comments are anchored to their
// line's side, number and text, and follow the text to the nearest line that still has it.

export interface LineAnchor {
  side: 'new' | 'old'
  line: number
  code: string
}

/** Index in `lines` of the line `a` sits on now, or -1 when no line on its side has its text any more. */
export function locateAnchor(lines: LineAnchor[], a: LineAnchor): number {
  let best = -1
  let dist = Infinity
  for (const [i, x] of lines.entries()) {
    if (x.side !== a.side || x.code !== a.code) continue
    const d = Math.abs(x.line - a.line)
    if (d < dist) {
      best = i
      dist = d
    }
  }
  return best
}
