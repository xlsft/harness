import { describe, expect, it } from 'vitest'
import type { ToolItem } from '~/lib/session/types'
import { netPatch } from '~/utils/tools'

const edit = (input: Record<string, unknown>, originalFile: string | null, name = 'Edit'): ToolItem => ({
  kind: 'tool', id: Math.random().toString(36), name, input, summary: '', status: 'ok', output: '',
  result: { originalFile, structuredPatch: [] }, resultSummary: '', durationMs: 0, startedAt: 0, messageId: 'm', grouped: false
})

const v1 = 'package main\n\nfunc add(a, b int) int { return a - b }\n'
const v2 = 'package main\n\nfunc add(a, b int) int { return a + b }\n'

describe('netPatch', () => {
  it('shows two edits of one file as one diff', () => {
    const hunks = netPatch([
      edit({ old_string: 'a - b', new_string: 'a + b' }, v1),
      edit({ old_string: 'func add', new_string: '// add returns the sum of a and b.\nfunc add' }, v2)
    ])!
    expect(hunks).toHaveLength(1)
    expect(hunks[0]!.lines).toEqual([
      ' package main',
      ' ',
      '-func add(a, b int) int { return a - b }',
      '+// add returns the sum of a and b.',
      '+func add(a, b int) int { return a + b }'
    ])
  })

  it('is empty when the edits cancel out, and null when they cannot be replayed', () => {
    expect(netPatch([edit({ old_string: 'a - b', new_string: 'a + b' }, v1), edit({ old_string: 'a + b', new_string: 'a - b' }, v2)])).toEqual([])
    expect(netPatch([edit({ old_string: 'missing', new_string: 'x' }, v1)])).toBeNull()
    expect(netPatch([{ ...edit({}, v1), result: null }])).toBeNull()
  })

  it('treats a write to a new file as all additions', () => {
    const hunks = netPatch([edit({ content: 'a\nb\n' }, null, 'Write')])!
    expect(hunks[0]!.lines).toEqual(['+a', '+b'])
  })
})
