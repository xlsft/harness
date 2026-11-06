// What a screen reader hears about a session. Text streaming into a live region is unusable: every token interrupts. So a session
// is read as moments: the agent started, it needs an answer, it finished (with what it said), something failed. Pure, so it is
// tested without a browser; composables/useScreenReader.ts feeds it the active session and puts the result in live regions.
import type { AssistantBlock, SessionState } from '~/lib/session/types'
import { tt } from '~/utils/i18n'

export interface SessionMoment {
  id: string
  // A turn runs.
  busy: boolean
  // Ids of what waits for the user, in the order they came.
  waiting: string[]
  // The last message of the agent that finished streaming.
  lastAnswerId: string
  lastAnswer: string
  lastError: string
}

export interface Announcement {
  text: string
  // 'assertive' speaks over whatever is being read: only for what stops the work until the user acts.
  priority: 'polite' | 'assertive'
}

// A finished answer read aloud whole can take minutes, and the transcript has it all: this is a start, not the text.
const MAX_READ = 600

function blockText(blocks: readonly AssistantBlock[]): string {
  return blocks.filter(b => b.type === 'text').map(b => b.text).join('\n').trim()
}

function plain(markdown: string): string {
  return markdown
    .replace(/```[\s\S]*?```/g, ` ${tt('code block')} `)
    .replace(/`([^`]+)`/g, '$1')
    .replace(/[*_#>]+/g, '')
    .replace(/\s+/g, ' ')
    .trim()
}

export function momentOf(s: SessionState): SessionMoment {
  const waiting: string[] = []
  for (const it of s.items) {
    if ((it.kind === 'permission' || it.kind === 'question' || it.kind === 'plan' || it.kind === 'computerAccess' || it.kind === 'browserSite') && it.state === 'pending') waiting.push(`${it.kind}:${it.id}`)
  }
  let lastAnswerId = ''
  let lastAnswer = ''
  let lastError = ''
  for (let i = s.items.length - 1; i >= 0; i--) {
    const it = s.items[i]!
    if (!lastAnswerId && it.kind === 'assistant' && !it.streaming) {
      const text = blockText(it.blocks)
      if (text) {
        lastAnswerId = it.id
        lastAnswer = text
      }
    }
    if (!lastError && it.kind === 'notice' && it.level === 'error') lastError = it.text
    if (lastAnswerId && lastError) break
  }
  return { id: s.meta.id, busy: s.meta.status === 'working' || s.meta.status === 'needs_input', waiting, lastAnswerId, lastAnswer, lastError }
}

const WAITING_LABELS: Record<string, () => string> = {
  permission: () => tt('The agent needs permission to go on.'),
  question: () => tt('The agent asked you a question.'),
  plan: () => tt('The agent has a plan for you to review.'),
  computerAccess: () => tt('The agent asks to use apps on this computer.'),
  browserSite: () => tt('The agent asks to use a site in your Chrome.')
}

// What changed between two moments of the session on screen. Nothing is said about a session just opened: its past is in the
// transcript, and reading it out is what the user can ask their reader to do.
export function announcements(prev: SessionMoment | null, next: SessionMoment): Announcement[] {
  if (!prev || prev.id !== next.id) return []
  const out: Announcement[] = []
  if (!prev.busy && next.busy) out.push({ text: tt('The agent is working.'), priority: 'polite' })
  for (const w of next.waiting) {
    if (prev.waiting.includes(w)) continue
    const label = WAITING_LABELS[w.slice(0, w.indexOf(':'))]
    if (label) out.push({ text: label(), priority: 'assertive' })
  }
  if (next.lastAnswerId && next.lastAnswerId !== prev.lastAnswerId) {
    const text = plain(next.lastAnswer)
    out.push({ text: text.length > MAX_READ ? `${text.slice(0, MAX_READ)}… ${tt('The rest is in the conversation.')}` : text, priority: 'polite' })
  }
  if (next.lastError && next.lastError !== prev.lastError) out.push({ text: tt('Error: {0}', [next.lastError]), priority: 'assertive' })
  if (prev.busy && !next.busy && next.waiting.length === 0) out.push({ text: tt('The agent is done.'), priority: 'polite' })
  return out
}
