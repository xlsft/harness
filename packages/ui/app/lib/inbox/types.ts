// The Inbox and Projects screens' view (docs/design/coordinator.md). The client and the server decide what is in which
// group, what is read and what a run may do; this keeps the pages the screen listed, applies the live changes the client
// streams, and words what they say.
import { tt } from '~/utils/i18n'
import {
  InboxGroup,
  InboxRequest_Kind,
  type InboxChange,
  type InboxCount,
  type InboxItem,
  type InboxRequest,
  type ListInboxResponse
} from '~/gen/harness/agent/v1/inbox_pb'
import type { CreateProjectRunRequestSchema,
  ControlProjectRunRequest_Action,
  ProjectRunState,
  ProjectWorkerState,
  type CreateProjectRunRequest,
  type ProjectRun,
  type ProjectRunLimits,
  type ProjectWorker

} from '~/gen/harness/agent/v1/project_runs_pb'
import type { MessageInitShape } from '@bufbuild/protobuf'
import { SubagentState } from '~/gen/harness/agent/v1/subagents_pb'

export { ControlProjectRunRequest_Action, InboxGroup, InboxRequest_Kind, ProjectRunState, ProjectWorkerState }
export type { CreateProjectRunRequest, InboxChange, InboxCount, InboxItem, InboxRequest, ProjectRun, ProjectRunLimits, ProjectWorker }

export interface InboxApi {
  list(group: InboxGroup, pageToken?: string, pageSize?: number): Promise<ListInboxResponse>
  // Calls onChange for every live change until signal aborts; onResync when the list should be read again.
  watch(handlers: { onChange: (c: InboxChange, counts: InboxCount[]) => void, onResync: () => void }, signal: AbortSignal): Promise<void>
  markRead(sessionIds: string[], read: boolean): Promise<void>
}

export interface ProjectRunsApi {
  list(includeRemote: boolean): Promise<ProjectRun[]>
  get(id: string): Promise<ProjectRun>
  create(req: MessageInitShape<typeof CreateProjectRunRequestSchema>): Promise<ProjectRun>
  control(id: string, action: ControlProjectRunRequest_Action, limits?: Partial<ProjectRunLimits>, sessionId?: string): Promise<ProjectRun>
  remove(id: string): Promise<void>
}

export const INBOX_GROUPS: readonly InboxGroup[] = [InboxGroup.NEEDS_INPUT, InboxGroup.FAILED, InboxGroup.FINISHED, InboxGroup.RUNNING]

export function groupTitle(g: InboxGroup): string {
  switch (g) {
    case InboxGroup.NEEDS_INPUT: return tt('Needs you')
    case InboxGroup.FAILED: return tt('Failed')
    case InboxGroup.FINISHED: return tt('Finished')
    case InboxGroup.RUNNING: return tt('Running')
  }
  return ''
}

// One group as listed so far.
export interface GroupPage {
  items: InboxItem[]
  next: string
  loaded: boolean
}

export interface InboxView {
  groups: Map<InboxGroup, GroupPage>
  counts: Map<InboxGroup, InboxCount>
}

export function emptyInbox(): InboxView {
  return { groups: new Map(INBOX_GROUPS.map(g => [g, { items: [], next: '', loaded: false }])), counts: new Map() }
}

function setCounts(v: InboxView, counts: InboxCount[]) {
  if (!counts.length) return
  v.counts = new Map(counts.map(c => [c.group, c]))
}

// The order the client lists in: unread first, then the latest attention.
function before(a: InboxItem, b: InboxItem): number {
  if (a.unread !== b.unread) return a.unread ? -1 : 1
  if (a.attentionAt !== b.attentionAt) return a.attentionAt > b.attentionAt ? -1 : 1
  return a.sessionId < b.sessionId ? -1 : 1
}

// Adds a page the client returned (the first one replaces what the group had).
export function addPage(v: InboxView, group: InboxGroup, res: ListInboxResponse, first: boolean): InboxView {
  const g = v.groups.get(group) ?? { items: [], next: '', loaded: false }
  const seen = new Set(first ? [] : g.items.map(i => i.sessionId))
  const items = first ? [] : [...g.items]
  for (const it of res.items) {
    if (!seen.has(it.sessionId)) items.push(it)
  }
  v.groups.set(group, { items, next: res.nextPageToken, loaded: true })
  setCounts(v, res.counts)
  return v
}

// Applies a live change: the item leaves its old group and joins its new one where the order puts it.
export function applyChange(v: InboxView, c: InboxChange, counts: InboxCount[]): InboxView {
  const id = c.item?.sessionId || c.removedSessionId
  for (const [g, page] of v.groups) {
    const i = page.items.findIndex(x => x.sessionId === id)
    if (i >= 0) v.groups.set(g, { ...page, items: page.items.filter((_, j) => j !== i) })
  }
  const item = c.item
  if (item && INBOX_GROUPS.includes(item.group)) {
    const page = v.groups.get(item.group)!
    const items = [...page.items]
    const at = items.findIndex(x => before(item, x) < 0)
    // An item that sorts past what is loaded waits for its page.
    if (at >= 0) items.splice(at, 0, item)
    else if (!page.next) items.push(item)
    v.groups.set(item.group, { ...page, items })
  }
  setCounts(v, counts)
  return v
}

