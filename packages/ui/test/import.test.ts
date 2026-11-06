import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { ClaudeImportReportSchema, ClaudeImportSection, ClaudeImportStatus, ScanClaudeImportResponseSchema } from '~/gen/harness/agent/v1/import_pb'
import {
  EMPTY_SELECTION,
  canImport,
  filterProjects,
  formatBytes,
  importRequest,
  manualItems,
  newSessions,
  sectionRows,
  selectAllState,
  selectionTotal,
  toggleAll,
  toggled,
  type ClaudeImportProject,
  type ClaudeImportScan
} from '~/lib/import/claudeCode'
import { importReportView, importScanView } from '~/lib/import/connect'
import { MockClaudeImportApi } from '~/lib/import/mock'
import { IDLE_RUN, IMPORT_SOURCES, importCounts, importErrors, reduceImportRun, type ImportItem, type ImportReport } from '~/lib/import/types'
import { MockTransport } from '~/lib/transport/mock'

function project(path: string, statuses: ('new' | 'exists' | 'skipped')[], size = 100): ClaudeImportProject {
  return {
    folder: path.replaceAll('/', '-'),
    path,
    sessionCount: statuses.length,
    importedCount: statuses.filter(s => s !== 'new').length,
    totalBytes: size * statuses.length,
    newestAt: '2026-10-01T00:00:00Z',
    sessions: statuses.map((status, i) => ({ id: `${path}-${i}`, title: `about ${path}`, sizeBytes: size, updatedAt: '2026-10-01T00:00:00Z', status, note: '' }))
  }
}

const item = (section: string, name: string, status: ImportItem['status']): ImportItem => ({ section, kind: section, name, status, note: '' })

function scan(over: Partial<ClaudeImportScan> = {}): ClaudeImportScan {
  return {
    found: true,
    claudeDir: '/home/.claude',
    connected: true,
    serverChecked: true,
    items: [item('memory', 'user', 'new'), item('skills', 'a', 'new'), item('skills', 'b', 'exists'), item('agents', 'x', 'exists'), item('commands', 'broken', 'skipped'), item('mcp', 'gh', 'manual')],
    projects: [project('/a', ['new', 'exists']), project('/b', ['new']), project('/c', ['exists']), project('/d', ['skipped'])],
    ...over
  }
}

describe('import sources', () => {
  it('lists Claude Code, the one source for now', () => {
    expect(IMPORT_SOURCES.map(s => s.id)).toEqual(['claude-code', 'claude-project'])
  })
})

describe('Claude Code selection', () => {
  it('starts with nothing chosen and force and secrets off', () => {
    const s = scan()
    expect(selectionTotal(s, EMPTY_SELECTION)).toEqual({ items: 0, sessions: 0, bytes: 0 })
    expect(canImport(s, EMPTY_SELECTION)).toBe(false)
    expect(EMPTY_SELECTION.force).toBe(false)
    expect(EMPTY_SELECTION.includeSecrets).toBe(false)
  })

  it('groups items by kind, with MCP servers only listed', () => {
    const rows = sectionRows(scan())
    expect(rows.map(r => r.key)).toEqual(['memory', 'settings', 'skills', 'agents', 'commands', 'output-styles', 'workflows'])
    const skills = rows.find(r => r.key === 'skills')!
    expect([skills.fresh, skills.exists, skills.selectable]).toEqual([1, 1, true])
    // Nothing found, or nothing but skipped items, has nothing to choose.
    expect(rows.find(r => r.key === 'settings')!.selectable).toBe(false)
    expect(rows.find(r => r.key === 'commands')!.selectable).toBe(false)
    expect(manualItems(scan()).map(i => i.name)).toEqual(['gh'])
  })

  it('searches projects by path and by session title', () => {
    const { projects } = scan()
    expect(filterProjects(projects, '').length).toBe(4)
    expect(filterProjects(projects, ' /B ').map(p => p.path)).toEqual(['/b'])
    expect(filterProjects(projects, 'about /c').map(p => p.path)).toEqual(['/c'])
    expect(filterProjects(projects, 'zzz')).toEqual([])
  })

  it('selects all of what is visible and has something to bring', () => {
    const { projects } = scan()
    expect(selectAllState(projects, new Set())).toBe('none')
    const all = toggleAll(projects, new Set())
    expect([...all].sort()).toEqual(['/a', '/b'])
    expect(selectAllState(projects, all)).toBe('all')
    expect(selectAllState(projects, new Set(['/a']))).toBe('some')
    expect(toggleAll(projects, all).size).toBe(0)
    // A search only touches what it shows.
    const shown = filterProjects(projects, '/b')
    const kept = toggleAll(shown, new Set(['/a']))
    expect([...kept].sort()).toEqual(['/a', '/b'])
    expect([...toggleAll(shown, kept)]).toEqual(['/a'])
  })

  it('totals what would come: new items and new sessions', () => {
    const s = scan()
    const sel = { ...EMPTY_SELECTION, sections: new Set(['memory', 'skills'] as const), projects: new Set(['/a', '/b', '/c']) }
    expect(selectionTotal(s, sel)).toEqual({ items: 2, sessions: 2, bytes: 200 })
    // Force brings what exists too.
    expect(selectionTotal(s, { ...sel, force: true }).items).toBe(3)
    expect(canImport(s, sel)).toBe(true)
    expect(canImport(scan({ connected: false }), sel)).toBe(false)
    // Only what exists, without force, is nothing to do.
    expect(canImport(s, { ...EMPTY_SELECTION, sections: new Set(['agents'] as const) })).toBe(false)
    expect(canImport(s, { ...EMPTY_SELECTION, sections: new Set(['agents'] as const), force: true })).toBe(true)
  })

  it('sends the intent as chosen', () => {
    const sel = { sections: new Set(['skills', 'memory'] as const), projects: toggled(new Set<string>(), '/a'), force: true, includeSecrets: false }
    expect(importRequest(sel)).toEqual({ sections: ['memory', 'skills'], projectPaths: ['/a'], includeSecrets: false, force: true })
    expect(importRequest(EMPTY_SELECTION, '/x').claudeDir).toBe('/x')
  })

  it('says sizes in words', () => {
    expect([formatBytes(2_100_000_000), formatBytes(3_400_000), formatBytes(1200), formatBytes(5)]).toEqual(['2.1 GB', '3.4 MB', '1 KB', '5 B'])
  })
})

