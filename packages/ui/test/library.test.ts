import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { TrackerTaskSchema } from '~/gen/harness/library/v1/tracker_pb'
import { MockLibraryApi } from '~/lib/library/mock'
import { ArtifactFilter, ArtifactKind, ArtifactVisibility, errorText, ExportFormat, formatSize, groupTasks, Scope, scopeLabel, statusLabel, timeAgo, TrackerPriority, TrackerStatus } from '~/lib/library/types'
import { MockTransport } from '~/lib/transport/mock'
import type { ToolItem } from '~/lib/session/types'
import { toolIcon, toolTarget } from '~/utils/tools'

const tool = (o: Partial<ToolItem>) => ({ id: 't', name: 'Bash', input: {}, status: 'ok', summary: '', resultSummary: '', durationMs: 0, startedAt: 0, ...o }) as ToolItem

describe('artifacts', () => {
  it('filters between what is yours and what others shared', async () => {
    const api = new MockLibraryApi()
    const mine = await api.listArtifacts({ filter: ArtifactFilter.MINE })
    const shared = await api.listArtifacts({ filter: ArtifactFilter.SHARED })
    expect(mine.every(a => a.mine)).toBe(true)
    expect(shared.every(a => !a.mine && a.ownerName)).toBe(true)
    expect((await api.listArtifacts({ filter: ArtifactFilter.ALL })).length).toBe(mine.length + shared.length)
    expect((await api.listArtifacts({ filter: ArtifactFilter.ALL, query: 'MIGRATION' })).map(a => a.title)).toEqual(['Migration notes'])
  })

  it('starts private, shares and can be taken back, by the owner only', async () => {
    const api = new MockLibraryApi()
    const [a] = await api.listArtifacts({ filter: ArtifactFilter.MINE })
    expect(a!.visibility).toBe(ArtifactVisibility.PRIVATE)
    expect((await api.setArtifactShared(a!.id, true)).visibility).toBe(ArtifactVisibility.SHARED)
    expect((await api.setArtifactShared(a!.id, false)).visibility).toBe(ArtifactVisibility.PRIVATE)
    const [other] = await api.listArtifacts({ filter: ArtifactFilter.SHARED })
    await expect(api.setArtifactShared(other!.id, false)).rejects.toThrow(/owner/)
  })

  it('gives text inline and the rest an address', async () => {
    const api = new MockLibraryApi()
    expect((await api.getArtifact('art_notes')).text).toContain('Migration notes')
    const page = await api.getArtifact('art_report')
    expect(page.artifact?.kind).toBe(ArtifactKind.PAGE)
    expect(page.text).toBe('')
    expect(await api.artifactUrl('art_report')).toMatch(/^data:text\/html/)
    await api.deleteArtifact('art_report')
    await expect(api.getArtifact('art_report')).rejects.toThrow(/no such/)
  })
})

describe('knowledge base', () => {
  it('searches and edits documents', async () => {
    const api = new MockLibraryApi()
    const hits = await api.searchKnowledge('gateway')
    expect(hits[0]?.title).toBe('Deployment runbook')
    expect(scopeLabel(hits[0]!.scope)).toBe('Everyone')
    const d = await api.createDocument({ scope: Scope.USER, title: ' Mine ', content: 'hello there', sourceName: 'a.txt' })
    expect(d.title).toBe('Mine')
    expect(scopeLabel(d.scope)).toBe('Only you')
    expect((await api.getDocument(d.id)).content).toBe('hello there')
    expect((await api.updateDocument(d.id, { content: 'changed' })).size).toBe(7n)
    expect((await api.listDocuments({ scope: Scope.ORG })).map(x => x.id)).toEqual(['kb_runbook'])
    await api.deleteDocument(d.id)
    await expect(api.createDocument({ scope: Scope.USER, title: '', content: 'x' })).rejects.toThrow(/title/)
  })
})

