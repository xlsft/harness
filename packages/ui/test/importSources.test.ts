import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { ClaudeImportStatus } from '~/gen/harness/agent/v1/import_pb'
import { ImportSourceReportSchema, ScanImportSourceResponseSchema } from '~/gen/harness/agent/v1/import_sources_pb'
import { chosenIds, defaultChoice, secretsMatter, sourceReportView, sourceScanView, sourceSections, toggleSection, type SourceItem } from '~/lib/import/sources'
import { MOCK_IMPORT_SOURCES, MockImportSourcesApi } from '~/lib/import/sourcesMock'

function item(id: string, section: string, status: SourceItem['status'], hasSecrets = false): SourceItem {
  return { id, section, kind: section === 'instructions' ? 'rule' : section, name: id, source: `${id}.md`, target: '', status, note: '', needsTrust: false, hasSecrets }
}

describe('import sources', () => {
  it('lists the Claude Code imports with their own screens, then the engine sources, Any folder last', () => {
    expect(MOCK_IMPORT_SOURCES.slice(0, 2).map(s => s.screen)).toEqual(['claude-code', 'claude-project'])
    expect(MOCK_IMPORT_SOURCES.at(-1)!.id).toBe('folder')
    expect(MOCK_IMPORT_SOURCES.filter(s => s.screen === '').every(s => s.userLevel || s.folderLevel)).toBe(true)
  })

  it('groups items in a fixed order and picks what is new', () => {
    const items = [item('a', 'mcp', 'new', true), item('b', 'instructions', 'exists'), item('c', 'instructions', 'new'), item('d', 'odd', 'manual')]
    expect(sourceSections(items).map(s => s.key)).toEqual(['instructions', 'mcp', 'odd'])
    const picked = defaultChoice(items)
    expect([...picked]).toEqual(['a', 'c'])
    expect(chosenIds(items, picked, false)).toEqual(['a', 'c'])
    expect(secretsMatter(items, picked)).toBe(true)
    expect(secretsMatter(items, new Set(['c']))).toBe(false)
    // An existing item can be picked only with force; a manual one never.
    const all = new Set(['a', 'b', 'c', 'd'])
    expect(chosenIds(items, all, false)).toEqual(['a', 'c'])
    expect(chosenIds(items, all, true)).toEqual(['a', 'b', 'c'])
  })

  it('toggles a section as a whole', () => {
    const items = [item('b', 'instructions', 'exists'), item('c', 'instructions', 'new')]
    const [s] = sourceSections(items)
    const on = toggleSection(s!, new Set(), false)
    expect([...on]).toEqual(['c'])
    expect([...toggleSection(s!, on, false)]).toEqual([])
    expect([...toggleSection(s!, new Set(), true)].sort()).toEqual(['b', 'c'])
  })

  it('maps the client answer', () => {
    const scan = sourceScanView(create(ScanImportSourceResponseSchema, {
      sourceId: 'claude-desktop',
      found: true,
      items: [{ id: 'mcp:x#fs', section: 'mcp', kind: 'mcp', name: 'fs', status: ClaudeImportStatus.NEW, hasSecrets: true }]
    }))
    expect(scan.items[0]).toMatchObject({ id: 'mcp:x#fs', status: 'new', hasSecrets: true })
    const rep = sourceReportView(create(ImportSourceReportSchema, { imported: 1, items: [{ id: 'mcp:x#fs', status: ClaudeImportStatus.IMPORTED }] }))
    expect(rep.imported).toBe(1)
    expect(rep.items[0]!.status).toBe('imported')
  })

  it('mock: a folder source asks for a folder, and a run marks what it brought', async () => {
    const api = new MockImportSourcesApi({ tick: 0 })
    await expect(api.scan('kiro')).rejects.toThrow(/folder/)
    const scan = await api.scan('claude-desktop')
    const ids = [...defaultChoice(scan.items)]
    const progress: number[] = []
    const report = await api.run({ sourceId: 'claude-desktop', itemIds: ids, includeSecrets: false, force: false }, { onProgress: p => progress.push(p.done) }).done
    expect(report!.imported).toBe(ids.length)
    expect(progress.at(-1)).toBe(ids.length)
    const again = await api.scan('claude-desktop')
    expect(again.items.every(i => i.status === 'exists')).toBe(true)
  })
})
