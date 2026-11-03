import { describe, expect, it } from 'vitest'
import { create } from '@bufbuild/protobuf'
import { createRouterTransport } from '@connectrpc/connect'
import { AgentService } from '~/gen/harness/agent/v1/agent_pb'
import { FileEntrySchema, FileKind, GitFileStatus } from '~/gen/harness/agent/v1/files_pb'
import { ConnectFilesApi } from '~/lib/files/connect'
import { MockFilesApi } from '~/lib/files/mock'
import {
  absolutePath, ancestorsToExpand, buildRows, changeCounts, changedDirs, fileKind, filterPaths, fuzzyScore, relativePath,
  showTargetFile, statusOf, writtenPaths
} from '~/lib/files/tree'
import type { FileEntry, FileRoot } from '~/lib/files/types'
import { createSessionState, reduceAll } from '~/lib/session/reducer'
import { EventBuilder } from '~/lib/transport/events'
import { MockTransport } from '~/lib/transport/mock'

const ROOT = '/p/app'
const roots: FileRoot[] = [{ path: ROOT, name: 'app', gitRoot: ROOT }, { path: '/p/shared', name: 'shared', gitRoot: '' }]

function entry(path: string, kind: FileEntry['kind'] = 'file', extra: Partial<FileEntry> = {}): FileEntry {
  return { name: path.slice(path.lastIndexOf('/') + 1), path, kind, symlink: false, size: 1, modifiedAt: '', git: '', ignored: false, deleted: false, ...extra }
}

describe('files tree', () => {
  const listings = {
    [ROOT]: [entry(`${ROOT}/src`, 'dir'), entry(`${ROOT}/docs`, 'dir'), entry(`${ROOT}/README.md`)],
    [`${ROOT}/src`]: [entry(`${ROOT}/src/lib`, 'dir'), entry(`${ROOT}/src/main.go`)],
    '/p/shared': [entry('/p/shared/notes.txt')]
  }
  const status = { [`${ROOT}/src/lib/deep.go`]: 'M', [`${ROOT}/README.md`]: 'A', [`${ROOT}/scratch`]: 'U' } as const

  it('flattens expanded folders depth first, roots on top', () => {
    const rows = buildRows({ roots, listings, expanded: new Set([ROOT, `${ROOT}/src`]), status: {} })
    expect(rows.map(r => [r.name, r.depth])).toEqual([
      ['app', 0], ['src', 1], ['lib', 2], ['main.go', 2], ['docs', 1], ['README.md', 1], ['shared', 0]
    ])
    expect(rows[0]!.root && rows[0]!.expanded).toBe(true)
    // Expanded but not listed yet: loading.
    const loading = buildRows({ roots, listings, expanded: new Set([ROOT, `${ROOT}/docs`]), status: {} })
    expect(loading.find(r => r.name === 'docs')!.loading).toBe(true)
  })

  it('aggregates git status onto the folders that contain changes', () => {
    const dirs = changedDirs({ ...status }, roots.map(r => r.path))
    expect([...dirs].sort()).toEqual([ROOT, `${ROOT}/scratch`, `${ROOT}/src`, `${ROOT}/src/lib`])
    const rows = buildRows({ roots, listings, expanded: new Set([ROOT, `${ROOT}/src`, '/p/shared']), status: { ...status } })
    const by = (n: string) => rows.find(r => r.name === n)!
    expect(by('app').changed).toBe(true)
    expect(by('src').changed && by('lib').changed).toBe(true)
    expect(by('docs').changed).toBe(false)
    expect(by('shared').changed).toBe(false)
    expect(by('README.md').git).toBe('A')
    expect(by('main.go').git).toBe('')
    expect(changeCounts(ROOT, { ...status })).toEqual({ M: 1, A: 1, U: 1 })
  })

  it('takes letters from the status map, untracked folders included, the listing as a fallback', () => {
    expect(statusOf(`${ROOT}/scratch/a/b.txt`, { ...status })).toBe('U')
    expect(statusOf(`${ROOT}/src/main.go`, { ...status })).toBe('')
    const rows = buildRows({
      roots, listings: { [ROOT]: [entry(`${ROOT}/x.go`, 'file', { git: 'M' })] }, expanded: new Set([ROOT]), status: {}
    })
    expect(rows[1]!.git).toBe('M')
  })

  it('expands the ancestors of a file to reveal it', () => {
    expect(ancestorsToExpand(`${ROOT}/src/lib/deep.go`, roots)).toEqual([ROOT, `${ROOT}/src`, `${ROOT}/src/lib`])
    expect(ancestorsToExpand('/elsewhere/x', roots)).toEqual([])
  })

  it('tells file kinds and resolves paths', () => {
    expect(['README.md', 'a.go', 'logo.png', 'icon.svg', 'LICENSE', 'Makefile'].map(p => fileKind(p))).toEqual(['md', 'code', 'image', 'svg', 'file', 'code'])
    expect(fileKind('src', 'dir')).toBe('dir')
    expect(absolutePath('src/../docs/a.md', ROOT)).toBe(`${ROOT}/docs/a.md`)
    expect(absolutePath('./b.ts', `${ROOT}/`)).toBe(`${ROOT}/b.ts`)
    expect(absolutePath('/abs/x', ROOT)).toBe('/abs/x')
    expect(relativePath(`${ROOT}/src/main.go`, roots)).toBe('src/main.go')
    expect(relativePath('/p/shared/notes.txt', roots)).toBe('notes.txt')
    expect(relativePath(ROOT, roots)).toBe('app')
  })

  it('notices file-writing tools', () => {
    expect(writtenPaths({ name: 'Write', input: { file_path: '/p/a.ts' } })).toEqual(['/p/a.ts'])
    expect(writtenPaths({ name: 'Edit', input: { file_path: '/p/a.ts' } })).toEqual(['/p/a.ts'])
    expect(writtenPaths({ name: 'Read', input: { file_path: '/p/a.ts' } })).toEqual([])
  })
})

