import { describe, expect, it } from 'vitest'
import { create } from '@bufbuild/protobuf'
import { CardOutputSchema, CardSchema } from '~/gen/harness/library/v1/boards_pb'
import { MockIssuesApi } from '~/lib/issues/mock'
import { BoardScope, cardsByStage, CardOutputStatus, CardPriority, CardRelationKind, ExternalState, externalNote, outputNote, priorityLabel, ProjectScope, stagesToInit, TrackerAuthMethod, TrackerConnectionScope } from '~/lib/issues/types'
import { MockTransport } from '~/lib/transport/mock'
import { toolIcon, toolTarget } from '~/utils/tools'
import type { ToolItem } from '~/lib/session/types'

const tool = (o: Partial<ToolItem>) => ({ id: 't', name: 'Bash', input: {}, status: 'ok', summary: '', resultSummary: '', durationMs: 0, startedAt: 0, ...o }) as ToolItem

describe('issues', () => {
  it('moves between stages, applying the output of the stage it enters', async () => {
    const api = new MockIssuesApi()
    const { board, cards } = await api.getBoard('brd_flow')
    const linked = cards.find(c => c.external)!
    const moved = await api.moveCard(linked.id, { stageId: 'stg_done', expectedVersion: linked.version })
    expect(moved.stageId).toBe('stg_done')
    expect(moved.output?.status).toBe(CardOutputStatus.DONE)
    expect(board.stages.map(s => s.name)).toEqual(['Queue', 'In progress', 'Done'])
    const other = cards.find(c => !c.external)!
    expect((await api.moveCard(other.id, { stageId: 'stg_done' })).output).toBeUndefined()
  })

  it('refuses a stale move, so the first of two wins', async () => {
    const api = new MockIssuesApi()
    const { cards } = await api.getBoard('brd_flow')
    const c = cards[0]!
    const seen = c.version
    await api.moveCard(c.id, { stageId: 'stg_progress', expectedVersion: seen })
    await expect(api.moveCard(c.id, { stageId: 'stg_done', expectedVersion: seen })).rejects.toThrow(/first/)
  })

  it('orders within a stage', async () => {
    const api = new MockIssuesApi()
    const a = await api.createCard({ boardId: 'brd_flow', stageId: 'stg_progress', title: 'A' })
    const b = await api.createCard({ boardId: 'brd_flow', stageId: 'stg_progress', title: 'B' })
    await api.moveCard(b.id, { stageId: 'stg_progress', beforeCardId: a.id })
    const { board, cards } = await api.getBoard('brd_flow')
    const titles = cardsByStage(board.stages, cards).stg_progress!.map(c => c.title)
    expect(titles.indexOf('B')).toBeLessThan(titles.indexOf('A'))
  })

  it('filters and keeps views', async () => {
    const api = new MockIssuesApi()
    expect((await api.listCards({ projectId: 'prj_core', priorities: [CardPriority.HIGH] })).map(c => c.key)).toEqual(['DEMO-7'])
    expect((await api.listCards({ query: 'upload' })).map(c => c.key)).toEqual(['CORE-2'])
    expect((await api.listCards({ query: 'demo-7' })).map(c => c.key)).toEqual(['DEMO-7'])
    expect(await api.listCards({ labels: ['security', 'nope'] })).toEqual([])
    const v = await api.saveView('High', { priorities: [CardPriority.HIGH] })
    expect((await api.listViews()).map(x => x.name)).toEqual(['High'])
    await api.deleteView(v.id)
    expect(await api.listViews()).toEqual([])
  })

  it('checks a ticker and keeps provider values under local ones', async () => {
    const api = new MockIssuesApi()
    await expect(api.createProject({ scope: ProjectScope.PERSONAL, metadata: { ticker: 'no', name: 'x' } })).rejects.toThrow(/ticker/)
    await expect(api.createProject({ scope: ProjectScope.PERSONAL, metadata: { ticker: 'CORE', name: 'x' } })).rejects.toThrow(/taken/)
    const [prov] = await api.listProviderProjects()
    const p = await api.createProject({ scope: ProjectScope.PERSONAL, metadata: {}, connection: { scope: TrackerConnectionScope.ADMIN, id: 'tc_company' }, externalKey: prov!.key, provider: prov!.metadata })
    expect(p.metadata?.name).toBe('Demo project')
    expect((await api.updateProject(p.id, { name: 'Mine' })).metadata?.name).toBe('Mine')
    expect((await api.updateProject(p.id, { name: '' })).metadata?.name).toBe('Demo project')
  })

  it('relates, comments and starts a session from the linked folder', async () => {
    const api = new MockIssuesApi()
    const r = await api.relate('crd_1', CardRelationKind.BLOCKED_BY, 'CORE-2')
    expect(r.relations.map(x => x.key)).toEqual(['CORE-2'])
    expect((await api.relate('crd_1', CardRelationKind.BLOCKED_BY, 'CORE-2', true)).relations).toEqual([])
    await api.comment('crd_1', 'On it')
    expect((await api.getCard('crd_1')).comments.map(c => c.text)).toEqual(['On it'])
    expect((await api.prepareSession('crd_1')).folder).toBe('')
    await api.setFolderLink('/repo', 'prj_core')
    const s = await api.prepareSession('crd_1')
    expect(s.folder).toBe('/repo')
    expect(s.branch).toBe('issue/demo-7')
    expect((await api.linkSession('crd_1', 's9', s.branch)).sessionIds).toContain('s9')
    await api.setFolderLink('/repo', '')
    expect(await api.listFolderLinks()).toEqual([])
  })

  it('keeps stage ids when a form saves them', async () => {
    const api = new MockIssuesApi()
    const { board } = await api.getBoard('brd_flow')
    const init = stagesToInit(board.stages)
    expect(init[0]!.inputs![0]!.query).toBe('Stage: Backlog')
    const up = await api.updateBoard(board.id, 'New', [init[1]!, init[2]!, { name: 'Extra' }])
    expect(up.stages.map(s => s.name)).toEqual(['In progress', 'Done', 'Extra'])
    expect(up.stages[0]!.id).toBe('stg_progress')
    // A stage that went took its cards to the first one.
    expect((await api.getBoard(board.id)).cards.every(c => up.stages.some(s => s.id === c.stageId))).toBe(true)
  })

  it('connects, and never shows a secret again', async () => {
    const api = new MockIssuesApi()
    const c = await api.saveConnection(TrackerConnectionScope.DEVICE, { provider: 'youtrack', baseUrl: 'https://yt.vpn.test', auth: TrackerAuthMethod.TOKEN, secret: 'perm:x', enabled: true })
    expect(c.hasSecret).toBe(true)
    expect(JSON.stringify(c, (_, v) => (typeof v === 'bigint' ? v.toString() : v))).not.toContain('perm:x')
    await expect(api.saveConnection(TrackerConnectionScope.DEVICE, { baseUrl: ' ' })).rejects.toThrow(/address/)
    const { connections, providers } = await api.listConnections()
    expect(connections.some(x => x.scope === TrackerConnectionScope.DEVICE)).toBe(true)
    expect(providers[0]?.authMethods).toContain(TrackerAuthMethod.OAUTH)
    await api.deleteConnection(TrackerConnectionScope.DEVICE, c.id)
    expect((await api.listConnections()).connections.some(x => x.id === c.id)).toBe(false)
  })

  it('makes a board with the default stages', async () => {
    const api = new MockIssuesApi()
    const b = await api.createBoard({ projectId: 'prj_core', scope: BoardScope.PERSONAL, name: 'Mine' })
    expect(b.stages.map(s => s.name)).toEqual(['To do', 'In progress', 'Done'])
  })
})

