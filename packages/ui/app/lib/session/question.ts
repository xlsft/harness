// AskUserQuestion: wire mapping (Question / QuestionResolved → view model) and the card's answer rules.

import { QuestionItem_Type, QuestionOutcome } from '~/gen/harness/agent/v1/agent_pb'
import type { QuestionAnswer, QuestionItem as PbQuestionItem, QuestionResolved } from '~/gen/harness/agent/v1/agent_pb'
import type { QuestionAnswerVM, QuestionItemVM, QuestionOptionVM, QuestionState, QuestionType } from './types'

const TYPES: Partial<Record<QuestionItem_Type, QuestionType>> = {
  [QuestionItem_Type.SINGLE]: 'single',
  [QuestionItem_Type.MULTI]: 'multi',
  [QuestionItem_Type.TEXT]: 'text',
  [QuestionItem_Type.YES_NO]: 'yes_no'
}

// An older client sends no type, only multi_select, and accepts Other on every question.
export function questionItemVM(i: PbQuestionItem): QuestionItemVM {
  const type = TYPES[i.type] ?? (i.multiSelect ? 'multi' : 'single')
  const typed = i.type !== QuestionItem_Type.UNSPECIFIED
  const allowOther = type !== 'text' && (typed ? i.allowOther : true)
  const options = i.options.map(o => ({ label: o.label, description: o.description, recommended: o.recommended }))
  const limit = options.length + (allowOther ? 1 : 0)
  return {
    question: i.question,
    header: i.header,
    type,
    options,
    min: type === 'multi' ? i.min : 0,
    max: type === 'multi' && i.max > 0 ? i.max : type === 'multi' ? limit : 1,
    allowOther
  }
}

export function answerVM(a: QuestionAnswer): QuestionAnswerVM {
  return { selected: [...a.selected], text: a.text, skipped: a.skipped }
}

export function questionState(r: QuestionResolved): QuestionState {
  switch (r.outcome) {
    case QuestionOutcome.ANSWERED: return 'answered'
    case QuestionOutcome.SKIPPED: return 'skipped'
    case QuestionOutcome.MESSAGE: return 'message'
    case QuestionOutcome.CANCELLED: return 'cancelled'
    // An older client reports only who resolved it.
    default: return r.resolvedBy === 'interrupt' ? 'cancelled' : 'answered'
  }
}

// Recommended options first, otherwise in the model's order.
export function orderedOptions(q: QuestionItemVM): QuestionOptionVM[] {
  return [...q.options.filter(o => o.recommended), ...q.options.filter(o => !o.recommended)]
}

// The card's draft for one question: chosen labels and the Other / text field.
export interface QuestionDraft {
  selected: string[]
  other: string
  otherOpen: boolean
}

export function emptyDraft(): QuestionDraft {
  return { selected: [], other: '', otherOpen: false }
}

// Picks an option: single and yes_no replace the choice (and close Other), multi toggles it within max.
export function toggleOption(q: QuestionItemVM, d: QuestionDraft, label: string): QuestionDraft {
  if (q.type !== 'multi') return { ...d, selected: [label], otherOpen: false, other: '' }
  if (d.selected.includes(label)) return { ...d, selected: d.selected.filter(l => l !== label) }
  if (choiceCount(q, d) >= q.max) return d
  return { ...d, selected: [...d.selected, label] }
}

// Opens or closes Other: for single and yes_no it replaces the chosen option.
export function toggleOther(q: QuestionItemVM, d: QuestionDraft): QuestionDraft {
  if (d.otherOpen) return { ...d, otherOpen: false, other: '' }
  if (q.type === 'multi' && choiceCount(q, d) >= q.max) return d
  return { ...d, otherOpen: true, selected: q.type === 'multi' ? d.selected : [] }
}

function otherText(q: QuestionItemVM, d: QuestionDraft) {
  return q.type === 'text' || (q.allowOther && d.otherOpen) ? d.other.trim() : ''
}

function choiceCount(q: QuestionItemVM, d: QuestionDraft) {
  return d.selected.length + (otherText(q, d) ? 1 : 0)
}

// The answer a draft gives. Nothing chosen means skipped, except for a multi question that accepts no
// choice (min 0): that is an answer with nothing selected.
export function draftAnswer(q: QuestionItemVM, d: QuestionDraft): QuestionAnswerVM {
  const text = otherText(q, d)
  const n = choiceCount(q, d)
  if (n === 0 && !(q.type === 'multi' && q.min === 0)) return { selected: [], text: '', skipped: true }
  return { selected: q.type === 'text' ? [] : [...d.selected], text, skipped: false }
}

// Whether the draft can be submitted: skipped or within the bounds.
export function draftValid(q: QuestionItemVM, d: QuestionDraft): boolean {
  const a = draftAnswer(q, d)
  if (a.skipped) return true
  const n = a.selected.length + (a.text ? 1 : 0)
  return q.type === 'multi' ? n >= q.min && n <= q.max : n === 1
}

// A one-line summary of an answer for the resolved card.
export function answerSummary(q: QuestionItemVM, a: QuestionAnswerVM | undefined): string {
  if (!a || a.skipped) return 'skipped'
  const parts = [...a.selected, ...(a.text ? [a.text] : [])]
  if (!parts.length) return q.type === 'multi' ? 'none' : 'skipped'
  return parts.join(', ')
}
