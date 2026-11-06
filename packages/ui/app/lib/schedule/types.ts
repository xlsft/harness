// The Scheduled screen's view of schedules. The client decides what runs and when; this only words what it reports and
// turns the form's fields into a message.
import { tt } from '~/utils/i18n'
import { create } from '@bufbuild/protobuf'
import {
  ScheduleOrigin,
  ScheduleRunSource,
  ScheduleRunStatus,
  ScheduleSchema,
  ScheduleTriggerKind,
  type ListSchedulesResponse,
  type Schedule,
  type ScheduleRun
} from '~/gen/harness/agent/v1/schedules_pb'

export { ScheduleOrigin, ScheduleRunSource, ScheduleRunStatus, ScheduleTriggerKind }
export type { Schedule, ScheduleRun }

export type ScheduleList = Pick<ListSchedulesResponse, 'schedules' | 'deviceId' | 'deviceName' | 'synced' | 'enabled'>

export interface SchedulesApi {
  list(): Promise<ScheduleList>
  create(s: Schedule): Promise<Schedule>
  update(s: Schedule): Promise<Schedule>
  remove(id: string): Promise<void>
  setPaused(id: string, paused: boolean): Promise<Schedule>
  // queued: asked of the schedule's own machine, which runs it when it has seen the request.
  runNow(id: string): Promise<{ schedule?: Schedule, queued: boolean }>
  runs(id: string): Promise<ScheduleRun[]>
  trust(id: string): Promise<Schedule>
  rotateWebhook(id: string): Promise<Schedule>
}

export type TriggerChoice = 'once' | 'interval' | 'cron' | 'file' | 'webhook'

const CHOICE_KIND: Record<TriggerChoice, ScheduleTriggerKind> = {
  once: ScheduleTriggerKind.ONCE,
  interval: ScheduleTriggerKind.INTERVAL,
  cron: ScheduleTriggerKind.CRON,
  file: ScheduleTriggerKind.FILE_CHANGED,
  webhook: ScheduleTriggerKind.WEBHOOK
}

export const TRIGGER_OPTIONS: { value: TriggerChoice, label: string }[] = [
  { value: 'once', label: tt('Once') },
  { value: 'interval', label: tt('Every…') },
  { value: 'cron', label: tt('Cron') },
  { value: 'file', label: tt('When files change') },
  { value: 'webhook', label: tt('When a webhook arrives') }
]

// What the form edits: text as typed, so the client's own checks answer for what is wrong.
export interface ScheduleDraft {
  id: string
  name: string
  prompt: string
  cwd: string
  model: string
  permissionMode: string
  worktree: boolean
  catchUp: boolean
  trigger: TriggerChoice
  // A local date and time as a datetime-local input gives it.
  at: string
  everyMinutes: string
  cron: string
  paths: string
  // The rest of the schedule, kept as it was.
  base?: Schedule
}

export function emptyScheduleDraft(cwd = ''): ScheduleDraft {
  return { id: '', name: '', prompt: '', cwd, model: '', permissionMode: '', worktree: false, catchUp: true, trigger: 'cron', at: '', everyMinutes: '60', cron: '0 9 * * 1-5', paths: '' }
}

function choiceOf(kind: ScheduleTriggerKind | undefined): TriggerChoice {
  for (const [choice, k] of Object.entries(CHOICE_KIND)) {
    if (k === kind) return choice as TriggerChoice
  }
  return 'cron'
}

const pad = (n: number) => String(n).padStart(2, '0')