describe('what a card says', () => {
  const card = (o: object) => create(CardSchema, o)
  it('shows a failure and a wait, and stays quiet when done', () => {
    const output = (status: CardOutputStatus, error = '', scope = TrackerConnectionScope.ADMIN) => create(CardOutputSchema, { status, error, connection: { scope, id: 'x' } })
    expect(outputNote(card({ output: output(CardOutputStatus.FAILED, 'Denied') }))).toEqual({ text: 'Output failed: Denied', bad: true })
    expect(outputNote(card({ output: output(CardOutputStatus.PENDING, 'Down') })).bad).toBe(true)
    expect(outputNote(card({ output: output(CardOutputStatus.PENDING, '', TrackerConnectionScope.DEVICE) })).text).toMatch(/device/)
    expect(outputNote(card({ output: output(CardOutputStatus.DONE) })).text).toBe('')
    expect(outputNote(card({})).text).toBe('')
  })

  it('says when the link is broken', () => {
    expect(externalNote(card({ external: { state: ExternalState.MISSING } }))).toMatch(/gone/)
    expect(externalNote(card({ external: { state: ExternalState.DISCONNECTED } }))).toMatch(/removed/)
    expect(externalNote(card({ external: { state: ExternalState.LINKED } }))).toBe('')
    expect(priorityLabel(CardPriority.URGENT)).toBe('Urgent')
  })

  it('names the tool in a transcript', () => {
    expect(toolIcon(tool({ name: 'Issues' }))).toBe('tasks')
    expect(toolTarget(tool({ name: 'Issues', input: { operation: 'move', id: 'ABC-1' } }))).toBe('move ABC-1')
  })

  it('reaches the mock through the transport', async () => {
    const api = new MockTransport().issues()
    expect((await api.listProjects()).length).toBeGreaterThan(0)
  })
})