describe('files filter', () => {
  it('matches fuzzily by path, file names first', () => {
    expect(fuzzyScore('rdm', 'README.md')).not.toBeNull()
    expect(fuzzyScore('xyz', 'README.md')).toBeNull()
    expect(fuzzyScore('main', 'src/main.go')!).toBeGreaterThan(fuzzyScore('main', 'domain/x/a.go')!)
  })

  it('ranks and deduplicates candidates relative to their roots', () => {
    const hits = filterPaths('main', [
      { path: `${ROOT}/src/main.go` },
      { path: `${ROOT}/src/main.go` },
      { path: `${ROOT}/cmd/maintenance/run.go` },
      { path: `${ROOT}/docs/`, dir: true },
      { path: '/p/shared/main.txt' }
    ], roots)
    expect(hits.map(h => h.rel)).toEqual(['main.txt', 'src/main.go', 'cmd/maintenance/run.go'])
    expect(hits.every(h => !h.dir)).toBe(true)
    expect(filterPaths('docs', [{ path: `${ROOT}/docs/` }], roots)[0]).toMatchObject({ path: `${ROOT}/docs`, dir: true })
  })
})

describe('MockFilesApi', () => {
  const api = new MockFilesApi({ sessionCwd: id => (id === 's1' ? '/u/proj' : undefined) })

  it('lists directories first, hides ignored entries unless asked, and lists deleted files', async () => {
    const top = await api.list('s1', '')
    const names = top.entries.map(e => e.name)
    expect(names.slice(0, 5)).toEqual(['assets', 'bin', 'data', 'docs', 'scratch'])
    expect(names).not.toContain('node_modules')
    expect(names).not.toContain('.git')
    expect(names).not.toContain('dist')
    expect(top.entries.find(e => e.name === 'README.md')!.git).toBe('M')
    const all = await api.list('s1', '/u/proj', { showIgnored: true })
    expect(all.entries.filter(e => e.ignored).map(e => e.name).sort()).toEqual(['.git', 'debug.log', 'dist', 'node_modules'])
    const server = await api.list('s1', 'src/server')
    expect(server.entries.find(e => e.name === 'legacy.go')).toMatchObject({ deleted: true, git: 'D' })
  })

  it('reads text, markdown, images and binary files within the limits, with etags', async () => {
    const md = await api.read('s1', 'README.md')
    expect(md.mime).toBe('text/markdown')
    expect(md.text.startsWith('# Harness')).toBe(true)
    expect((await api.read('s1', 'README.md', { ifNoneMatch: md.etag })).notModified).toBe(true)
    const png = await api.read('s1', 'assets/pixel.png')
    expect(png.data?.[1]).toBe(0x50) // "P" of the PNG signature
    const bin = await api.read('s1', 'bin/harness-client')
    expect(bin.binary && !bin.data && bin.text === '').toBe(true)
    expect((await api.read('s1', 'README.md', { maxBytes: 10 })).truncated).toBe(true)
  })

  it('refuses paths outside the working directory', async () => {
    await expect(api.read('s1', '../other/secret')).rejects.toThrow(/permission_denied/)
    await expect(api.list('s1', '/etc')).rejects.toThrow(/permission_denied/)
    await expect(api.read('s1', 'nope.txt')).rejects.toThrow(/not_found/)
    await expect(api.roots('missing')).rejects.toThrow(/not_found/)
  })

  it('sees writes: a new etag and a git status', async () => {
    const before = await api.read('s1', 'notes.txt')
    api.write('s1', 'src/new.ts', 'export {}\n')
    api.write('s1', 'notes.txt', 'changed\n')
    const after = await api.read('s1', 'notes.txt', { ifNoneMatch: before.etag })
    expect(after.notModified).toBe(false)
    expect(after.text).toBe('changed\n')
    const st = await api.gitStatus('s1')
    expect(st.files['/u/proj/src/new.ts']).toBe('U')
    expect(await api.search('s1', 'newts', 5)).toContain('src/new.ts')
  })

  it('is served by the mock transport for its sessions', async () => {
    const t = new MockTransport()
    const [s] = await t.listSessions()
    const rs = await t.files().roots(s!.id)
    expect(rs[0]!.path).toBe(s!.cwd)
    expect((await t.files().list(s!.id, '')).entries.length).toBeGreaterThan(3)
  })
})