describe('import run reducer', () => {
  const report: ImportReport = { items: [item('memory', 'user', 'imported')], imported: 1, exists: 0, skipped: 0, queued: 0, errors: 0, manual: 0, error: '' }

  it('follows progress to the report', () => {
    let r = reduceImportRun(IDLE_RUN, { type: 'start' })
    expect(r.phase).toBe('running')
    r = reduceImportRun(r, { type: 'progress', progress: { current: 'skill a', done: 0, total: 2 } })
    expect([r.current, r.done, r.total, r.items.length]).toEqual(['skill a', 0, 2, 0])
    r = reduceImportRun(r, { type: 'progress', progress: { current: 'skill a', done: 1, total: 2, item: item('skills', 'a', 'error') } })
    expect(r.items.length).toBe(1)
    expect(importErrors(r).map(i => i.name)).toEqual(['a'])
    r = reduceImportRun(r, { type: 'report', report })
    expect(r.phase).toBe('done')
    expect(importCounts(r).imported).toBe(1)
    expect(importErrors(r)).toEqual([])
  })

  it('keeps what finished when cancelled, and ignores late progress', () => {
    let r = reduceImportRun(IDLE_RUN, { type: 'start' })
    r = reduceImportRun(r, { type: 'progress', progress: { current: 'x', done: 2, total: 5, item: item('skills', 'a', 'imported') } })
    r = reduceImportRun(r, { type: 'progress', progress: { current: 'y', done: 3, total: 5, item: item('skills', 'b', 'error') } })
    r = reduceImportRun(r, { type: 'cancelled' })
    expect(r.phase).toBe('cancelled')
    expect([r.done, r.total]).toEqual([3, 5])
    expect(importCounts(r)).toMatchObject({ imported: 1, errors: 1 })
    expect(reduceImportRun(r, { type: 'progress', progress: { current: 'z', done: 4, total: 5 } })).toBe(r)
    expect(reduceImportRun(reduceImportRun(r, { type: 'start' }), { type: 'cancelled' }).items).toEqual([])
  })

  it('tells a run that stopped from one that finished', () => {
    const stopped = reduceImportRun(reduceImportRun(IDLE_RUN, { type: 'start' }), { type: 'report', report: { ...report, error: 'upload: unreachable' } })
    expect([stopped.phase, stopped.error, stopped.items.length]).toEqual(['failed', 'upload: unreachable', 1])
    const failed = reduceImportRun(reduceImportRun(IDLE_RUN, { type: 'start' }), { type: 'failed', error: 'no server' })
    expect([failed.phase, failed.error]).toEqual(['failed', 'no server'])
  })
})