export function markLocal(v: InboxView, ids: string[], read: boolean): InboxView {
  for (const [g, page] of v.groups) {
    v.groups.set(g, { ...page, items: page.items.map(i => (ids.includes(i.sessionId) ? { ...i, unread: !read } : i)) })
  }
  return v
}

export type InboxRow
  = | { kind: 'group', key: string, group: InboxGroup, total: number, unread: number }
    | { kind: 'item', key: string, item: InboxItem }
    | { kind: 'empty', key: string, group: InboxGroup }

// The flat list the screen virtualises: a header per group, then its items.
export function inboxRows(v: InboxView, only?: InboxGroup): InboxRow[] {
  const rows: InboxRow[] = []
  for (const g of INBOX_GROUPS) {
    if (only && g !== only) continue
    const page = v.groups.get(g)!
    const c = v.counts.get(g)
    rows.push({ kind: 'group', key: `g${g}`, group: g, total: c?.total ?? page.items.length, unread: c?.unread ?? 0 })
    if (page.loaded && !page.items.length) rows.push({ kind: 'empty', key: `e${g}`, group: g })
    for (const item of page.items) rows.push({ kind: 'item', key: item.sessionId, item })
  }
  return rows
}

// The first group, in screen order, with another page to read.
export function nextToLoad(v: InboxView, only?: InboxGroup): InboxGroup | undefined {
  return INBOX_GROUPS.find(g => (!only || g === only) && !!v.groups.get(g)?.next)
}

export function requestText(r: InboxRequest): string {
  switch (r.kind) {
    case InboxRequest_Kind.PERMISSION: return tt('Allow {0}: {1}', [r.toolName, r.summary])
    case InboxRequest_Kind.QUESTION: return r.questions[0]?.question ?? tt('A question')
    case InboxRequest_Kind.PLAN: return tt('A plan to approve')
    case InboxRequest_Kind.PROJECT_RUN: return tt('Project run paused: {0}', [r.summary])
  }
  return ''
}

export function itemLine(it: InboxItem): string {
  if (it.requests.length) return requestText(it.requests[0]!)
  if (it.error) return it.error
  if (it.report) return it.report
  if (it.working) return tt('Working')
  const agent = it.agents.find(a => a.state === SubagentState.RUNNING)
  return agent ? tt('Agent {0} running', [agent.name]) : ''
}

export function deviceText(it: InboxItem): string {
  const name = it.remote ? it.deviceName || it.deviceId : tt('This computer')
  if (it.deviceOnline === false) return tt('{0}, offline', [name])
  if (!it.open) return tt('{0}, closed', [name])
  return name
}

export function runStateText(s: ProjectRunState): string {
  switch (s) {
    case ProjectRunState.PLANNING: return tt('Planning')
    case ProjectRunState.RUNNING: return tt('Running')
    case ProjectRunState.WAITING_FOR_USER: return tt('Waiting for you')
    case ProjectRunState.MERGING: return tt('Merging')
    case ProjectRunState.PAUSED: return tt('Paused')
    case ProjectRunState.DONE: return tt('Done')
    case ProjectRunState.FAILED: return tt('Failed')
    case ProjectRunState.STOPPED: return tt('Stopped')
  }
  return ''
}

export function workerStateText(s: ProjectWorkerState): string {
  switch (s) {
    case ProjectWorkerState.STARTING: return tt('Starting')
    case ProjectWorkerState.RUNNING: return tt('Running')
    case ProjectWorkerState.NEEDS_INPUT: return tt('Needs you')
    case ProjectWorkerState.FINISHED: return tt('Finished')
    case ProjectWorkerState.FAILED: return tt('Failed')
    case ProjectWorkerState.STOPPED: return tt('Stopped')
    case ProjectWorkerState.INTERRUPTED: return tt('Interrupted')
    case ProjectWorkerState.MERGED: return tt('Merged')
  }
  return ''
}

export function runEnded(s: ProjectRunState): boolean {
  return s === ProjectRunState.DONE || s === ProjectRunState.FAILED || s === ProjectRunState.STOPPED
}

// How much of the budget is spent, 0 to 1; undefined without a budget.
export function budgetShare(run: ProjectRun): number | undefined {
  const l = run.limits
  const u = run.usage
  if (!l || !u) return undefined
  if (l.budgetUsd > 0) return Math.min(1, u.costUsd / l.budgetUsd)
  if (l.budgetTokens > 0n) return Math.min(1, Number(u.inputTokens + u.outputTokens) / Number(l.budgetTokens))
  return undefined
}

export function budgetText(run: ProjectRun): string {
  const l = run.limits
  const u = run.usage
  const spent = `$${(u?.costUsd ?? 0).toFixed(2)}`
  if (l && l.budgetUsd > 0) return tt('{0} of {1}', [spent, `$${l.budgetUsd.toFixed(2)}`])
  const tokens = Number((u?.inputTokens ?? 0n) + (u?.outputTokens ?? 0n))
  if (l && l.budgetTokens > 0n) return tt('{0} of {1} tokens', [tokens.toLocaleString(), Number(l.budgetTokens).toLocaleString()])
  return tt('{0} spent, no budget', [spent])
}

export function activeWorkers(run: ProjectRun): number {
  return run.workers.filter(w => w.state === ProjectWorkerState.STARTING || w.state === ProjectWorkerState.RUNNING || w.state === ProjectWorkerState.NEEDS_INPUT).length
}
