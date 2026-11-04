// ClaudeImportApi on the agent transport. Cancelling aborts the call, which stops the run on the client.

import { Code, ConnectError, createClient, type Client, type Transport } from '@connectrpc/connect'
import { AgentService } from '~/gen/harness/agent/v1/agent_pb'
import {
  ClaudeImportSection,
  ClaudeImportStatus,
  type ClaudeImportItem as ItemPb,
  type ClaudeImportProject as ProjectPb,
  type ClaudeImportReport as ReportPb,
  type ScanClaudeImportResponse
} from '~/gen/harness/agent/v1/import_pb'
import type { ClaudeImportApi, ClaudeImportProject, ClaudeImportRequest, ClaudeImportScan, ClaudeSection } from './claudeCode'
import type { ImportHandle, ImportHandlers, ImportItem, ImportReport, ImportStatus } from './types'

const SECTION_NAME: Record<ClaudeImportSection, ClaudeSection | ''> = {
  [ClaudeImportSection.UNSPECIFIED]: '',
  [ClaudeImportSection.MEMORY]: 'memory',
  [ClaudeImportSection.SETTINGS]: 'settings',
  [ClaudeImportSection.SKILLS]: 'skills',
  [ClaudeImportSection.AGENTS]: 'agents',
  [ClaudeImportSection.COMMANDS]: 'commands',
  [ClaudeImportSection.OUTPUT_STYLES]: 'output-styles',
  [ClaudeImportSection.MCP_SERVERS]: 'mcp',
  [ClaudeImportSection.SESSIONS]: 'sessions'
}

const SECTION_PB: Partial<Record<ClaudeSection, ClaudeImportSection>> = {
  'memory': ClaudeImportSection.MEMORY,
  'settings': ClaudeImportSection.SETTINGS,
  'skills': ClaudeImportSection.SKILLS,
  'agents': ClaudeImportSection.AGENTS,
  'commands': ClaudeImportSection.COMMANDS,
  'output-styles': ClaudeImportSection.OUTPUT_STYLES
}

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

export function importItemView(i: ItemPb): ImportItem {
  return { section: SECTION_NAME[i.section], kind: i.kind, name: i.name, status: STATUS_NAME[i.status], note: i.note }
}

export function importProjectView(p: ProjectPb): ClaudeImportProject {
  return {
    folder: p.folder,
    path: p.path,
    sessionCount: p.sessionCount,
    importedCount: p.importedCount,
    totalBytes: Number(p.totalBytes),
    newestAt: p.newestAt,
    sessions: p.sessions.map(s => ({ id: s.id, title: s.title, sizeBytes: Number(s.sizeBytes), updatedAt: s.updatedAt, status: STATUS_NAME[s.status], note: s.note }))
  }
}

export function importScanView(r: ScanClaudeImportResponse): ClaudeImportScan {
  return { found: r.found, claudeDir: r.claudeDir, items: r.items.map(importItemView), projects: r.projects.map(importProjectView), connected: r.connected, serverChecked: r.serverChecked }
}

export function importReportView(r: ReportPb): ImportReport {
  return { items: r.items.map(importItemView), imported: r.imported, exists: r.exists, skipped: r.skipped, queued: r.queued, errors: r.errors, manual: r.manual, error: r.error }
}

export class ConnectClaudeImportApi implements ClaudeImportApi {
  private client: Client<typeof AgentService>

  constructor(transport: Transport) {
    this.client = createClient(AgentService, transport)
  }

  async scan(claudeDir?: string) {
    return importScanView(await this.client.scanClaudeImport({ claudeDir }))
  }

  run(req: ClaudeImportRequest, handlers: ImportHandlers = {}): ImportHandle {
    const abort = new AbortController()
    const sections = req.sections.map(s => SECTION_PB[s]).filter((s): s is ClaudeImportSection => s !== undefined)
    const done = (async () => {
      let report: ImportReport | null = null
      try {
        const stream = this.client.runClaudeImport({
          sections,
          projectPaths: req.projectPaths,
          sessionIds: req.sessionIds ?? [],
          includeSecrets: req.includeSecrets,
          force: req.force,
          claudeDir: req.claudeDir
        }, { signal: abort.signal })
        for await (const m of stream) {
          if (m.event.case === 'progress') {
            const p = m.event.value
            handlers.onProgress?.({ current: p.current, done: p.done, total: p.total, item: p.item ? importItemView(p.item) : undefined })
          } else if (m.event.case === 'report') {
            report = importReportView(m.event.value)
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
