// Importing a folder's Claude Code files into its .harness and AGENTS.md (AgentService ScanProjectImport, RunProjectImport and
// DismissProjectImport, proto/harness/agent/v1/project_import.proto). The client decides what there is and whether to offer it,
// and remembers a dismissal on this device: this only renders the answer.

import { createClient, type Client, type Transport } from '@connectrpc/connect'
import { AgentService } from '~/gen/harness/agent/v1/agent_pb'
import { ClaudeImportStatus } from '~/gen/harness/agent/v1/import_pb'
import type { ProjectImportItem as ItemPb } from '~/gen/harness/agent/v1/project_import_pb'

export type ProjectImportStatus = 'new' | 'exists' | 'skipped' | 'imported' | 'error'

export interface ProjectImportItem {
  kind: string
  source: string
  target: string
  status: ProjectImportStatus
  note: string
  // It holds hooks or allow rules, which apply only once the folder is trusted.
  needsTrust: boolean
}

export interface ProjectImportScan {
  root: string
  items: ProjectImportItem[]
  offer: boolean
  dismissed: boolean
  trusted: boolean
}

export interface ProjectImportReport {
  root: string
  items: ProjectImportItem[]
  imported: number
  exists: number
  skipped: number
  errors: number
}

export interface ProjectImportApi {
  scan(path: string): Promise<ProjectImportScan>
  run(path: string, force?: boolean): Promise<ProjectImportReport>
  dismiss(path: string, dismissed?: boolean): Promise<void>
}

// What an import would bring, for the offer's list.
export function comingOver(items: ProjectImportItem[]): ProjectImportItem[] {
  return items.filter(i => i.status === 'new')
}

// "1 instructions file, 3 skills": what comes over, by kind, in the order found.
export function offerSummary(items: ProjectImportItem[]): string {
  const counts = new Map<string, number>()
  for (const i of comingOver(items)) counts.set(i.kind, (counts.get(i.kind) ?? 0) + 1)
  return [...counts].map(([kind, n]) => `${n} ${kindLabel(kind, n)}`).join(', ')
}

const KIND_LABELS: Record<string, [string, string]> = {
  'instructions': ['instructions file', 'instructions files'],
  'rule': ['rule', 'rules'],
  'settings': ['settings file', 'settings files'],
  'hook-script': ['hook script', 'hook scripts'],
  'skill': ['skill', 'skills'],
  'command': ['command', 'commands'],
  'agent': ['agent', 'agents'],
  'output-style': ['output style', 'output styles'],
  'workflow': ['workflow', 'workflows']
}

export function kindLabel(kind: string, n = 1): string {
  const l = KIND_LABELS[kind]
  return l ? l[n === 1 ? 0 : 1] : kind
}

const STATUS_NAME: Partial<Record<ClaudeImportStatus, ProjectImportStatus>> = {
  [ClaudeImportStatus.NEW]: 'new',
  [ClaudeImportStatus.EXISTS]: 'exists',
  [ClaudeImportStatus.IMPORTED]: 'imported',
  [ClaudeImportStatus.ERROR]: 'error'
}

export function projectItemView(i: ItemPb): ProjectImportItem {
  return { kind: i.kind, source: i.source, target: i.target, status: STATUS_NAME[i.status] ?? 'skipped', note: i.note, needsTrust: i.needsTrust }
}

export class ConnectProjectImportApi implements ProjectImportApi {
  private client: Client<typeof AgentService>

  constructor(transport: Transport) {
    this.client = createClient(AgentService, transport)
  }

  async scan(path: string): Promise<ProjectImportScan> {
    const r = await this.client.scanProjectImport({ path })
    return { root: r.root, items: r.items.map(projectItemView), offer: r.offer, dismissed: r.dismissed, trusted: r.trusted }
  }

  async run(path: string, force = false): Promise<ProjectImportReport> {
    const r = await this.client.runProjectImport({ path, force })
    return { root: r.root, items: r.items.map(projectItemView), imported: r.imported, exists: r.exists, skipped: r.skipped, errors: r.errors }
  }

  async dismiss(path: string, dismissed = true): Promise<void> {
    await this.client.dismissProjectImport({ path, dismissed })
  }
}

// A believable folder with Claude Code files; the others have none. It remembers an import and a dismissal.
export class MockProjectImportApi implements ProjectImportApi {
  private imported = new Set<string>()
  private dismissed = new Set<string>()

  constructor(private hasFiles: (path: string) => boolean = p => p.endsWith('/dashboard')) {}

  private items(path: string, status: ProjectImportStatus): ProjectImportItem[] {
    if (!this.hasFiles(path)) return []
    const item = (kind: string, source: string, target: string, note = '', needsTrust = false) => ({ kind, source, target, status, note, needsTrust })
    return [
      item('instructions', 'CLAUDE.md', 'AGENTS.md'),
      item('rule', '.claude/rules/api.md', '.harness/rules/api.md'),
      item('settings', '.claude/settings.json', '.harness/settings.json', '2 allow rules and hooks on PostToolUse, which apply only once this folder is trusted', true),
      item('skill', '.claude/skills/release', '.harness/skills/release'),
      item('command', '.claude/commands/test.md', '.harness/commands/test.md')
    ]
  }

  async scan(path: string): Promise<ProjectImportScan> {
    const done = this.imported.has(path)
    const items = this.items(path, done ? 'exists' : 'new')
    const dismissed = this.dismissed.has(path)
    return { root: path, items, offer: items.length > 0 && !done && !dismissed, dismissed, trusted: false }
  }

  async run(path: string): Promise<ProjectImportReport> {
    const done = this.imported.has(path)
    const items = this.items(path, done ? 'exists' : 'imported')
    this.imported.add(path)
    return { root: path, items, imported: done ? 0 : items.length, exists: done ? items.length : 0, skipped: 0, errors: 0 }
  }

  async dismiss(path: string, dismissed = true): Promise<void> {
    if (dismissed) this.dismissed.add(path)
    else this.dismissed.delete(path)
  }
}