describe('task tracker', () => {
  it('moves tasks through their statuses', async () => {
    const api = new MockLibraryApi()
    const t = await api.createTask({ title: 'Do it', project: 'core' })
    expect(t.status).toBe(TrackerStatus.TODO)
    expect(t.priority).toBe(TrackerPriority.NORMAL)
    const done = await api.updateTask(t.id, { status: TrackerStatus.DONE })
    expect(done.closedAt).not.toBe('')
    expect(done.title).toBe('Do it')
    expect((await api.updateTask(t.id, { status: TrackerStatus.TODO })).closedAt).toBe('')
    await api.deleteTask(t.id)
    expect((await api.listTasks({ query: 'do it' })).length).toBe(0)
    await expect(api.createTask({ title: ' ' })).rejects.toThrow(/title/)
  })

  it('groups by status, keeping the order it was given', () => {
    const task = (id: string, status: TrackerStatus) => create(TrackerTaskSchema, { id, status })
    const groups = groupTasks([task('a', TrackerStatus.TODO), task('b', TrackerStatus.DONE), task('c', TrackerStatus.TODO)])
    expect(groups.map(g => g.label)).toEqual(['To do', 'In progress', 'Blocked', 'Done', 'Cancelled'])
    expect(groups[0]!.tasks.map(t => t.id)).toEqual(['a', 'c'])
    expect(groups[1]!.tasks).toEqual([])
    expect(statusLabel(TrackerStatus.BLOCKED)).toBe('Blocked')
  })
})

describe('memory', () => {
  it('keeps notes per project and rebuilds what the screen shows', async () => {
    const api = new MockLibraryApi()
    const [p] = await api.listMemory('/some/folder')
    expect(p!.current).toBe(true)
    const saved = await api.saveMemory(p!.projectKey, 'style', '---\nname: Style\ndescription: Keep it short\ntype: user\n---\n\nShort.\n')
    expect(saved.files.map(f => f.name)).toContain('style.md')
    expect(saved.index).toContain('Keep it short')
    await expect(api.saveMemory(p!.projectKey, '../up', 'x')).rejects.toThrow(/lowercase/)
    expect((await api.deleteMemory(p!.projectKey, 'style.md')).index).not.toContain('Keep it short')
  })

  it('has a switch in the preferences', async () => {
    const t = new MockTransport()
    expect((await t.getUserPreferences()).autoMemoryEnabled).toBe(true)
    expect((await t.updateUserPreferences({ autoMemoryEnabled: false })).autoMemoryEnabled).toBe(false)
    expect((await t.getUserPreferences()).autoMemoryEnabled).toBe(false)
  })
})

describe('export and search', () => {
  it('names the file by its format', async () => {
    const api = new MockLibraryApi()
    const md = await api.exportSession('s1', ExportFormat.MARKDOWN)
    expect(md.name).toMatch(/\.md$/)
    expect(new TextDecoder().decode(md.content)).toContain('## User')
    expect((await api.exportSession('s1', ExportFormat.JSONL)).mediaType).toBe('application/x-ndjson')
  })

  it('finds transcripts with a snippet', async () => {
    const api = new MockLibraryApi()
    expect(await api.searchTranscripts('  ')).toEqual([])
    expect((await api.searchTranscripts('kerberos'))[0]?.snippet).toContain('kerberos')
  })
})

describe('formatting', () => {
  it('reads sizes and ages', () => {
    expect(formatSize(12)).toBe('12 B')
    expect(formatSize(2048n)).toBe('2.0 KB')
    expect(formatSize(3 << 20)).toBe('3.0 MB')
    const now = Date.parse('2026-06-10T12:00:00Z')
    expect(timeAgo('2026-06-10T11:59:50Z', now)).toBe('just now')
    expect(timeAgo('2026-06-10T11:58:00Z', now)).toBe('2 min ago')
    expect(timeAgo('2026-06-09T11:00:00Z', now)).toBe('yesterday')
    expect(timeAgo('2026-06-05T11:00:00Z', now)).toBe('5 days ago')
    expect(timeAgo('nonsense', now)).toBe('')
    expect(errorText(new Error('[not_found] gone'))).toBe('gone')
  })

  it('names the library tools in the transcript', () => {
    expect(toolTarget(tool({ name: 'Memory', input: { command: 'write', name: 'style' } }))).toBe('write style')
    expect(toolTarget(tool({ name: 'KnowledgeSearch', input: { query: 'deploy' } }))).toBe('"deploy"')
    expect(toolTarget(tool({ name: 'ArtifactPublish', input: { title: 'Report' } }))).toBe('Report')
    expect(toolTarget(tool({ name: 'TaskTracker', input: { action: 'create', title: 'Do it' } }))).toBe('create Do it')
    expect(toolTarget(tool({ name: 'NotebookRun', input: { notebook_path: '/a.ipynb' } }))).toBe('/a.ipynb')
    expect(toolIcon(tool({ name: 'TaskTracker' }))).toBe('tasks')
  })
})