// RFC 3339 to what a datetime-local input shows, in this machine's zone.
export function toLocalInput(iso: string): string {
  const d = new Date(iso)
  if (!iso || Number.isNaN(d.getTime())) return ''
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`
}

export function scheduleDraftOf(s: Schedule): ScheduleDraft {
  const t = s.trigger
  return {
    ...emptyScheduleDraft(s.cwd),
    id: s.id,
    name: s.name,
    prompt: s.prompt,
    model: s.model,
    permissionMode: s.permissionMode,
    worktree: s.worktree,
    catchUp: s.catchUp,
    trigger: choiceOf(t?.kind),
    at: toLocalInput(t?.at ?? ''),
    everyMinutes: t?.everySeconds ? String(Number(t.everySeconds) / 60) : '60',
    cron: t?.cron || '0 9 * * 1-5',
    paths: (t?.paths ?? []).join('\n'),
    base: s
  }
}

export function scheduleOfDraft(d: ScheduleDraft): Schedule {
  const paths = d.paths.split('\n').map(p => p.trim()).filter(Boolean)
  const base = d.base
  const at = d.at ? new Date(d.at) : undefined
  return create(ScheduleSchema, {
    id: d.id,
    name: d.name.trim(),
    prompt: d.prompt,
    cwd: d.cwd,
    model: d.model,
    effort: base?.effort ?? '',
    permissionMode: d.permissionMode,
    engine: base?.engine ?? '',
    worktree: d.worktree,
    catchUp: d.catchUp,
    paused: base?.paused ?? false,
    maxRuns: base?.maxRuns ?? 0,
    expiresAt: base?.expiresAt ?? '',
    deviceId: base?.deviceId ?? '',
    trigger: {
      kind: CHOICE_KIND[d.trigger],
      at: d.trigger === 'once' && at && !Number.isNaN(at.getTime()) ? at.toISOString() : '',
      everySeconds: d.trigger === 'interval' ? BigInt(Math.round(Number(d.everyMinutes || 0) * 60) || 0) : 0n,
      cron: d.trigger === 'cron' ? d.cron.trim() : '',
      timezone: d.trigger === 'cron' ? base?.trigger?.timezone ?? '' : '',
      paths: d.trigger === 'file' ? paths : [],
      cooldownSeconds: base?.trigger?.cooldownSeconds ?? 0n
    }
  })
}

function everyText(seconds: number): string {
  if (seconds % 86400 === 0) return seconds === 86400 ? 'Every day' : `Every ${seconds / 86400} days`
  if (seconds % 3600 === 0) return seconds === 3600 ? 'Every hour' : `Every ${seconds / 3600} hours`
  if (seconds % 60 === 0) return seconds === 60 ? 'Every minute' : `Every ${seconds / 60} minutes`
  return `Every ${seconds} seconds`
}

export function formatWhen(iso: string, now = new Date()): string {
  const d = new Date(iso)
  if (!iso || Number.isNaN(d.getTime())) return ''
  const time = d.toLocaleTimeString(undefined, { hour: 'numeric', minute: '2-digit' })
  return d.toDateString() === now.toDateString() ? time : `${d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' })}, ${time}`
}

export function triggerText(s: Schedule): string {
  const t = s.trigger
  switch (t?.kind) {
    case ScheduleTriggerKind.ONCE: return `Once, ${formatWhen(t.at)}`
    case ScheduleTriggerKind.INTERVAL: return everyText(Number(t.everySeconds))
    case ScheduleTriggerKind.CRON: return `Cron ${t.cron}${t.timezone ? ` (${t.timezone})` : ''}`
    case ScheduleTriggerKind.FILE_CHANGED: return `When ${t.paths.join(', ')} change`
    case ScheduleTriggerKind.WEBHOOK: return 'When a webhook arrives'
  }
  return 'No trigger'
}

const STATUS_TEXT: Partial<Record<ScheduleRunStatus, string>> = {
  [ScheduleRunStatus.RUNNING]: 'Running',
  [ScheduleRunStatus.SUCCEEDED]: 'Finished',
  [ScheduleRunStatus.FAILED]: 'Failed',
  [ScheduleRunStatus.INTERRUPTED]: 'Interrupted',
  [ScheduleRunStatus.SKIPPED]: 'Skipped',
  [ScheduleRunStatus.MISSED]: 'Missed',
  [ScheduleRunStatus.NEEDS_INPUT]: 'Needs your input',
  [ScheduleRunStatus.STARTED]: 'Handed to the agent'
}

export const runStatusText = (r: ScheduleRun) => STATUS_TEXT[r.status] ?? ''

export function runStatusTone(r: ScheduleRun): string {
  switch (r.status) {
    case ScheduleRunStatus.SUCCEEDED: return 'text-ok'
    case ScheduleRunStatus.FAILED: return 'text-bad'
    case ScheduleRunStatus.NEEDS_INPUT:
    case ScheduleRunStatus.MISSED: return 'text-warn'
    case ScheduleRunStatus.RUNNING: return 'text-accent'
  }
  return 'text-muted'
}

const SOURCE_TEXT: Partial<Record<ScheduleRunSource, string>> = {
  [ScheduleRunSource.TIME]: 'on time',
  [ScheduleRunSource.CATCH_UP]: 'caught up after downtime',
  [ScheduleRunSource.MANUAL]: 'run by hand',
  [ScheduleRunSource.FILE]: 'files changed',
  [ScheduleRunSource.WEBHOOK]: 'webhook'
}

export const runSourceText = (r: ScheduleRun) => SOURCE_TEXT[r.source] ?? ''

// Where it runs, in words: a schedule tied to a machine says which.
export function machineText(s: Schedule): string {
  if (s.thisDevice) return s.deviceLocal ? 'This machine only' : 'Runs on this machine'
  return `Runs on ${s.deviceName || 'another machine'}`
}

// One line on where it stands.
export function stateText(s: Schedule): string {
  if (s.needsTrust) return 'Waiting for your approval'
  if (s.done) return 'Done'
  if (s.paused) return 'Paused'
  if (s.nextRunAt) return `Next ${formatWhen(s.nextRunAt)}`
  return 'Waiting for its trigger'
}

const ORIGIN_TEXT: Partial<Record<ScheduleOrigin, string>> = {
  [ScheduleOrigin.LOOP]: '/loop',
  [ScheduleOrigin.WAKEUP]: 'wake-up'
}

export const originText = (s: Schedule) => ORIGIN_TEXT[s.origin] ?? ''

// What the screen shows first: what still has something to do, then what is done.
export function sortSchedules(list: readonly Schedule[]): Schedule[] {
  const at = (s: Schedule) => (s.nextRunAt ? new Date(s.nextRunAt).getTime() : Number.POSITIVE_INFINITY)
  return [...list].sort((a, b) => {
    if (a.done !== b.done) return Number(a.done) - Number(b.done)
    if (at(a) !== at(b)) return at(a) < at(b) ? -1 : 1
    return a.createdAt.localeCompare(b.createdAt)
  })
}
