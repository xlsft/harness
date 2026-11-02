import { describe, expect, it } from 'vitest'
import { formatReview, parseReview, type ReviewItem } from '~/utils/review'

describe('review messages', () => {
  const items: ReviewItem[] = [
    { path: 'main.go', line: 3, sign: '+', code: '// add returns the sum of a and b.', text: 'More detail,\nplease.' },
    { path: 'main.go', line: 3, sign: '-', code: 'func add(a, b int) int { return a - b }', text: 'Why was this wrong?' }
  ]

  it('round-trips through the text the agent reads', () => {
    const text = formatReview(items)
    expect(text.startsWith('Review comments on your changes:\n\nmain.go:3\n```diff\n+// add returns')).toBe(true)
    expect(parseReview(text)).toEqual(items)
  })

  it('ignores ordinary messages', () => {
    expect(parseReview('Review the code please')).toBeNull()
    expect(parseReview('Review comment on your changes:\n\nnothing structured')).toBeNull()
  })
})
