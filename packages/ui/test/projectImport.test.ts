import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { ClaudeImportStatus } from '~/gen/harness/agent/v1/import_pb'
import { ProjectImportItemSchema } from '~/gen/harness/agent/v1/project_import_pb'
import { MockProjectImportApi, comingOver, kindLabel, offerSummary, projectItemView, type ProjectImportItem } from '~/lib/import/project'

const item = (kind: string, status: ProjectImportItem['status'] = 'new'): ProjectImportItem => ({ kind, source: `s/${kind}`, target: `t/${kind}`, status, note: '', needsTrust: false })

describe('the offer of a project import', () => {
  it('lists only what would come over, by kind', () => {
    const items = [item('instructions'), item('skill'), item('skill'), item('agent', 'exists'), item('settings', 'skipped')]
    expect(comingOver(items).map(i => i.kind)).toEqual(['instructions', 'skill', 'skill'])
    expect(offerSummary(items)).toBe('1 instructions file, 2 skills')
    expect(kindLabel('output-style', 2)).toBe('output styles')
    expect(kindLabel('something-new')).toBe('something-new')
  })

  it('reads the statuses of the contract', () => {
    const view = (status: ClaudeImportStatus) => projectItemView(create(ProjectImportItemSchema, { kind: 'rule', status, needsTrust: true }))
    expect(view(ClaudeImportStatus.NEW).status).toBe('new')
    expect(view(ClaudeImportStatus.IMPORTED).status).toBe('imported')
    expect(view(ClaudeImportStatus.SKIPPED).status).toBe('skipped')
    expect(view(ClaudeImportStatus.ERROR)).toMatchObject({ status: 'error', needsTrust: true })
  })

  it('stops once dismissed, and comes back when asked', async () => {
    const api = new MockProjectImportApi(() => true)
    expect((await api.scan('/a')).offer).toBe(true)
    await api.dismiss('/a')
    expect(await api.scan('/a')).toMatchObject({ offer: false, dismissed: true })
    expect((await api.scan('/b')).offer).toBe(true)
    await api.dismiss('/a', false)
    expect((await api.scan('/a')).offer).toBe(true)
  })

  it('stops once imported, and a second run brings nothing', async () => {
    const api = new MockProjectImportApi(() => true)
    const first = await api.run('/a')
    expect(first.imported).toBeGreaterThan(0)
    expect((await api.scan('/a')).offer).toBe(false)
    const second = await api.run('/a')
    expect(second).toMatchObject({ imported: 0, exists: first.imported })
  })

  it('has nothing to offer for a folder without such files', async () => {
    const scan = await new MockProjectImportApi(() => false).scan('/a')
    expect(scan).toMatchObject({ offer: false, items: [] })
  })
})
