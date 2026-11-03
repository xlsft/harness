import { createPinia, setActivePinia } from 'pinia'
import { describe, expect, it, vi } from 'vitest'
import { QuestionOutcome, SendUserMessageRequest_Delivery } from '~/gen/harness/agent/v1/agent_pb'
import {
  answerSummary,
  draftAnswer,
  draftValid,
  emptyDraft,
  orderedOptions,
  toggleOption,
  toggleOther
} from '~/lib/session/question'
import { createSessionState, reduce, reduceAll } from '~/lib/session/reducer'
import type { QuestionItem, QuestionItemVM, QuestionReply } from '~/lib/session/types'
import { EventBuilder } from '~/lib/transport/events'
import type { SessionTransport } from '~/lib/transport/types'
import { useSessionStore } from '~/stores/sessions'

vi.mock('vue', () => ({ markRaw: <T>(v: T) => v, toRaw: <T>(v: T) => v }))

const T0 = Date.parse('2026-11-02T12:00:00Z')

function setup() {
  const b = new EventBuilder('s1', T0)
  const s = createSessionState('s1')
  return { b, s }
}

const card = (s: ReturnType<typeof createSessionState>) => s.items.find(i => i.kind === 'question') as QuestionItem

function ask(b: EventBuilder) {
  b.user('u1', 'set it up')
  b.turnStarted('u1', 'glm-4.6')
  b.question('q1', 't1', [
    { question: 'Which DB?', header: 'DB', type: 'single', options: [{ label: 'SQLite' }, { label: 'Postgres', description: 'server', recommended: true }] },
    { question: 'Which features?', header: 'Features', type: 'multi', min: 1, max: 2, options: [{ label: 'Auth' }, { label: 'Billing' }, { label: 'Search' }] },
    { question: 'Name?', header: 'Name', type: 'text' },
    { question: 'Ship?', header: 'Ship', type: 'yes_no', allowOther: false }
  ])
}

describe('question reducer', () => {
  it('maps typed questions', () => {
    const { b, s } = setup()
    ask(b)
    reduceAll(s, b.events)
    const [single, multi, text, yesNo] = card(s).items
    expect(single).toMatchObject({ type: 'single', allowOther: true, max: 1 })
    expect(single!.options[1]).toEqual({ label: 'Postgres', description: 'server', recommended: true })
    expect(multi).toMatchObject({ type: 'multi', min: 1, max: 2, allowOther: true })
    expect(text).toMatchObject({ type: 'text', allowOther: false, options: [] })
    expect(yesNo).toMatchObject({ type: 'yes_no', allowOther: false })
    expect(yesNo!.options.map(o => o.label)).toEqual(['Yes', 'No'])
  })

  it('reads questions of an older client (multi_select only, Other always offered)', () => {
    const { b, s } = setup()
    b.question('q1', 't1', [{ question: 'Pick?', header: 'P', multiSelect: true, options: [{ label: 'A' }, { label: 'B' }] }])
    reduceAll(s, b.events)
    expect(card(s).items[0]).toMatchObject({ type: 'multi', allowOther: true, min: 0, max: 3 })
    reduce(s, b.questionResolved('q1', 'interrupt', QuestionOutcome.UNSPECIFIED))
    expect(card(s).state).toBe('cancelled')
  })

  it('a replayed log rebuilds a question that still waits', () => {
    const { b } = setup()
    ask(b)
    // Messages written meanwhile are queued on the client and do not end the wait.
    b.queue([{ uuid: 'm1', text: 'use sqlite' }])
    const fresh = createSessionState('s1')
    reduceAll(fresh, b.events)
    expect(card(fresh).state).toBe('pending')
    expect(fresh.pendingQuestion).toBe('q1')
    expect(fresh.meta.status).toBe('needs_input')
    expect(fresh.queue.map(q => q.text)).toEqual(['use sqlite'])
  })

  it('records each outcome', () => {
    const outcomes: [QuestionOutcome, string][] = [
      [QuestionOutcome.ANSWERED, 'answered'],
      [QuestionOutcome.SKIPPED, 'skipped'],
      [QuestionOutcome.MESSAGE, 'message'],
      [QuestionOutcome.CANCELLED, 'cancelled']
    ]
    for (const [outcome, state] of outcomes) {
      const { b, s } = setup()
      ask(b)
      reduceAll(s, b.events)
      reduce(s, b.questionResolved('q1', 'local', outcome,
        outcome === QuestionOutcome.ANSWERED ? [{ selected: ['Postgres'] }, { selected: ['Auth'], text: 'Audit' }, { skipped: true }, { selected: ['No'] }] : [],
        outcome === QuestionOutcome.MESSAGE ? 'sqlite please' : ''))
      const c = card(s)
      expect(c.state).toBe(state)
      expect(s.pendingQuestion).toBeNull()
      expect(s.meta.status).toBe('working')
      if (state === 'answered') {
        expect(c.answers[1]).toEqual({ selected: ['Auth'], text: 'Audit', skipped: false })
        expect(c.items.map((it, i) => answerSummary(it, c.answers[i]))).toEqual(['Postgres', 'Auth, Audit', 'skipped', 'No'])
      }
      if (state === 'message') expect(c.message).toBe('sqlite please')
    }
  })

  it('a question still open when the turn ends is cancelled', () => {
    const { b, s } = setup()
    ask(b)
    b.turnFinished('interrupted')
    reduceAll(s, b.events)
    expect(card(s).state).toBe('cancelled')
    expect(s.pendingQuestion).toBeNull()
    expect(s.meta.status).toBe('idle')
  })
})

