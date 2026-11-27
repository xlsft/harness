import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { InboxChangeSchema, InboxItemSchema, ListInboxResponseSchema } from '~/gen/harness/agent/v1/inbox_pb'
import { ProjectRunSchema } from '~/gen/harness/agent/v1/project_runs_pb'
import { MockInboxApi, MockProjectRunsApi } from '~/lib/inbox/mock'
import {
  addPage,
  applyChange,
  budgetShare,
  budgetText,
  ControlProjectRunRequest_Action,
  emptyInbox,
  inboxRows,
  InboxGroup,
  InboxRequest_Kind,
  itemLine,
  markLocal,
  nextToLoad,
  ProjectRunState
} from '~/lib/inbox/types'

const item = (id: string, group: InboxGroup, extra: Partial<Parameters<typeof create<typeof InboxItemSchema>>[1]> = {}) =>
  create(InboxItemSchema, { sessionId: id, title: id, group, open: true, ...extra })

describe('inbox view', () => {
  it('lists pages per group and asks for the next page in screen order', () => {
    let v = emptyInbox()
    v = addPage(v, InboxGroup.NEEDS_INPUT, create(ListInboxResponseSchema, { items: [item('a', InboxGroup.NEEDS_INPUT)], counts: [{ group: InboxGroup.NEEDS_INPUT, total: 1, unread: 1 }] }), true)
    v = addPage(v, InboxGroup.FINISHED, create(ListInboxResponseSchema, { items: [item('b', InboxGroup.FINISHED)], nextPageToken: 'p2' }), true)
    const rows = inboxRows(v)
    expect(rows.filter(r => r.kind === 'group')).toHaveLength(4)
    expect(rows.filter(r => r.kind === 'item').map(r => r.kind === 'item' && r.item.sessionId)).toEqual(['a', 'b'])
    expect(nextToLoad(v)).toBe(InboxGroup.FINISHED)
    v = addPage(v, InboxGroup.FINISHED, create(ListInboxResponseSchema, { items: [item('b', InboxGroup.FINISHED), item('c', InboxGroup.FINISHED)] }), false)
    expect(v.groups.get(InboxGroup.FINISHED)!.items.map(i => i.sessionId)).toEqual(['b', 'c'])
    expect(nextToLoad(v)).toBeUndefined()
    expect(inboxRows(v, InboxGroup.NEEDS_INPUT).filter(r => r.kind === 'item')).toHaveLength(1)
  })

  it('moves an item between groups on a live change and drops removed ones', () => {
    let v = emptyInbox()
    v = addPage(v, InboxGroup.NEEDS_INPUT, create(ListInboxResponseSchema, { items: [item('a', InboxGroup.NEEDS_INPUT)] }), true)
    v = addPage(v, InboxGroup.RUNNING, create(ListInboxResponseSchema, { items: [] }), true)
    v = applyChange(v, create(InboxChangeSchema, { item: item('a', InboxGroup.RUNNING, { working: true }) }), [])
    expect(v.groups.get(InboxGroup.NEEDS_INPUT)!.items).toHaveLength(0)
    expect(v.groups.get(InboxGroup.RUNNING)!.items[0]!.sessionId).toBe('a')
    v = applyChange(v, create(InboxChangeSchema, { item: item('z', InboxGroup.RUNNING, { unread: true, attentionAt: '2026-11-07T10:00:00Z' }) }), [{ $typeName: 'harness.agent.v1.InboxCount', group: InboxGroup.RUNNING, total: 2, unread: 1 }])
    expect(v.groups.get(InboxGroup.RUNNING)!.items.map(i => i.sessionId)).toEqual(['z', 'a'])
    expect(v.counts.get(InboxGroup.RUNNING)!.total).toBe(2)
    v = applyChange(v, create(InboxChangeSchema, { removedSessionId: 'z' }), [])
    expect(v.groups.get(InboxGroup.RUNNING)!.items.map(i => i.sessionId)).toEqual(['a'])
    v = markLocal(v, ['a'], false)
    expect(v.groups.get(InboxGroup.RUNNING)!.items[0]!.unread).toBe(true)
  })

  it('words what an item needs', () => {
    expect(itemLine(item('p', InboxGroup.NEEDS_INPUT, { requests: [{ kind: InboxRequest_Kind.PERMISSION, toolName: 'Bash', summary: 'make' }] }))).toBe('Allow Bash: make')
    expect(itemLine(item('f', InboxGroup.FAILED, { error: 'boom', report: 'old' }))).toBe('boom')
    expect(itemLine(item('d', InboxGroup.FINISHED, { report: 'All done.' }))).toBe('All done.')
  })

  it('shows a budget only when there is one', () => {
    expect(budgetShare(create(ProjectRunSchema, { limits: { maxWorkers: 2 }, usage: { costUsd: 1 } }))).toBeUndefined()
    const run = create(ProjectRunSchema, { limits: { maxWorkers: 2, budgetUsd: 4 }, usage: { costUsd: 1 } })
    expect(budgetShare(run)).toBe(0.25)
    expect(budgetText(run)).toBe('$1.00 of $4.00')
  })
})

describe('mock inbox and runs', () => {
  it('answers, pages and streams', async () => {
    const api = new MockInboxApi()
    const first = await api.list(InboxGroup.FINISHED, '', 50)
    expect(first.items).toHaveLength(50)
    expect(first.nextPageToken).toBe('50')
    const ctl = new AbortController()
    const seen: string[] = []
    const watching = api.watch({ onChange: c => seen.push(c.item?.sessionId ?? c.removedSessionId), onResync: () => {} }, ctl.signal)
    api.resolve('mock-needs', 'perm-1')
    await api.markRead(['mock-done'], true)
    ctl.abort()
    await watching
    expect(seen).toEqual(['mock-needs', 'mock-done'])
    expect((await api.list(InboxGroup.RUNNING)).items.map(i => i.sessionId)).toContain('mock-needs')
  })

  it('creates and controls a run', async () => {
    const runs = new MockProjectRunsApi()
    await expect(runs.create({ goal: '', cwd: '/x' })).rejects.toThrow(/goal/)
    const run = await runs.create({ goal: 'Ship it', cwd: '/repo', limits: { maxWorkers: 99 } })
    expect(run.limits!.maxWorkers).toBe(32)
    expect((await runs.control(run.id, ControlProjectRunRequest_Action.PAUSE)).state).toBe(ProjectRunState.PAUSED)
    expect((await runs.control(run.id, ControlProjectRunRequest_Action.STOP)).state).toBe(ProjectRunState.STOPPED)
    await runs.remove(run.id)
    expect((await runs.list()).map(r => r.id)).not.toContain(run.id)
  })
})
