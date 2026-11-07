import { describe, expect, it } from 'vitest'
import { highlightOutput, stripTerminalCodes } from '../app/utils/highlight'

describe('terminal codes in command output', () => {
  it('are removed, and the output keeps its own highlighting', () => {
    const raw = '\x1B[41m\x1B[1m FAIL \x1B[22m\x1B[49m test/a.test.ts\n\x1B[2m Test Files \x1B[22m \x1B[1m\x1B[31m1 failed\x1B[39m\x1B[22m'
    expect(stripTerminalCodes(raw)).toBe(' FAIL  test/a.test.ts\n Test Files  1 failed')
    const lines = highlightOutput(raw)
    expect(lines.flat().map(t => t.t).join('')).not.toContain('\x1B')
    expect(lines[0]!.some(t => t.t === 'FAIL')).toBe(true)
  })

  it('include titles and links, and leave plain text alone', () => {
    expect(stripTerminalCodes('\x1B]0;title\x07a \x1B]8;;https://x.dev\x1B\\link\x1B]8;;\x1B\\')).toBe('a link')
    expect(stripTerminalCodes('plain 🙂')).toBe('plain 🙂')
  })
})
