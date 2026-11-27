// The browser mock of the import sources: the client's list as it is today, a Claude Desktop with two MCP servers and
// a folder with files of several tools. It remembers what it imported, so scanning again marks it.

import { tt } from '~/utils/i18n'
import type { ImportHandle, ImportHandlers, ImportItem, ImportReport } from './types'
import type { ImportSourceInfo, ImportSourcesApi, SourceItem, SourceRunRequest, SourceScan } from './sources'

const folderOnly = (id: string, name: string, logo: string, description: string): ImportSourceInfo =>
  ({ id, name, logo, description, userLevel: false, folderLevel: true, detected: false, screen: '' })

// As ListImportSources answers: the Claude Code imports first, then the engine's sources by name, Any folder last.
const both = (id: string, name: string, logo: string, detected: boolean, description: string): ImportSourceInfo =>
  ({ ...folderOnly(id, name, logo, description), userLevel: true, detected })

export const MOCK_IMPORT_SOURCES: ImportSourceInfo[] = [
  { id: 'claude-code', name: 'Claude Code', logo: 'claude', userLevel: true, folderLevel: false, detected: true, screen: 'claude-code', description: tt('Memory, rules, settings, skills, agents, commands, output styles, workflows, MCP servers and sessions on this computer.') },
  { id: 'claude-project', name: 'A folder\'s Claude Code files', logo: 'claude', userLevel: false, folderLevel: true, detected: false, screen: 'claude-project', description: tt('CLAUDE.md and what is under .claude, into the folder\'s AGENTS.md and .harness.') },
  folderOnly('aider', 'Aider', 'aider', 'A folder\'s CONVENTIONS.md.'),
  { id: 'claude-desktop', name: 'Claude Desktop', logo: 'claude', userLevel: true, folderLevel: false, detected: true, screen: '', description: tt('MCP servers from claude_desktop_config.json, added on this device.') },
  folderOnly('cline', 'Cline', 'cline', 'A folder\'s Cline rules.'),
  folderOnly('continue', 'Continue', 'continue', 'A folder\'s Continue rules.'),
  both('cursor', 'Cursor', 'cursor', true, 'MCP servers and skills from ~/.cursor; a folder\'s Cursor rules, .cursorrules and .cursor/mcp.json.'),
  both('codex', 'Codex CLI', 'codex', true, 'AGENTS.md, MCP servers, custom prompts, agents and skills from ~/.codex; a folder\'s AGENTS.override.md.'),
  both('gemini', 'Gemini CLI', 'gemini', false, 'GEMINI.md, MCP servers, commands, agents, skills and extensions from ~/.gemini; a folder\'s GEMINI.md files and .gemini/settings.json.'),
  both('copilot', 'GitHub Copilot', 'github', false, 'Instructions, prompts, agents, skills and MCP servers from ~/.copilot and VS Code\'s user data; a folder\'s Copilot instructions and .vscode/mcp.json.'),
  folderOnly('junie', 'Junie', 'junie', 'A folder\'s Junie guidelines.'),
  folderOnly('kilo', 'Kilo Code', 'kilo', 'A folder\'s Kilo Code rules.'),
  folderOnly('kiro', 'Kiro', 'kiro', 'A folder\'s Kiro steering files.'),
  folderOnly('roo', 'Roo Code', 'roo', 'A folder\'s Roo Code rules.'),
  both('windsurf', 'Windsurf', 'windsurf', false, 'Global rules, rules, skills, MCP servers and workflows of Windsurf and Devin Desktop; a folder\'s Windsurf rules and .windsurfrules.'),
  folderOnly('zed', 'Zed', 'zed', 'A folder\'s .rules file.'),
  folderOnly('folder', 'Any folder', '', 'Every other tool\'s instructions, rules and MCP configs in a folder, in one pass.')
]

const it = (o: Partial<SourceItem> & Pick<SourceItem, 'kind' | 'name' | 'source'>): SourceItem => ({
  id: `${o.kind}:${o.source}#${o.name}`,
  section: o.kind === 'rule' ? 'instructions' : o.kind,
  target: '',
  status: 'new',
  note: '',
  needsTrust: false,
  hasSecrets: false,
  ...o
})

const DESKTOP: SourceItem[] = [
  it({ kind: 'mcp', name: 'filesystem', source: '~/Library/Application Support/Claude/claude_desktop_config.json', target: 'MCP server filesystem on this device', note: 'stdio npx -y @modelcontextprotocol/server-filesystem /Users/you/Desktop' }),
  it({ kind: 'mcp', name: 'weather api', source: '~/Library/Application Support/Claude/claude_desktop_config.json', target: 'MCP server weather-api on this device', hasSecrets: true, note: 'stdio uvx weather-mcp; named weather-api here; secret values left empty: WEATHER_API_KEY (set them in Settings > MCP, or use Include secrets)' })
]

