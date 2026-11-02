// Review comments from the diff pane travel as a plain user message the agent can read, and the
// transcript recognizes the same text to draw it as a review card. formatReview and parseReview are the
// two halves of that format:
//
//   Review comments on your changes:
//
//   src/main.go:3
//   ```diff
//   +// add returns the sum of a and b.
//   ```
//   Explain the overflow behaviour too.

export interface ReviewItem {
  path: string
  line: number
  // '+' added, '-' removed, ' ' unchanged context.
  sign: '+' | '-' | ' '
  code: string
  text: string
}

const HEADER = /^Review comments? on your changes:\n\n/
const ITEM = /^(\S[^\n]*):(\d+)\n```diff\n([+\- ])([^\n]*)\n```\n([\s\S]*?)(?=\n\n\S[^\n]*:\d+\n```diff\n|\s*(?![\s\S]))/gm

export function formatReview(items: ReviewItem[]): string {
  const head = items.length === 1 ? 'Review comment on your changes:' : 'Review comments on your changes:'
  const blocks = [...items]
    .sort((a, b) => a.path.localeCompare(b.path) || a.line - b.line)
    .map(c => `${c.path}:${c.line}\n\`\`\`diff\n${c.sign}${c.code}\n\`\`\`\n${c.text.trim()}`)
  return `${head}\n\n${blocks.join('\n\n')}`
}

/** The review items of a message written by formatReview, or null for any other text. */
export function parseReview(text: string): ReviewItem[] | null {
  if (!HEADER.test(text)) return null
  const body = text.replace(HEADER, '')
  const items: ReviewItem[] = []
  for (const m of body.matchAll(ITEM)) {
    items.push({ path: m[1]!, line: Number(m[2]), sign: m[3] as ReviewItem['sign'], code: m[4]!, text: m[5]!.trim() })
  }
  return items.length ? items : null
}
