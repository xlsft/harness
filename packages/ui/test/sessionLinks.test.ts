import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { Notice_Level, NoticeSchema, SessionEventSchema } from '~/gen/harness/agent/v1/agent_pb'
import { SessionProposal_State, SessionProposalSchema } from '~/gen/harness/agent/v1/session_links_pb'
import { chaptersOf, linkedSessionIds, sessionCardOf, sessionNoteOf } from '~/lib/session/links'
import { createSessionState, reduce } from '~/lib/session/reducer'
import type { NoticeItem, ToolItem } from '~/lib/session/types'

function tool(name: string, input: Record<string, unknown>, over: Partial<ToolItem> = {}): ToolItem {
  return { kind: 'tool', id: 't1', name, input, summary: '', status: 'ok', output: '', result: null, resultSummary: '', durationMs: 0, startedAt: 0, messageId: 'm', grouped: false, ...over }
}

let seq = 0n
const event = (value: Parameters<typeof create<typeof SessionEventSchema>>[1]) => create(SessionEventSchema, { seq: ++seq, sessionId: 's1', ...value })

describe('session cards', () => {
  it('a started session is a row, with the id from the result', () => {
    const t = tool('SessionStart', { title: 'Docs', initiation: 'user_asked' }, { result: { session_id: 'c1', name: 'brisk-otter-ab12', title: 'Docs' } })
    expect(sessionCardOf(t, {})).toEqual({ kind: 'session', id: 'c1', name: 'brisk-otter-ab12', title: 'Docs' })
    expect(linkedSessionIds([t])).toEqual(['c1'])
  })

  it('an offer shows nothing of its own until it is answered, nor when the user stays here', () => {
    expect(sessionCardOf(tool('SessionStart', { title: 'Docs', initiation: 'offer' }, { status: 'running' }), {})).toEqual({ kind: 'none' })
    expect(sessionCardOf(tool('SessionStart', { title: 'Docs', initiation: 'offer' }, { result: { outcome: 'answered' } }), {})).toEqual({ kind: 'none' })
  })

  it('a failed start says so', () => {
    expect(sessionCardOf(tool('SessionStart', { title: 'Docs', initiation: 'user_asked' }, { status: 'error', resultSummary: 'Could not start' }), {})).toEqual({ kind: 'status', text: 'Could not start', error: true })
  })

  it('a proposal takes its state from the client, by the tool call', () => {
    const s = createSessionState('s1')
    const t = tool('SessionStart', { title: 'Docs', prompt: 'write them', initiation: 'own_initiative' }, { result: { proposal_id: 'prop_1' } })
    // A copy of the card the client knows nothing about offers no buttons.
    expect(sessionCardOf(t, s.proposals)).toEqual({ kind: 'proposal', title: 'Docs', prompt: 'write them', proposal: undefined })
    reduce(s, event({ event: { case: 'sessionProposal', value: create(SessionProposalSchema, { id: 'prop_1', toolUseId: 't1', state: SessionProposal_State.PENDING }) } }))
    expect(sessionCardOf(t, s.proposals)).toMatchObject({ proposal: { id: 'prop_1', state: 'pending' } })
    reduce(s, event({ event: { case: 'sessionProposal', value: create(SessionProposalSchema, { id: 'prop_1', toolUseId: 't1', state: SessionProposal_State.STARTED, sessionId: 'c1', sessionName: 'n' }) } }))
    expect(sessionCardOf(t, s.proposals)).toMatchObject({ proposal: { state: 'started', sessionId: 'c1', sessionName: 'n' } })
    reduce(s, event({ event: { case: 'sessionProposal', value: create(SessionProposalSchema, { id: 'prop_1', toolUseId: 't1', state: SessionProposal_State.EXPIRED }) } }))
    expect(sessionCardOf(t, s.proposals)).toMatchObject({ proposal: { state: 'expired' } })
  })

  it('lists the rows of SessionList', () => {
    const t = tool('SessionList', {}, { result: { sessions: [{ id: 'a', title: 'A', state: 'idle', name: 'n' }, { id: 'b', title: 'B', state: 'remote' }] } })
    expect(sessionCardOf(t, {})).toEqual({ kind: 'list', rows: [{ id: 'a', name: 'n', title: 'A', state: 'idle' }, { id: 'b', name: '', title: 'B', state: 'remote' }] })
  })
})

describe('chapters', () => {
  it('come from the MarkChapter calls, without the failed ones', () => {
    expect(chaptersOf([
      tool('MarkChapter', { title: 'One', summary: 'first' }, { id: 'a' }),
      tool('Bash', {}, { id: 'b' }),
      tool('MarkChapter', { title: '' }, { id: 'c', status: 'error' }),
      tool('MarkChapter', { title: 'Two' }, { id: 'd', status: 'running' })
    ])).toEqual([{ id: 'a', title: 'One', summary: 'first' }, { id: 'd', title: 'Two', summary: '' }])
  })
})

describe('notes from other sessions', () => {
  const json = JSON.stringify({ kind: 'message', fromId: 'p1', fromName: 'calm-heron', fromTitle: 'Parent', text: 'hi', line: 'Message from session calm-heron: hi' })

  it('become a notice with the session, never a user item', () => {
    const s = createSessionState('s1')
    reduce(s, event({ event: { case: 'notice', value: create(NoticeSchema, { kind: 'session_message', level: Notice_Level.INFO, text: json }) } }))
    expect(s.items.some(i => i.kind === 'user')).toBe(false)
    const n = s.items[0] as NoticeItem
    expect(n).toMatchObject({ kind: 'notice', text: 'Message from session calm-heron: hi', session: { id: 'p1', name: 'calm-heron' } })
  })

  it('only those kinds, and plain text stays a plain notice', () => {
    expect(sessionNoteOf('info', json)).toBeNull()
    expect(sessionNoteOf('session_report', 'not json')).toBeNull()
  })
})