const FOLDER: SourceItem[] = [
  it({ kind: 'instructions', name: '.cursorrules', source: '.cursorrules', target: 'AGENTS.md' }),
  it({ kind: 'rule', name: 'frontend/components', source: '.cursor/rules/frontend/components.mdc', target: '.harness/rules/frontend/components.md' }),
  it({ kind: 'mcp', name: 'docs', source: '.cursor/mcp.json', target: '.mcp.json', needsTrust: true, note: 'used once the folder is trusted and the server approved on this device' }),
  it({ kind: 'instructions', name: 'copilot-instructions.md', source: '.github/copilot-instructions.md', target: 'AGENTS.md' }),
  it({ kind: 'instructions', name: 'GEMINI.md', source: 'GEMINI.md', target: 'AGENTS.md', status: 'exists', note: 'already imported' }),
  it({ kind: 'rule', name: 'style', source: '.windsurf/rules/style.md', target: '.harness/rules/style.md', note: 'Windsurf used it when it judged it relevant; here it always applies' })
]

const CODEX: SourceItem[] = [
  it({ kind: 'instructions', name: 'AGENTS.md', source: '~/.codex/AGENTS.md', target: 'rule codex/AGENTS.md' }),
  it({ kind: 'mcp', name: 'docs', source: '~/.codex/config.toml', target: 'MCP server docs on this device', hasSecrets: true }),
  it({ kind: 'command', name: 'draftpr', source: '~/.codex/prompts/draftpr.md', target: 'command draftpr', note: 'Codex took FILES as KEY=value; here they are given in order' }),
  it({ kind: 'agent', name: 'reviewer', source: '~/.codex/agents/reviewer.toml', target: 'agent reviewer' }),
  it({ kind: 'skill', name: 'review', source: '~/.agents/skills/review', target: 'skill review' }),
  it({ kind: 'settings', name: 'Codex settings', source: '~/.codex/config.toml', status: 'manual', note: 'model = gpt-6: models and permission modes are chosen in Settings' }),
  it({ kind: 'session', name: 'Codex sessions', source: '~/.codex/sessions', status: 'manual', note: '12 session(s); sessions from this tool are not imported yet' })
]

const ITEMS: Record<string, SourceItem[]> = { 'claude-desktop': DESKTOP, 'folder': FOLDER, 'cursor': FOLDER.filter(i => i.source.startsWith('.cursor')), 'codex': CODEX }

export interface MockImportSourcesOptions {
  // Delay per item; 0 runs it on microtasks (tests).
  tick?: number
}

export class MockImportSourcesApi implements ImportSourcesApi {
  private tick: number
  private done = new Set<string>()

  constructor(opts: MockImportSourcesOptions = {}) {
    this.tick = opts.tick ?? 120
  }

  async list() {
    return { sources: MOCK_IMPORT_SOURCES.map(s => ({ ...s })), connected: true }
  }

  private items(sourceId: string) {
    return (ITEMS[sourceId] ?? []).map(i => (this.done.has(i.id) ? { ...i, status: 'exists' as const, note: 'already there' } : { ...i }))
  }

  async scan(sourceId: string, folder?: string): Promise<SourceScan> {
    const info = MOCK_IMPORT_SOURCES.find(s => s.id === sourceId)
    if (!info) throw new Error(`no import source "${sourceId}"`)
    if (info.folderLevel && !info.userLevel && !folder) throw new Error(`${info.name} imports from a folder: choose one`)
    const items = this.items(sourceId)
    return { sourceId, root: folder ?? '', found: items.length > 0, items, connected: true, serverChecked: true, trusted: false }
  }

  run(req: SourceRunRequest, handlers: ImportHandlers = {}): ImportHandle {
    const chosen = this.items(req.sourceId).filter(i => req.itemIds.includes(i.id))
    const report: ImportReport = { items: [], imported: 0, exists: 0, skipped: 0, queued: 0, errors: 0, manual: 0, error: '' }
    let cancelled = false
    const pause = () => new Promise<void>(resolve => (this.tick <= 0 ? void Promise.resolve().then(resolve) : setTimeout(resolve, this.tick)))
    const done = (async () => {
      let n = 0
      for (const i of chosen) {
        handlers.onProgress?.({ current: `${i.kind} ${i.name}`, done: n, total: chosen.length })
        await pause()
        if (cancelled) return null
        let item: ImportItem
        if (i.status === 'exists' && !req.force) {
          item = { ...i, note: 'already there' }
          report.exists++
        } else {
          this.done.add(i.id)
          item = { ...i, status: 'imported', note: i.hasSecrets && req.includeSecrets ? 'secrets stored on this device' : i.note }
          report.imported++
        }
        report.items.push(item)
        n++
        handlers.onProgress?.({ current: `${i.kind} ${i.name}`, done: n, total: chosen.length, item })
      }
      return report
    })()
    return { done, cancel: () => (cancelled = true) }
  }
}
