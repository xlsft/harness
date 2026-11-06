// The mock client's schedules: what the screen needs to be used without a client behind it.
import { clone, create } from '@bufbuild/protobuf'
import { ScheduleRunSchema, ScheduleSchema } from '~/gen/harness/agent/v1/schedules_pb'
import { ScheduleOrigin, ScheduleRunSource, ScheduleRunStatus, ScheduleTriggerKind, type Schedule, type ScheduleList, type ScheduleRun, type SchedulesApi } from './types'

export class MockSchedulesApi implements SchedulesApi {
  private items = new Map<string, Schedule>()
  private history = new Map<string, ScheduleRun[]>()
  private n = 0
  // UserPreferences.schedules_enabled, as the mock client's preferences last set it.
  enabled = true

  constructor() {
    const s = create(ScheduleSchema, {
      id: 'sch_demo', name: 'Morning triage', prompt: 'Go through the open issues and sort them by urgency.', cwd: '~/Projects/app',
      trigger: { kind: ScheduleTriggerKind.CRON, cron: '0 9 * * 1-5', timezone: 'UTC' }, catchUp: true, deviceId: 'dev_mock', deviceName: 'this machine', thisDevice: true,
      origin: ScheduleOrigin.USER, createdAt: '2026-01-01T09:00:00Z', nextRunAt: new Date(Date.now() + 3600_000).toISOString()
    })
    this.items.set(s.id, s)
    this.history.set(s.id, [])
  }

  async list(): Promise<ScheduleList> {
    return { schedules: [...this.items.values()].map(s => clone(ScheduleSchema, s)), deviceId: 'dev_mock', deviceName: 'this machine', synced: true, enabled: this.enabled }
  }

  private checked(s: Schedule) {
    if (!s.prompt.trim()) throw new Error('[invalid_argument] a schedule needs a prompt')
    const t = s.trigger
    if (t?.kind === ScheduleTriggerKind.INTERVAL && t.everySeconds < 60n) throw new Error('[invalid_argument] the shortest repeat is 1m0s')
    if (t?.kind === ScheduleTriggerKind.ONCE && !t.at) throw new Error('[invalid_argument] a one-off needs a time (RFC 3339)')
    if (t?.kind === ScheduleTriggerKind.CRON && t.cron.trim().split(/\s+/).length !== 5 && !t.cron.startsWith('@')) throw new Error('[invalid_argument] a cron expression has five fields')
    if (t?.kind === ScheduleTriggerKind.FILE_CHANGED && !t.paths.length) throw new Error('[invalid_argument] a file trigger needs between 1 and 32 patterns')
  }

  async create(s: Schedule) {
    this.checked(s)
    const out = clone(ScheduleSchema, s)
    out.id = `sch_${++this.n}`
    out.thisDevice = true
    out.deviceId = 'dev_mock'
    out.deviceName = 'this machine'
    out.origin = ScheduleOrigin.USER
    out.createdAt = new Date().toISOString()
    if (out.trigger?.kind === ScheduleTriggerKind.WEBHOOK) out.trigger.webhookUrl = `https://harness.example/api/schedules/webhook/doc_${this.n}.secret`
    else if (out.trigger?.kind !== ScheduleTriggerKind.FILE_CHANGED) out.nextRunAt = new Date(Date.now() + 3600_000).toISOString()
    this.items.set(out.id, out)
    this.history.set(out.id, [])
    return clone(ScheduleSchema, out)
  }

  private get(id: string): Schedule {
    const s = this.items.get(id)
    if (!s) throw new Error(`[not_found] no schedule ${id}`)
    return s
  }

  async update(s: Schedule) {
    this.checked(s)
    const old = this.get(s.id)
    const out = clone(ScheduleSchema, s)
    out.thisDevice = old.thisDevice
    out.deviceName = old.deviceName
    out.origin = old.origin
    out.createdAt = old.createdAt
    out.nextRunAt = old.nextRunAt
    out.lastRun = old.lastRun
    out.runs = old.runs
    this.items.set(s.id, out)
    return clone(ScheduleSchema, out)
  }

  async remove(id: string) {
    this.get(id)
    this.items.delete(id)
    this.history.delete(id)
  }

  async setPaused(id: string, paused: boolean) {
    const s = this.get(id)
    s.paused = paused
    return clone(ScheduleSchema, s)
  }

  async runNow(id: string) {
    const s = this.get(id)
    const now = new Date().toISOString()
    const run = create(ScheduleRunSchema, { id: `run_${++this.n}`, scheduleId: id, status: ScheduleRunStatus.SUCCEEDED, source: ScheduleRunSource.MANUAL, startedAt: now, finishedAt: now, sessionId: 'main', summary: 'Done.', deviceName: 'this machine' })
    this.history.get(id)?.unshift(run)
    s.lastRun = run
    s.runs++
    return { schedule: clone(ScheduleSchema, s), queued: false }
  }

  async runs(id: string) {
    this.get(id)
    return this.history.get(id) ?? []
  }

  async trust(id: string) {
    const s = this.get(id)
    s.needsTrust = false
    s.problem = ''
    return clone(ScheduleSchema, s)
  }

  async rotateWebhook(id: string) {
    const s = this.get(id)
    if (s.trigger) s.trigger.webhookUrl = `https://harness.example/api/schedules/webhook/doc_${id}.secret${++this.n}`
    return clone(ScheduleSchema, s)
  }
}
