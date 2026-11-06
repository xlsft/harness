import { create, type MessageInitShape } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { SessionEventSchema, type SessionEvent } from '~/gen/harness/agent/v1/agent_pb'
import { ScheduleSchema } from '~/gen/harness/agent/v1/schedules_pb'
import { GoalState, TaskNodeStatus } from '~/gen/harness/agent/v1/teamwork_pb'
import { MockSchedulesApi } from '~/lib/schedule/mock'
import {
  emptyScheduleDraft,
  machineText,
  scheduleDraftOf,
  scheduleOfDraft,
  ScheduleRunStatus,
  ScheduleTriggerKind,
  sortSchedules,
  stateText,
  triggerText
} from '~/lib/schedule/types'
import { createSessionState, reduce } from '~/lib/session/reducer'
import { goalStateText, taskGraphCounts, taskNodeLine } from '~/lib/session/teamwork'

const schedule = (init: MessageInitShape<typeof ScheduleSchema>) => create(ScheduleSchema, init)

describe('the schedule form', () => {
  it('turns its fields into a schedule and back', () => {
    const d = { ...emptyScheduleDraft('/repo'), name: ' Nightly ', prompt: 'test it', trigger: 'interval' as const, everyMinutes: '90' }
    const s = scheduleOfDraft(d)
    expect(s.name).toBe('Nightly')
    expect(s.trigger).toMatchObject({ kind: ScheduleTriggerKind.INTERVAL, everySeconds: 5400n, cron: '' })
    expect(s.cwd).toBe('/repo')
    const back = scheduleDraftOf(s)
    expect(back).toMatchObject({ trigger: 'interval', everyMinutes: '90', prompt: 'test it' })
  })

  it('sends only what the chosen trigger uses', () => {
    const d = { ...emptyScheduleDraft(), prompt: 'x', trigger: 'file' as const, paths: ' src/**/*.go \n\n*.md\n', cron: '* * * * *' }
    expect(scheduleOfDraft(d).trigger).toMatchObject({ kind: ScheduleTriggerKind.FILE_CHANGED, paths: ['src/**/*.go', '*.md'], cron: '', everySeconds: 0n })
  })

  it('keeps a local time as an instant and shows it back the same', () => {
    const d = { ...emptyScheduleDraft(), prompt: 'x', trigger: 'once' as const, at: '2030-05-06T07:08' }
    const s = scheduleOfDraft(d)
    expect(new Date(s.trigger!.at).getTime()).toBe(new Date('2030-05-06T07:08').getTime())
    expect(scheduleDraftOf(s).at).toBe('2030-05-06T07:08')
  })

  it('keeps what the form does not edit', () => {
    const base = schedule({ id: 'sch_1', prompt: 'x', paused: true, maxRuns: 3, expiresAt: '2031-01-01T00:00:00Z', deviceId: 'dev_b', effort: 'high', trigger: { kind: ScheduleTriggerKind.CRON, cron: '0 0 * * *', timezone: 'Asia/Tokyo' } })
    const out = scheduleOfDraft(scheduleDraftOf(base))
    expect(out).toMatchObject({ id: 'sch_1', paused: true, maxRuns: 3, expiresAt: '2031-01-01T00:00:00Z', deviceId: 'dev_b', effort: 'high' })
    expect(out.trigger?.timezone).toBe('Asia/Tokyo')
  })
})

describe('what the list says', () => {
  it('names the machine a schedule needs', () => {
    expect(machineText(schedule({ thisDevice: true }))).toBe('Runs on this machine')
    expect(machineText(schedule({ thisDevice: true, deviceLocal: true }))).toBe('This machine only')
    expect(machineText(schedule({ deviceName: 'studio' }))).toBe('Runs on studio')
  })

  it('says where it stands', () => {
    expect(stateText(schedule({ needsTrust: true, nextRunAt: '2030-01-01T00:00:00Z' }))).toMatch(/approval/)
    expect(stateText(schedule({ paused: true }))).toBe('Paused')
    expect(stateText(schedule({ done: true }))).toBe('Done')
    expect(stateText(schedule({}))).toMatch(/trigger/)
    expect(triggerText(schedule({ trigger: { kind: ScheduleTriggerKind.INTERVAL, everySeconds: 7200n } }))).toBe('Every 2 hours')
    expect(triggerText(schedule({ trigger: { kind: ScheduleTriggerKind.FILE_CHANGED, paths: ['a', 'b'] } }))).toBe('When a, b change')
  })

  it('puts what is done last', () => {
    const list = sortSchedules([schedule({ id: 'done', done: true }), schedule({ id: 'event' }), schedule({ id: 'later', nextRunAt: '2030-02-01T00:00:00Z' }), schedule({ id: 'soon', nextRunAt: '2030-01-01T00:00:00Z' })])
    expect(list.map(s => s.id)).toEqual(['soon', 'later', 'event', 'done'])
  })
})

describe('the mock schedules', () => {
  it('goes through create, pause, run now, history and delete', async () => {
    const api = new MockSchedulesApi()
    await expect(api.create(schedule({ prompt: ' ' }))).rejects.toThrow(/prompt/)
    const s = await api.create(scheduleOfDraft({ ...emptyScheduleDraft(), prompt: 'go', trigger: 'interval', everyMinutes: '5' }))
    expect((await api.setPaused(s.id, true)).paused).toBe(true)
    await api.runNow(s.id)
    const runs = await api.runs(s.id)
    expect(runs[0]?.status).toBe(ScheduleRunStatus.SUCCEEDED)
    await api.remove(s.id)
    expect((await api.list()).schedules.some(x => x.id === s.id)).toBe(false)
  })
})

describe('session events', () => {
  const ev = (event: SessionEvent['event']) => create(SessionEventSchema, { event })

  it('follows the goal', () => {
    let s = createSessionState('s1')
    s = reduce(s, ev({ case: 'goalUpdated', value: { goal: { condition: 'tests pass', state: GoalState.ACTIVE, turns: 2, maxTurns: 10 } as never } }))
    expect(s.goal).toMatchObject({ condition: 'tests pass', state: 'active', turns: 2, maxTurns: 10 })
    expect(goalStateText(s.goal!)).toMatch(/Working/)
    s = reduce(s, ev({ case: 'goalUpdated', value: { goal: { condition: 'tests pass', state: GoalState.CAP_REACHED, turns: 10, maxTurns: 10 } as never } }))
    expect(s.goal?.state).toBe('cap_reached')
  })

  it('replaces the task graph as a whole', () => {
    let s = createSessionState('s1')
    const node = (id: string, status: TaskNodeStatus, extra = {}) => ({ id, title: id, status, blockedBy: [], waitingFor: [], ...extra }) as never
    s = reduce(s, ev({ case: 'taskGraphUpdated', value: { tasks: [node('t1', TaskNodeStatus.IN_PROGRESS, { ownerName: 'alpha' }), node('t2', TaskNodeStatus.PENDING, { blockedBy: ['t1'], waitingFor: ['t1'] })] } as never }))
    expect(s.taskGraph).toHaveLength(2)
    expect(taskNodeLine(s.taskGraph[0]!)).toBe('alpha is on it')
    expect(taskNodeLine(s.taskGraph[1]!)).toBe('Waits for t1')
    s = reduce(s, ev({ case: 'taskGraphUpdated', value: { tasks: [node('t1', TaskNodeStatus.DONE)] } as never }))
    expect(taskGraphCounts(s.taskGraph)).toEqual({ done: 1, total: 1 })
  })
})