describe('Show in the files pane', () => {
  it('lands on the file and its lines', async () => {
    const b = new EventBuilder('s1', Date.parse('2026-11-03T12:00:00Z'))
    const s = createSessionState('s1')
    b.show('t1', { file: 'src/server/routes.ts', lines: [40, 44] })
    reduceAll(s, b.events)
    const target = s.showRequest!.target
    expect(target.kind).toBe('file')
    if (target.kind !== 'file') return
    const { path, lines } = showTargetFile(target, '/u/proj')
    expect(path).toBe('/u/proj/src/server/routes.ts')
    expect(lines).toEqual({ start: 40, end: 44 })
    // The tree opens down to it and the file has those lines.
    const projRoots = [{ path: '/u/proj', name: 'proj', gitRoot: '/u/proj' }]
    expect(ancestorsToExpand(path, projRoots)).toEqual(['/u/proj', '/u/proj/src', '/u/proj/src/server'])
    const api = new MockFilesApi({ sessionCwd: () => '/u/proj' })
    const c = await api.read('s1', path)
    expect(c.text.split('\n').length).toBeGreaterThan(lines!.end)
    // Without a range: no lines; a single line: a one-line range.
    expect(showTargetFile({ path: '/abs/a.go', lineStart: 0, lineEnd: 0 }, '/u/proj').lines).toBeNull()
    expect(showTargetFile({ path: 'a.go', lineStart: 7, lineEnd: 0 }, '/u/proj').lines).toEqual({ start: 7, end: 7 })
  })
})

describe('ConnectFilesApi', () => {
  it('maps the RPCs to the pane view', async () => {
    const transport = createRouterTransport(({ service }) => {
      service(AgentService, {
        listFileRoots: () => ({ roots: [{ path: '/p', name: 'p', gitRoot: '/p' }] }),
        listFiles: req => ({
          path: req.path || '/p',
          entries: [create(FileEntrySchema, { name: 'a.go', path: '/p/a.go', kind: FileKind.FILE, size: 12n, gitStatus: GitFileStatus.MODIFIED, ignored: req.showIgnored })],
          truncated: false
        }),
        readFile: req => ({ path: req.path, size: 3n, etag: 'e1', mime: 'text/plain', text: 'abc', notModified: req.ifNoneMatch === 'e1' }),
        getGitStatus: () => ({ root: '/p', files: { '/p/a.go': GitFileStatus.MODIFIED, '/p/new': GitFileStatus.UNTRACKED } }),
        suggestFiles: req => ({ paths: [`hit-${req.query}`] })
      })
    })
    const api = new ConnectFilesApi(transport)
    expect(await api.roots('s')).toEqual([{ path: '/p', name: 'p', gitRoot: '/p' }])
    const l = await api.list('s', '', { showIgnored: true })
    expect(l.entries[0]).toMatchObject({ name: 'a.go', kind: 'file', size: 12, git: 'M', ignored: true })
    const r = await api.read('s', '/p/a.go')
    expect(r).toMatchObject({ text: 'abc', size: 3, binary: false, notModified: false })
    expect(r.data).toBeUndefined()
    expect((await api.read('s', '/p/a.go', { ifNoneMatch: 'e1' })).notModified).toBe(true)
    expect((await api.gitStatus('s')).files).toEqual({ '/p/a.go': 'M', '/p/new': 'U' })
    expect(await api.search('s', 'x', 5)).toEqual(['hit-x'])
  })
})
