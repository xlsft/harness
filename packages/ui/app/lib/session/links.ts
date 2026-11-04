// What the session tools show in a transcript, read from their calls and results. Everything here is decided on the
// client; this only says which card a call draws. Chapters come from the MarkChapter calls, so they survive a replay.
import type { SessionProposalVM, ToolItem, TranscriptItem } from './types'

// Tool calls drawn by SessionCard or as a chapter divider instead of a tool row.
export const SESSION_CARD_TOOLS = new Set(['SessionStart', 'SessionHandoff', 'SessionList', 'MarkChapter'])

export interface Chapter {
  // The tool call's id: the divider's anchor.
  id: string
  title: string
  summary: string
}

export function chaptersOf(items: TranscriptItem[]): Chapter[] {
  return items.flatMap((i): Chapter[] => i.kind === 'tool' && i.name === 'MarkChapter' && i.status !== 'error' && i.status !== 'interrupted'
    ? [{ id: i.id, title: String(i.input.title ?? ''), summary: String(i.input.summary ?? '') }]
    : [])
}

export interface SessionListRow {
  id: string
  name: string
  title: string
  state: string
}

export type SessionCardVM
  = | { kind: 'none' }
    | { kind: 'session', id: string, name: string, title: string }
    | { kind: 'list', rows: SessionListRow[] }
    // proposal is undefined when the client knows nothing of it (a fork's copy of the card, another machine's session).
    | { kind: 'proposal', title: string, prompt: string, proposal?: SessionProposalVM }
    | { kind: 'status', text: string, error: boolean }

const str = (v: unknown) => (typeof v === 'string' ? v : '')

export function sessionCardOf(t: ToolItem, proposals: Record<string, SessionProposalVM>): SessionCardVM {
  const r = t.result ?? {}
  if (t.name === 'SessionList') {
    if (t.status !== 'ok') return { kind: 'none' }
    const rows = Array.isArray(r.sessions) ? r.sessions as Record<string, unknown>[] : []
    return { kind: 'list', rows: rows.map(x => ({ id: str(x.id), name: str(x.name), title: str(x.title), state: str(x.state) })) }
  }
  const title = str(t.input.title)
  if (t.input.initiation === 'own_initiative') {
    if (t.status === 'error') return { kind: 'status', text: 'Could not propose a session', error: true }
    return { kind: 'proposal', title, prompt: str(t.input.prompt), proposal: proposals[t.id] }
  }
  if (str(r.session_id)) return { kind: 'session', id: str(r.session_id), name: str(r.name), title: str(r.title) || title }
  if (t.status === 'running') return t.input.initiation === 'offer' ? { kind: 'none' } : { kind: 'status', text: `Starting a session: ${title}`, error: false }
  if (t.status === 'error') return { kind: 'status', text: t.resultSummary || 'Could not start the session', error: true }
  return { kind: 'none' }
}

// The sessions a transcript links to, so the session list can catch up when a new one appears.
export function linkedSessionIds(items: TranscriptItem[]): string[] {
  return items.flatMap(i => i.kind === 'tool' && SESSION_CARD_TOOLS.has(i.name) && str(i.result?.session_id) ? [str(i.result?.session_id)] : [])
}

// Notice kinds of notes from other sessions; their text is the client's JSON (SessionNoteNotice).
export function sessionNoteOf(kind: string, text: string): { text: string, session: { id: string, name: string, title: string } } | null {
  if (kind !== 'session_message' && kind !== 'session_report') return null
  try {
    const j = JSON.parse(text) as Record<string, unknown>
    return { text: str(j.line) || str(j.text), session: { id: str(j.fromId), name: str(j.fromName), title: str(j.fromTitle) } }
  } catch {
    return null
  }
}