describe('import views', () => {
  it('reads the scan', () => {
    const v = importScanView(create(ScanClaudeImportResponseSchema, {
      found: true,
      claudeDir: '/c',
      connected: true,
      serverChecked: true,
      items: [{ section: ClaudeImportSection.OUTPUT_STYLES, kind: 'output-style', name: 'Terse', status: ClaudeImportStatus.EXISTS, note: 'n' }],
      projects: [{ folder: '-w', path: '/w', sessionCount: 1, importedCount: 1, totalBytes: 5000n, newestAt: 'd', sessions: [{ id: 's', title: 't', sizeBytes: 5000n, updatedAt: 'd', status: ClaudeImportStatus.NEW, note: '' }] }]
    }))
    expect(v.items[0]).toEqual({ section: 'output-styles', kind: 'output-style', name: 'Terse', status: 'exists', note: 'n' })
    expect(v.projects[0]!.totalBytes).toBe(5000)
    expect(v.projects[0]!.sessions[0]!.status).toBe('new')
  })

  it('reads the report', () => {
    const r = importReportView(create(ClaudeImportReportSchema, { imported: 2, errors: 1, error: 'e', items: [{ section: ClaudeImportSection.SESSIONS, kind: 'session', name: 's', status: ClaudeImportStatus.ERROR, note: 'io' }] }))
    expect([r.imported, r.errors, r.error]).toEqual([2, 1, 'e'])
    expect(r.items[0]!.status).toBe('error')
    expect(r.items[0]!.section).toBe('sessions')
  })
})

describe('Claude Code mock', () => {
  it('scans the real case: 151 projects, 289 sessions, about 2.1 GB', async () => {
    const s = await new MockClaudeImportApi({ tick: 0 }).scan()
    expect(s.projects.length).toBe(151)
    expect(s.projects.reduce((a, p) => a + p.sessionCount, 0)).toBe(289)
    const gb = s.projects.reduce((a, p) => a + p.totalBytes, 0) / 1e9
    expect(gb).toBeGreaterThan(1.8)
    expect(gb).toBeLessThan(2.6)
  })

  it('reports progress, and marks what came on the next scan', async () => {
    const api = new MockClaudeImportApi({ tick: 0 })
    const { projects } = await api.scan()
    const paths = projects.slice(0, 3).map(p => p.path)
    const seen: { done: number, total: number }[] = []
    const report = await api.run({ sections: ['memory'], projectPaths: paths, includeSecrets: false, force: false }, { onProgress: p => seen.push(p) }).done
    expect(report!.imported).toBeGreaterThan(0)
    expect(seen.at(-1)).toMatchObject({ done: 7, total: 7 })
    const again = await api.scan()
    expect(again.items.find(i => i.kind === 'memory')!.status).toBe('exists')
    expect(newSessions(again.projects[0]!)).toEqual([])
    // Nothing new the second time.
    const second = await api.run({ sections: ['memory'], projectPaths: paths, includeSecrets: false, force: false }).done
    expect(second!.imported).toBe(0)
    expect(second!.exists).toBe(7)
  })

  it('cancels, and importing again goes on', async () => {
    const api = new MockClaudeImportApi({ tick: 0 })
    const { projects } = await api.scan()
    const paths = projects.map(p => p.path)
    const h = api.run({ sections: [], projectPaths: paths, includeSecrets: false, force: false }, {
      onProgress: (p) => {
        if (p.done === 3) h.cancel()
      }
    })
    expect(await h.done).toBeNull()
    const after = await api.scan()
    const imported = after.projects.reduce((a, p) => a + p.importedCount, 0)
    expect(imported).toBe(3)
    const report = await api.run({ sections: [], projectPaths: paths, includeSecrets: false, force: false }).done
    expect(report!.exists).toBe(3)
    // The session that fails once and the one put off are told apart.
    expect(report!.errors).toBe(1)
    expect(report!.queued).toBe(1)
    expect(report!.items.find(i => i.status === 'error')!.note).toMatch(/input\/output/)
  })

  it('says when there is no folder or no server', async () => {
    expect((await new MockClaudeImportApi({ found: false }).scan()).found).toBe(false)
    const off = new MockClaudeImportApi({ connected: false, tick: 0 })
    expect((await off.scan()).connected).toBe(false)
    await expect(off.run({ sections: ['memory'], projectPaths: [], includeSecrets: false, force: false }).done).rejects.toThrow(/server/)
  })

  it('brings imported sessions into the session list', async () => {
    const t = new MockTransport({ tick: 0 })
    const api = t.claudeImport()
    const { projects } = await api.scan()
    const p = projects[0]!
    const before = await t.listSessions()
    await api.run({ sections: [], projectPaths: [p.path], includeSecrets: false, force: false }).done
    const after = await t.listSessions()
    expect(after.length).toBe(before.length + p.sessionCount)
    expect(after.find(s => s.id === p.sessions[0]!.id)?.cwd).toBe(p.path)
    // Same one each time.
    expect(t.claudeImport()).toBe(api)
  })
})
