// Importing from Claude Code (AgentService ScanClaudeImport and RunClaudeImport, proto/harness/agent/v1/import.proto):
// what a scan found on this computer, and what the user picked from it. Scanning writes nothing; importing uploads to
// the user's own server.

import { tt } from '~/utils/i18n'
import type { ImportHandle, ImportHandlers, ImportItem, ImportStatus } from './types'

export type ClaudeSection = 'memory' | 'settings' | 'skills' | 'agents' | 'commands' | 'output-styles' | 'mcp' | 'sessions'

// The kinds with a checkbox, in order. MCP servers are only listed: they are set up by hand.
export const CLAUDE_SECTIONS: { key: ClaudeSection, label: string }[] = [
  { key: 'memory', label: tt('Memory') },
  { key: 'settings', label: tt('Settings') },
  { key: 'skills', label: tt('Skills') },
  { key: 'agents', label: tt('Agents') },
  { key: 'commands', label: tt('Commands') },
  { key: 'output-styles', label: tt('Output styles') }
]

export interface ClaudeImportSession {
  id: string
  title: string
  sizeBytes: number
  updatedAt: string
  status: ImportStatus
  note: string
}

export interface ClaudeImportProject {
  folder: string
  path: string
  sessionCount: number
  importedCount: number
  totalBytes: number
  newestAt: string
  sessions: ClaudeImportSession[]
}

export interface ClaudeImportScan {
  found: boolean
  claudeDir: string
  items: ImportItem[]
  projects: ClaudeImportProject[]
  // There is a server to import to.
  connected: boolean
  // The server said which documents exist; otherwise some "new" ones may exist.
  serverChecked: boolean
}

export interface ClaudeImportRequest {
  sections: ClaudeSection[]
  projectPaths: string[]
  sessionIds?: string[]
  includeSecrets: boolean
  force: boolean
  claudeDir?: string
}

export interface ClaudeImportApi {
  scan(claudeDir?: string): Promise<ClaudeImportScan>
  run(req: ClaudeImportRequest, handlers?: ImportHandlers): ImportHandle
}

// What the user picked. Replaced whole on every change, so a view recomputes from it.
export interface ClaudeSelection {
  sections: ReadonlySet<ClaudeSection>
  projects: ReadonlySet<string>
  force: boolean
  includeSecrets: boolean
}

// Nothing is picked until the user picks it; force and secrets stay off.
export const EMPTY_SELECTION: ClaudeSelection = { sections: new Set(), projects: new Set(), force: false, includeSecrets: false }

export interface ClaudeSectionRow {
  key: ClaudeSection
  label: string
  items: ImportItem[]
  // Items that would come as things stand.
  fresh: number
  exists: number
  // There is something to bring, at least with force.
  selectable: boolean
}

export function sectionRows(scan: ClaudeImportScan): ClaudeSectionRow[] {
  return CLAUDE_SECTIONS.map(({ key, label }) => {
    const items = scan.items.filter(i => i.section === key)
    const fresh = items.filter(i => i.status === 'new').length
    const exists = items.filter(i => i.status === 'exists').length
    return { key, label, items, fresh, exists, selectable: fresh + exists > 0 }
  })
}

export function manualItems(scan: ClaudeImportScan): ImportItem[] {
  return scan.items.filter(i => i.section === 'mcp')
}

/** The sessions an import of this project would bring. */
export function newSessions(p: ClaudeImportProject): ClaudeImportSession[] {
  return p.sessions.filter(s => s.status === 'new')
}

export function filterProjects(projects: ClaudeImportProject[], query: string): ClaudeImportProject[] {
  const q = query.trim().toLowerCase()
  if (!q) return projects
  return projects.filter(p => p.path.toLowerCase().includes(q) || p.sessions.some(s => s.title.toLowerCase().includes(q)))
}

const selectable = (projects: ClaudeImportProject[]) => projects.filter(p => newSessions(p).length > 0)

/** Whether the visible projects with something to bring are picked. */
export function selectAllState(visible: ClaudeImportProject[], picked: ReadonlySet<string>): 'all' | 'some' | 'none' {
  const list = selectable(visible)
  const n = list.filter(p => picked.has(p.path)).length
  if (n === 0) return 'none'
  return n === list.length ? 'all' : 'some'
}

/** Picks the visible projects, or takes them back when all are picked. Projects out of view stay as they are. */
export function toggleAll(visible: ClaudeImportProject[], picked: ReadonlySet<string>): Set<string> {
  const next = new Set(picked)
  const list = selectable(visible)
  const all = selectAllState(visible, picked) === 'all'
  for (const p of list) {
    if (all) next.delete(p.path)
    else next.add(p.path)
  }
  return next
}

export function toggled<T>(set: ReadonlySet<T>, value: T): Set<T> {
  const next = new Set(set)
  if (!next.delete(value)) next.add(value)
  return next
}

export interface ClaudeImportTotal {
  items: number
  sessions: number
  bytes: number
}

/** The running total: what an import of this selection would bring. */
export function selectionTotal(scan: ClaudeImportScan, sel: ClaudeSelection): ClaudeImportTotal {
  const items = scan.items.filter(i => sel.sections.has(i.section as ClaudeSection) && (i.status === 'new' || (sel.force && i.status === 'exists'))).length
  let sessions = 0
  let bytes = 0
  for (const p of scan.projects) {
    if (!sel.projects.has(p.path)) continue
    for (const s of newSessions(p)) {
      sessions++
      bytes += s.sizeBytes
    }
  }
  return { items, sessions, bytes }
}

export function canImport(scan: ClaudeImportScan, sel: ClaudeSelection): boolean {
  const t = selectionTotal(scan, sel)
  return scan.connected && t.items + t.sessions > 0
}

export function importRequest(sel: ClaudeSelection, claudeDir?: string): ClaudeImportRequest {
  return {
    sections: CLAUDE_SECTIONS.map(s => s.key).filter(k => sel.sections.has(k)),
    projectPaths: [...sel.projects],
    includeSecrets: sel.includeSecrets,
    force: sel.force,
    ...(claudeDir && { claudeDir })
  }
}

export function formatBytes(n: number): string {
  if (n >= 1e9) return `${(n / 1e9).toFixed(1)} GB`
  if (n >= 1e6) return `${(n / 1e6).toFixed(1)} MB`
  if (n >= 1e3) return `${Math.round(n / 1e3)} KB`
  return `${n} B`
}
