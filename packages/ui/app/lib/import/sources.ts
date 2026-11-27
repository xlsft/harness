// Importing from every other tool (AgentService ListImportSources, ScanImportSource and RunImportSource,
// proto/harness/agent/v1/import_sources.proto). The client's import engine decides which tools are there, what each
// item is, where it goes and whether it is already there; this only renders the answer and sends back the ids the user
// chose. The Claude Code imports are listed too, with their own screens (`screen`).

import { Code, ConnectError, createClient, type Client, type Transport } from '@connectrpc/connect'
import { AgentService } from '~/gen/harness/agent/v1/agent_pb'
import { ClaudeImportStatus } from '~/gen/harness/agent/v1/import_pb'
import type { ImportSourceInfo as InfoPb, ImportSourceItem as ItemPb, ImportSourceReport as ReportPb, ScanImportSourceResponse } from '~/gen/harness/agent/v1/import_sources_pb'
import { tt } from '~/utils/i18n'
import type { ImportHandle, ImportHandlers, ImportItem, ImportReport, ImportStatus } from './types'

export interface ImportSourceInfo {
  id: string
  name: string
  description: string
  // A key of PROVIDER_LOGOS; '' shows the initials.
  logo: string
  userLevel: boolean
  folderLevel: boolean
  detected: boolean
  // 'claude-code' or 'claude-project' for the sources with a screen of their own; '' for the generic one.
  screen: string
}

export interface ImportSourceList {
  sources: ImportSourceInfo[]
  connected: boolean
}

export interface SourceItem extends ImportItem {
  id: string
  source: string
  target: string
  needsTrust: boolean
  hasSecrets: boolean
}

export interface SourceScan {
  sourceId: string
  root: string
  found: boolean
  items: SourceItem[]
  connected: boolean
  serverChecked: boolean
  trusted: boolean
}

export interface SourceRunRequest {
  sourceId: string
  folder?: string
  itemIds: string[]
  includeSecrets: boolean
  force: boolean
}

export interface ImportSourcesApi {
  list(): Promise<ImportSourceList>
  scan(sourceId: string, folder?: string): Promise<SourceScan>
  run(req: SourceRunRequest, handlers?: ImportHandlers): ImportHandle
}

// --- What the screen shows, decided from the client's answer ---

export const SECTION_LABELS: Record<string, string> = {
  instructions: tt('Instructions and rules'),
  mcp: tt('MCP servers'),
  commands: tt('Commands'),
  agents: tt('Agents'),
  skills: tt('Skills'),
  settings: tt('Settings'),
  sessions: tt('Sessions')
}

const SECTION_ORDER = ['instructions', 'mcp', 'commands', 'agents', 'skills', 'settings', 'sessions']

export interface SourceSection {
  key: string
  label: string
  items: SourceItem[]
}

/** The items grouped by section, in a fixed order; unknown sections come last under their own name. */
export function sourceSections(items: SourceItem[]): SourceSection[] {
  const keys = [...new Set(items.map(i => i.section))].sort((a, b) => {
    const ia = SECTION_ORDER.indexOf(a)
    const ib = SECTION_ORDER.indexOf(b)
    return (ia < 0 ? 99 : ia) - (ib < 0 ? 99 : ib)
  })
  return keys.map(key => ({ key, label: SECTION_LABELS[key] ?? key, items: items.filter(i => i.section === key) }))
}

/** Whether an item can be chosen: new, or already there (it comes again with force). */
export function choosable(i: SourceItem, force: boolean): boolean {
  return i.status === 'new' || (force && i.status === 'exists')
}

/** What a fresh scan picks: everything new except sessions, which are opt-in as in the Claude Code import. */
export function defaultChoice(items: SourceItem[]): Set<string> {
  return new Set(items.filter(i => i.status === 'new' && i.kind !== 'session').map(i => i.id))
}

