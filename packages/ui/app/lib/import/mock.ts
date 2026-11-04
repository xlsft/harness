// A believable machine to import from: the real case behind the scan's design, 151 projects with 289 transcripts in
// 2.1 GB. It remembers what it imported, so scanning again marks it and running again skips it, and one session fails
// on its first try, so the summary has an error to show.

import type { ClaudeImportApi, ClaudeImportProject, ClaudeImportRequest, ClaudeImportScan } from './claudeCode'
import type { ImportHandle, ImportHandlers, ImportItem, ImportReport } from './types'

export interface MockImportedSession {
  id: string
  title: string
  cwd: string
  updatedAt: string
}

export interface MockClaudeImportOptions {
  // Delay per item; 0 runs it on microtasks (tests).
  tick?: number
  // A session arrived in the session list.
  onSession?(s: MockImportedSession): void
  // Pretend this machine has no Claude Code, or no server.
  found?: boolean
  connected?: boolean
}

const PROJECT_COUNT = 151
const SESSION_COUNT = 289
const NAMES = ['harness', 'atlas', 'billing', 'dotfiles', 'gateway', 'lumen', 'notes', 'orbit', 'pager', 'quartz', 'relay', 'scribe', 'tally', 'vault', 'wharf', 'yarrow']
const TOPICS = ['Fix the flaky upload test', 'Add retries to the sync loop', 'Why does the sidebar jump', 'Move auth to the middleware', 'Write the migration', 'Clean up the settings screen', 'Review the object store change']
// The one that fails on its first try, and the one whose upload is put off.
const FAILS_ONCE = 7
const PUT_OFF = 13

const CONFIG: ImportItem[] = [
  { section: 'memory', kind: 'memory', name: 'user', status: 'new', note: '@imports are not resolved; imported files stay local' },
  { section: 'settings', kind: 'settings', name: 'user', status: 'new', note: 'secret env not uploaded: ANTHROPIC_API_KEY, GH_TOKEN (use Include secrets)' },
  { section: 'settings', kind: 'permissions', name: 'user', status: 'new', note: '12 allow, 2 deny' },
  { section: 'settings', kind: 'hooks', name: 'user', status: 'new', note: 'runs only after it is trusted on each device' },
  { section: 'skills', kind: 'skill', name: 'pdf', status: 'new', note: '2 extra files' },
  { section: 'skills', kind: 'skill', name: 'review', status: 'exists', note: '' },
  { section: 'skills', kind: 'skill', name: 'scratch', status: 'skipped', note: 'no SKILL.md' },
  { section: 'agents', kind: 'agent', name: 'code-reviewer', status: 'new', note: '' },
  { section: 'agents', kind: 'agent', name: 'planner', status: 'new', note: '' },
  { section: 'commands', kind: 'command', name: 'deploy', status: 'new', note: '' },
  { section: 'commands', kind: 'command', name: 'git:commit', status: 'new', note: '' },
  { section: 'mcp', kind: 'mcp', name: 'github', status: 'manual', note: 'http https://api.example.com/mcp; 1 env/header values not imported' },
  { section: 'mcp', kind: 'mcp', name: 'filesystem', status: 'manual', note: 'stdio npx; add it on the server' }
]

function projects(): ClaudeImportProject[] {
  const out: ClaudeImportProject[] = []
  const day = 86_400_000
  const newest = Date.UTC(2026, 9, 30)
  let n = 0
  for (let i = 0; i < PROJECT_COUNT; i++) {
    // 138 projects with two sessions and the rest with one come to 289.
    const count = i < SESSION_COUNT - PROJECT_COUNT ? 2 : 1
    const name = NAMES[i % NAMES.length]!
    const path = `/Users/you/Projects/${name}${i < NAMES.length ? '' : `-${i}`}`
    const sessions = []
    for (let j = 0; j < count; j++, n++) {
      const size = 2_000_000 + ((n * 7_919_317) % 12_500_000)
      sessions.push({
        id: `00000000-0000-4000-8000-${String(n).padStart(12, '0')}`,
        title: TOPICS[n % TOPICS.length]!,
        sizeBytes: size,
        updatedAt: new Date(newest - (n * day) / 3).toISOString(),
        status: 'new' as const,
        note: ''
      })
    }
    out.push({
      folder: path.replaceAll('/', '-'),
      path,
      sessionCount: count,
      importedCount: 0,
      totalBytes: sessions.reduce((a, s) => a + s.sizeBytes, 0),
      newestAt: sessions[0]!.updatedAt,
      sessions
    })
  }
  return out
}