const q = (o: Partial<QuestionItemVM>): QuestionItemVM => ({
  question: 'Q?', header: 'H', type: 'single', min: 0, max: 1, allowOther: true,
  options: [{ label: 'A', description: '', recommended: false }, { label: 'B', description: '', recommended: true }, { label: 'C', description: '', recommended: false }],
  ...o
})

describe('question card rules', () => {
  it('puts recommended options first', () => {
    expect(orderedOptions(q({})).map(o => o.label)).toEqual(['B', 'A', 'C'])
  })

  it('single: one choice, Other replaces it; nothing chosen is skipped', () => {
    const single = q({})
    let d = toggleOption(single, emptyDraft(), 'A')
    d = toggleOption(single, d, 'B')
    expect(draftAnswer(single, d)).toEqual({ selected: ['B'], text: '', skipped: false })
    d = toggleOther(single, d)
    expect(d.selected).toEqual([])
    expect(draftAnswer(single, d).skipped).toBe(true)
    d = { ...d, other: ' MySQL ' }
    expect(draftAnswer(single, d)).toEqual({ selected: [], text: 'MySQL', skipped: false })
    expect(draftValid(single, d)).toBe(true)
    expect(draftAnswer(single, emptyDraft()).skipped).toBe(true)
    // Without Other the text is ignored.
    expect(draftAnswer(q({ allowOther: false }), { selected: [], other: 'x', otherOpen: true }).skipped).toBe(true)
  })

  it('multi: within min..max, Other counts as one; empty is an answer only when min is 0', () => {
    const multi = q({ type: 'multi', min: 1, max: 2 })
    let d = toggleOption(multi, emptyDraft(), 'A')
    d = toggleOption(multi, d, 'B')
    d = toggleOption(multi, d, 'C')
    expect(d.selected).toEqual(['A', 'B'])
    expect(toggleOther(multi, d).otherOpen).toBe(false)
    d = toggleOption(multi, d, 'A')
    d = { ...toggleOther(multi, d), other: 'D' }
    expect(draftAnswer(multi, d)).toEqual({ selected: ['B'], text: 'D', skipped: false })
    expect(draftValid(multi, d)).toBe(true)
    expect(draftAnswer(multi, emptyDraft()).skipped).toBe(true)
    const optional = q({ type: 'multi', min: 0, max: 4 })
    expect(draftAnswer(optional, emptyDraft())).toEqual({ selected: [], text: '', skipped: false })
    expect(answerSummary(optional, draftAnswer(optional, emptyDraft()))).toBe('none')
    const two = q({ type: 'multi', min: 2, max: 3 })
    expect(draftValid(two, toggleOption(two, emptyDraft(), 'A'))).toBe(false)
  })

  it('text: the field is the answer', () => {
    const text = q({ type: 'text', options: [], allowOther: false })
    expect(draftAnswer(text, { selected: [], other: 'demo', otherOpen: false })).toEqual({ selected: [], text: 'demo', skipped: false })
    expect(draftAnswer(text, { selected: [], other: '  ', otherOpen: false }).skipped).toBe(true)
  })
})

describe('question store actions', () => {
  function store() {
    setActivePinia(createPinia())
    const st = useSessionStore()
    const calls: unknown[][] = []
    st.transport = {
      sendUserMessage: async (...a: unknown[]) => void calls.push(['send', ...a]),
      answerQuestion: async (_id: string, requestId: string, reply: QuestionReply) => void calls.push(['answer', requestId, reply]),
      subscribe: () => () => {}
    } as unknown as SessionTransport
    const s = createSessionState('s1')
    s.meta.status = 'needs_input'
    s.pendingQuestion = 'q1'
    st.sessions.s1 = s
    st.order = ['s1']
    return { st, calls, s }
  }

  it('always hands chat messages to the client', async () => {
    const { st, calls, s } = store()
    await st.send('s1', 'use sqlite')
    expect(calls).toEqual([['send', 's1', expect.any(String), 'use sqlite', SendUserMessageRequest_Delivery.QUEUE]])
    s.pendingQuestion = null
    s.meta.status = 'working'
    await st.send('s1', 'later')
    expect(calls).toHaveLength(2)
    expect(calls[1]![3]).toBe('later')
  })

  it('answers, skips and answers with the queued messages', async () => {
    const { st, calls, s } = store()
    const answers = [{ selected: ['A'], text: '', skipped: false }]
    await st.answerQuestion('s1', 'q1', answers)
    await st.skipQuestion('s1', 'q1')
    expect(calls).toEqual([['answer', 'q1', { outcome: 'answered', answers }], ['answer', 'q1', { outcome: 'skipped' }]])
    calls.length = 0

    expect(st.hasChatAnswer('s1')).toBe(false)
    s.queue = [{ id: 'm1', text: 'typed meanwhile' }]
    expect(st.hasChatAnswer('s1')).toBe(true)
    await st.answerWithMessage('s1', 'q1')
    expect(calls).toEqual([['answer', 'q1', { outcome: 'message' }]])
  })
})