/** The ids to send: what is picked and still choosable. */
export function chosenIds(items: SourceItem[], picked: ReadonlySet<string>, force: boolean): string[] {
  return items.filter(i => picked.has(i.id) && choosable(i, force)).map(i => i.id)
}

/** Include secrets matters only when a chosen item has values that look like secrets. */
export function secretsMatter(items: SourceItem[], picked: ReadonlySet<string>): boolean {
  return items.some(i => picked.has(i.id) && i.hasSecrets)
}

/** Picks a section's choosable items, or takes them back when all are picked. */
export function toggleSection(section: SourceSection, picked: ReadonlySet<string>, force: boolean): Set<string> {
  const next = new Set(picked)
  const list = section.items.filter(i => choosable(i, force))
  const all = list.length > 0 && list.every(i => next.has(i.id))
  for (const i of list) {
    if (all) next.delete(i.id)
    else next.add(i.id)
  }
  return next
}

// --- Connect ---

const STATUS_NAME: Record<ClaudeImportStatus, ImportStatus> = {
  [ClaudeImportStatus.UNSPECIFIED]: 'skipped',
  [ClaudeImportStatus.NEW]: 'new',
  [ClaudeImportStatus.EXISTS]: 'exists',
  [ClaudeImportStatus.SKIPPED]: 'skipped',
  [ClaudeImportStatus.MANUAL]: 'manual',
  [ClaudeImportStatus.IMPORTED]: 'imported',
  [ClaudeImportStatus.QUEUED]: 'queued',
  [ClaudeImportStatus.ERROR]: 'error'
}

export function sourceInfoView(s: InfoPb): ImportSourceInfo {
  return { id: s.id, name: s.name, description: s.description, logo: s.logo, userLevel: s.userLevel, folderLevel: s.folderLevel, detected: s.detected, screen: s.screen }
}

export function sourceItemView(i: ItemPb): SourceItem {
  return { id: i.id, section: i.section, kind: i.kind, name: i.name, source: i.source, target: i.target, status: STATUS_NAME[i.status], note: i.note, needsTrust: i.needsTrust, hasSecrets: i.hasSecrets }
}

export function sourceScanView(r: ScanImportSourceResponse): SourceScan {
  return { sourceId: r.sourceId, root: r.root, found: r.found, items: r.items.map(sourceItemView), connected: r.connected, serverChecked: r.serverChecked, trusted: r.trusted }
}

export function sourceReportView(r: ReportPb): ImportReport {
  return { items: r.items.map(sourceItemView), imported: r.imported, exists: r.exists, skipped: r.skipped, queued: 0, errors: r.errors, manual: r.manual, error: r.error }
}

export class ConnectImportSourcesApi implements ImportSourcesApi {
  private client: Client<typeof AgentService>

  constructor(transport: Transport) {
    this.client = createClient(AgentService, transport)
  }

  async list() {
    const r = await this.client.listImportSources({})
    return { sources: r.sources.map(sourceInfoView), connected: r.connected }
  }

  async scan(sourceId: string, folder?: string) {
    return sourceScanView(await this.client.scanImportSource({ sourceId, folder }))
  }

  run(req: SourceRunRequest, handlers: ImportHandlers = {}): ImportHandle {
    const abort = new AbortController()
    const done = (async () => {
      let report: ImportReport | null = null
      try {
        const stream = this.client.runImportSource({ sourceId: req.sourceId, folder: req.folder, itemIds: req.itemIds, includeSecrets: req.includeSecrets, force: req.force }, { signal: abort.signal })
        for await (const m of stream) {
          if (m.event.case === 'progress') {
            const p = m.event.value
            handlers.onProgress?.({ current: p.current, done: p.done, total: p.total, item: p.item ? sourceItemView(p.item) : undefined })
          } else if (m.event.case === 'report') {
            report = sourceReportView(m.event.value)
          }
        }
      } catch (e) {
        if (abort.signal.aborted || ConnectError.from(e).code === Code.Canceled) return null
        throw e
      }
      return report
    })()
    return { done, cancel: () => abort.abort() }
  }
}