export class MockClaudeImportApi implements ClaudeImportApi {
  private tick: number
  private opts: MockClaudeImportOptions
  private data = projects()
  private docs = new Map<string, 'exists'>()
  private failed = false

  constructor(opts: MockClaudeImportOptions = {}) {
    this.tick = opts.tick ?? 80
    this.opts = opts
  }

  private itemStatus(i: ImportItem) {
    return i.status === 'new' && this.docs.has(`${i.kind}/${i.name}`) ? 'exists' as const : i.status
  }

  async scan(claudeDir?: string): Promise<ClaudeImportScan> {
    const dir = claudeDir ?? '/Users/you/.claude'
    if (this.opts.found === false) return { found: false, claudeDir: dir, items: [], projects: [], connected: this.opts.connected ?? true, serverChecked: true }
    return {
      found: true,
      claudeDir: dir,
      items: CONFIG.map(i => ({ ...i, status: this.itemStatus(i) })),
      projects: this.data.map(p => ({ ...p, sessions: p.sessions.map(s => ({ ...s })) })),
      connected: this.opts.connected ?? true,
      serverChecked: true
    }
  }

  private markImported(id: string) {
    for (const p of this.data) {
      const s = p.sessions.find(x => x.id === id)
      if (s && s.status !== 'exists') {
        s.status = 'exists'
        s.note = 'already imported'
        p.importedCount++
        this.opts.onSession?.({ id, title: s.title, cwd: p.path, updatedAt: s.updatedAt })
      }
    }
  }

  run(req: ClaudeImportRequest, handlers: ImportHandlers = {}): ImportHandle {
    if (this.opts.connected === false) return { done: Promise.reject(new Error('importing needs a harness server; this client is not connected to one')), cancel: () => {} }
    const items = CONFIG.filter(i => req.sections.includes(i.section as never))
    const chosen = this.data.filter(p => req.projectPaths.includes(p.path)).flatMap(p => p.sessions.map(s => ({ s, p })))
    const total = items.length + chosen.length
    const report: ImportReport = { items: [], imported: 0, exists: 0, skipped: 0, queued: 0, errors: 0, manual: 0, error: '' }
    let cancelled = false
    let wake: (() => void) | undefined
    const pause = () => new Promise<void>((resolve) => {
      if (this.tick <= 0) {
        void Promise.resolve().then(resolve)
        return
      }
      const t = setTimeout(resolve, this.tick)
      wake = () => {
        clearTimeout(t)
        resolve()
      }
    })
    const done = (async () => {
      let n = 0
      const finish = (item: ImportItem) => {
        report.items.push(item)
        const key = ({ imported: 'imported', exists: 'exists', skipped: 'skipped', queued: 'queued', error: 'errors', manual: 'manual', new: 'skipped' } as const)[item.status]
        report[key]++
        n++
        handlers.onProgress?.({ current: `${item.kind} ${item.name}`, done: n, total, item })
      }
      for (const i of items) {
        handlers.onProgress?.({ current: `${i.kind} ${i.name}`, done: n, total })
        await pause()
        if (cancelled) return null
        const status = this.itemStatus(i)
        if (status === 'exists' && !req.force) finish({ ...i, status: 'exists', note: 'already on the server (use Overwrite existing to overwrite)' })
        else if (status === 'new' || status === 'exists') {
          this.docs.set(`${i.kind}/${i.name}`, 'exists')
          finish({ ...i, status: 'imported', note: i.kind === 'settings' && req.includeSecrets ? '' : i.note })
        } else finish({ ...i })
      }
      for (const { s } of chosen) {
        handlers.onProgress?.({ current: `session ${s.title}`, done: n, total })
        await pause()
        if (cancelled) return null
        const index = Number(s.id.slice(-12))
        const base = { section: 'sessions', kind: 'session', name: s.title }
        if (s.status === 'exists') {
          finish({ ...base, status: 'exists', note: 'already copied' })
        } else if (index === FAILS_ONCE && !this.failed) {
          this.failed = true
          finish({ ...base, status: 'error', note: 'copy: input/output error' })
        } else if (index === PUT_OFF) {
          finish({ ...base, status: 'queued', note: 'upload deferred: server busy' })
        } else {
          this.markImported(s.id)
          finish({ ...base, status: 'imported', note: '' })
        }
      }
      return report
    })()
    return {
      done,
      cancel: () => {
        cancelled = true
        wake?.()
      }
    }
  }
}
