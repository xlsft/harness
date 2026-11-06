// Settings > Import: where a user can bring their work from, and what any import reports. Each source has its own piece
// for what it finds and what can be chosen (./claudeCode.ts); following a run is the same for all of them. The client
// decides what exists and what happened: this only renders it.

import type { IconName } from '~/utils/icons'

export interface ImportSource {
  id: 'claude-code' | 'claude-project'
  name: string
  description: string
  icon: IconName
}

export const IMPORT_SOURCES: ImportSource[] = [
  {
    id: 'claude-code',
    name: 'Claude Code',
    description: 'Memory, rules, settings, skills, agents, commands, output styles, workflows and sessions on this computer.',
    icon: 'terminal'
  },
  {
    id: 'claude-project',
    name: 'A folder\'s Claude Code files',
    description: 'CLAUDE.md and what is under .claude, into the folder\'s AGENTS.md and .harness.',
    icon: 'folder'
  }
]

export type ImportStatus = 'new' | 'exists' | 'skipped' | 'manual' | 'imported' | 'queued' | 'error'

export interface ImportItem {
  // The choice it belongs to (a source's own word).
  section: string
  // "skill", "agent", "session", ...
  kind: string
  name: string
  status: ImportStatus
  note: string
}

export interface ImportProgress {
  current: string
  done: number
  total: number
  // The item that just finished.
  item?: ImportItem
}

export interface ImportCounts {
  imported: number
  exists: number
  skipped: number
  queued: number
  errors: number
  manual: number
}

export interface ImportReport extends ImportCounts {
  items: ImportItem[]
  // Why the run as a whole stopped.
  error: string
}

export interface ImportHandlers {
  onProgress?(p: ImportProgress): void
}

// A run under way. done gives the report, or null when it was cancelled.
export interface ImportHandle {
  done: Promise<ImportReport | null>
  cancel(): void
}

export interface ImportRun {
  phase: 'idle' | 'running' | 'done' | 'cancelled' | 'failed'
  current: string
  done: number
  total: number
  // What finished so far: a cancelled run has no report, and still has these.
  items: ImportItem[]
  report: ImportReport | null
  error: string
}

export type ImportRunEvent
  = | { type: 'start' }
    | { type: 'progress', progress: ImportProgress }
    | { type: 'report', report: ImportReport }
    | { type: 'cancelled' }
    | { type: 'failed', error: string }

export const IDLE_RUN: ImportRun = { phase: 'idle', current: '', done: 0, total: 0, items: [], report: null, error: '' }

export function reduceImportRun(run: ImportRun, e: ImportRunEvent): ImportRun {
  switch (e.type) {
    case 'start':
      return { ...IDLE_RUN, phase: 'running' }
    case 'progress': {
      if (run.phase !== 'running') return run
      const { current, done, total, item } = e.progress
      return { ...run, current, done, total, items: item ? [...run.items, item] : run.items }
    }
    case 'report':
      // A failed run reports too: what got done, and why it stopped.
      return { ...run, phase: e.report.error ? 'failed' : 'done', report: e.report, items: e.report.items, error: e.report.error, current: '' }
    case 'cancelled':
      return run.phase === 'running' ? { ...run, phase: 'cancelled', current: '' } : run
    case 'failed':
      return { ...run, phase: 'failed', error: e.error, current: '' }
  }
}

/** What a run came to: the client's report, or what finished before it was cancelled. */
export function importCounts(run: ImportRun): ImportCounts {
  if (run.report) return run.report
  const n = (s: ImportStatus) => run.items.filter(i => i.status === s).length
  return { imported: n('imported'), exists: n('exists'), skipped: n('skipped'), queued: n('queued'), errors: n('error'), manual: n('manual') }
}

export function importErrors(run: ImportRun): ImportItem[] {
  return run.items.filter(i => i.status === 'error')
}
