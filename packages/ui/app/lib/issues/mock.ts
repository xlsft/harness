// A scripted IssuesApi for the mock transport and tests: one project with a board like the owner's example, kept in memory.

import { create } from '@bufbuild/protobuf'
import { BoardSchema, BoardStageSchema, CardCommentSchema, CardOutputSchema, CardSchema, ExternalLinkSchema, SavedViewSchema, StageInputSchema, StageOutputSchema } from '~/gen/harness/library/v1/boards_pb'
import { ProjectMemberSchema, ProjectMetadataSchema, ProjectSchema, ProviderProjectSchema } from '~/gen/harness/library/v1/projects_pb'
import { TrackerCapabilitiesSchema, TrackerConnectionSchema, TrackerFieldSchema, TrackerIssueSchema } from '~/gen/harness/library/v1/tracker_connections_pb'
import { FolderProjectSchema } from '~/gen/harness/agent/v1/trackers_pb'
import {
  BoardScope,
  CardOutputStatus,
  CardPriority,
  ExternalState,
  ProjectLevel,
  ProjectScope,
  TrackerAuthMethod,
  TrackerConnectionScope,
  type Board,
  type Card,
  type CardComment,
  type CardFilterInit,
  type CardPatch,
  type CardRelationKind,
  type ConnectionInputInit,
  type FolderProject,
  type IssuesApi,
  type MetadataInit,
  type Project,
  type SavedView,
  type ShareInit,
  type StageInit,
  type TrackerConnection
} from './types'

const iso = (minAgo: number) => new Date(Date.now() - minAgo * 60_000).toISOString()
const ADMIN_REF = { scope: TrackerConnectionScope.ADMIN, id: 'tc_company' }

export class MockIssuesApi implements IssuesApi {
  private seq = 100
  private projects: Project[] = [
    create(ProjectSchema, { id: 'prj_core', scope: ProjectScope.GLOBAL, level: ProjectLevel.MANAGE, metadata: { ticker: 'CORE', name: 'Core', description: 'The server', color: '#5aa3ff' }, local: { ticker: 'CORE', name: 'Core', description: 'The server', color: '#5aa3ff' }, provider: {}, createdAt: iso(9000), updatedAt: iso(9000) })
  ]

  private boards: Board[] = [
    create(BoardSchema, {
      id: 'brd_flow',
      projectId: 'prj_core',
      scope: BoardScope.SHARED,
      name: 'Flow',
      canConfigure: true,
      canEdit: true,
      stages: [
        create(BoardStageSchema, { id: 'stg_queue', name: 'Queue', inputs: [create(StageInputSchema, { id: 'sin_1', connection: ADMIN_REF, query: 'Stage: Backlog', intervalSeconds: 300, lastPolledAt: iso(2), lastOkAt: iso(2) })] }),
        create(BoardStageSchema, { id: 'stg_progress', name: 'In progress' }),
        create(BoardStageSchema, { id: 'stg_done', name: 'Done', outputs: [create(StageOutputSchema, { id: 'sou_1', connection: ADMIN_REF, fields: [{ name: 'Stage', value: 'Review' }] })] })
      ]
    })
  ]

  private cards: Card[] = [
    create(CardSchema, { id: 'crd_1', key: 'DEMO-7', projectId: 'prj_core', boardId: 'brd_flow', stageId: 'stg_queue', title: 'Rotate the signing keys', version: 1, priority: CardPriority.HIGH, labels: ['security'], external: create(ExternalLinkSchema, { connection: ADMIN_REF, externalId: 'DEMO-7', url: 'https://yt.example.test/issue/DEMO-7', state: ExternalState.LINKED }), createdAt: iso(600), updatedAt: iso(600) }),
    create(CardSchema, { id: 'crd_2', key: 'CORE-2', projectId: 'prj_core', boardId: 'brd_flow', stageId: 'stg_progress', title: 'Finish the upload retry', version: 1, priority: CardPriority.NORMAL, sessionIds: ['s1'], createdAt: iso(120), updatedAt: iso(30) })
  ]

  private comments: Record<string, CardComment[]> = {}
  private views: SavedView[] = []
  private links: FolderProject[] = []
  private connections: TrackerConnection[] = [
    create(TrackerConnectionSchema, { id: 'tc_company', scope: TrackerConnectionScope.ADMIN, provider: 'youtrack', name: 'Company YouTrack', baseUrl: 'https://yt.example.test', auth: TrackerAuthMethod.TOKEN, hasSecret: true, enabled: true, healthy: true, knowledgeEnabled: true })
  ]

  private providers = [create(TrackerCapabilitiesSchema, { provider: 'youtrack', displayName: 'YouTrack', authMethods: [TrackerAuthMethod.TOKEN, TrackerAuthMethod.OAUTH, TrackerAuthMethod.PASSWORD], queryLanguage: 'YouTrack search', queryExample: 'project: DEMO Stage: Backlog', customFields: true, comments: true, knowledge: true })]

  private id(prefix: string) {
    return `${prefix}_${++this.seq}`
  }

  private card(id: string): Card {
    const c = this.cards.find(x => x.id === id || x.key === id)
    if (!c) throw new Error(`no such issue: ${id}`)
    return c
  }

  private project(id: string): Project {
    const p = this.projects.find(x => x.id === id)
    if (!p) throw new Error(`no such project: ${id}`)
    return p
  }

  async listProjects() {
    return [...this.projects]
  }

  async ensureDefaultProject() {
    return this.projects[0]!
  }

  async createProject(d: { scope: ProjectScope, metadata: MetadataInit, connection?: { scope: TrackerConnectionScope, id: string }, externalKey?: string, provider?: MetadataInit }) {
    const metadata = { ...d.provider, ...Object.fromEntries(Object.entries(d.metadata).filter(([, v]) => v)) }
    if (!metadata.ticker || !/^[A-Z][A-Z0-9]{1,9}$/.test(metadata.ticker)) throw new Error('A ticker is 2 to 10 upper case letters and digits, starting with a letter')
    if (!metadata.name) throw new Error('A project needs a name')
    if (this.projects.some(p => p.metadata?.ticker === metadata.ticker)) throw new Error(`The ticker ${metadata.ticker} is already taken`)
    const p = create(ProjectSchema, { id: this.id('prj'), scope: d.scope, level: ProjectLevel.MANAGE, metadata: create(ProjectMetadataSchema, metadata), local: create(ProjectMetadataSchema, d.metadata), provider: create(ProjectMetadataSchema, d.provider ?? {}), connection: d.connection, externalKey: d.externalKey ?? '', createdAt: iso(0), updatedAt: iso(0) })
    this.projects.push(p)
    return p
  }

  async updateProject(id: string, patch: MetadataInit) {
    const p = this.project(id)
    const { ticker, name, description, logo, color } = p.local ?? create(ProjectMetadataSchema)
    p.local = create(ProjectMetadataSchema, { ticker, name, description, logo, color, ...patch })
    const merged: MetadataInit = { ...p.provider }
    for (const [k, v] of Object.entries(p.local)) if (v && !k.startsWith('$')) (merged as Record<string, unknown>)[k] = v
    p.metadata = create(ProjectMetadataSchema, merged)
    return p
  }

  async deleteProject(id: string) {
    this.projects = this.projects.filter(p => p.id !== id)
    this.boards = this.boards.filter(b => b.projectId !== id)
    this.cards = this.cards.filter(c => c.projectId !== id)
  }

  async setShares(id: string, shares: ShareInit[]) {
    const p = this.project(id)
    p.shares = shares.map(s => ({ ...s, subjectName: s.subjectId || 'Everyone', $typeName: 'harness.library.v1.ProjectShare' as const }))
    return p
  }

  async listMembers() {
    return [create(ProjectMemberSchema, { userId: 'u1', name: 'You', email: 'you@example.test', level: ProjectLevel.MANAGE })]
  }

  async listProviderProjects() {
    return [create(ProviderProjectSchema, { key: 'DEMO', metadata: { ticker: 'DEMO', name: 'Demo project', description: 'From the provider' } })]
  }

  async listBoards(projectId: string) {
    return this.boards.filter(b => b.projectId === projectId)
  }

  async getBoard(id: string) {
    const board = this.boards.find(b => b.id === id)
    if (!board) throw new Error('no such board')
    return { board, cards: this.cards.filter(c => c.boardId === id) }
  }

  async createBoard(d: { projectId: string, scope: BoardScope, name: string }) {
    const b = create(BoardSchema, { id: this.id('brd'), projectId: d.projectId, scope: d.scope, name: d.name, canConfigure: true, canEdit: true, stages: ['To do', 'In progress', 'Done'].map(name => create(BoardStageSchema, { id: this.id('stg'), name })) })
    this.boards.push(b)
    return b
  }

  async updateBoard(id: string, name: string, stages: StageInit[]) {
    const b = this.boards.find(x => x.id === id)
    if (!b) throw new Error('no such board')
    b.name = name
    b.stages = stages.map(s => create(BoardStageSchema, {
      id: s.id || this.id('stg'),
      name: s.name,
      inputs: (s.inputs ?? []).map(i => create(StageInputSchema, { id: i.id || this.id('sin'), connection: i.connection, query: i.query, intervalSeconds: i.intervalSeconds ?? 300 })),
      outputs: (s.outputs ?? []).map(o => create(StageOutputSchema, { id: o.id || this.id('sou'), connection: o.connection, fields: o.fields, comment: o.comment ?? '' }))
    }))
    const first = b.stages[0]!.id
    for (const c of this.cards) if (c.boardId === id && !b.stages.some(s => s.id === c.stageId)) c.stageId = first
    return b
  }

  async deleteBoard(id: string) {
    this.boards = this.boards.filter(b => b.id !== id)
    this.cards = this.cards.filter(c => c.boardId !== id)
  }

  async syncBoard() {
    return 0
  }

  async listCards(f: CardFilterInit) {
    const q = f.query?.trim().toLowerCase()
    return this.cards.filter(c =>
      (!f.projectId || c.projectId === f.projectId)
      && (!f.boardId || c.boardId === f.boardId)
      && (!q || c.title.toLowerCase().includes(q) || c.key.toLowerCase() === q)
      && (!f.priorities?.length || f.priorities.includes(c.priority))
      && (f.labels ?? []).every(l => c.labels.includes(l))
      && (!f.unassigned || !c.assigneeIds.length)
      && (!f.assigneeIds?.length || f.assigneeIds.some(a => c.assigneeIds.includes(a === 'me' ? 'u1' : a))))
  }

  async getCard(id: string) {
    const card = this.card(id)
    return { card, issue: card.external ? create(TrackerIssueSchema, { id: card.external.externalId, title: card.title, url: card.external.url, description: 'From the provider' }) : undefined, issueError: '', comments: this.comments[card.id] ?? [], activity: [] }
  }

  async createCard(d: { boardId: string, stageId?: string, title: string }) {
    const b = this.boards.find(x => x.id === d.boardId)
    if (!b) throw new Error('no such board')
    if (!d.title.trim()) throw new Error('title is required')
    const n = ++this.seq
    const c = create(CardSchema, { id: `crd_${n}`, key: `${this.project(b.projectId).metadata?.ticker}-${n}`, projectId: b.projectId, boardId: d.boardId, stageId: d.stageId || b.stages[0]!.id, title: d.title.trim(), version: 1, priority: CardPriority.NORMAL, createdAt: iso(0), updatedAt: iso(0) })
    this.cards.push(c)
    return c
  }

  async updateCard(id: string, p: CardPatch) {
    const c = this.card(id)
    Object.assign(c, Object.fromEntries(Object.entries(p).filter(([, v]) => v !== undefined)))
    c.version++
    return c
  }

  async moveCard(id: string, to: { stageId: string, beforeCardId?: string, expectedVersion?: number }) {
    const c = this.card(id)
    if (to.expectedVersion && to.expectedVersion !== c.version) throw new Error('Someone else moved this issue first. Reload the board.')
    const left = c.stageId !== to.stageId
    c.stageId = to.stageId
    c.version++
    const rest = this.cards.filter(x => x !== c)
    const at = to.beforeCardId ? rest.findIndex(x => x.id === to.beforeCardId) : -1
    rest.splice(at < 0 ? rest.length : at, 0, c)
    this.cards = rest
    if (left) {
      const board = this.boards.find(b => b.id === c.boardId)
      const out = board?.stages.find(s => s.id === to.stageId)?.outputs.find(o => o.connection?.id === c.external?.connection?.id)
      c.output = out && c.external ? create(CardOutputSchema, { id: this.id('cou'), stageId: to.stageId, connection: out.connection, status: CardOutputStatus.DONE, fields: out.fields, updatedAt: iso(0) }) : undefined
    }
    return c
  }

  async deleteCard(id: string) {
    this.cards = this.cards.filter(c => c.id !== id)
  }

  async retryOutput(id: string) {
    const c = this.card(id)
    if (c.output) c.output.status = CardOutputStatus.DONE
    return c
  }

  async comment(id: string, text: string) {
    const c = this.card(id)
    ;(this.comments[c.id] ??= []).push(create(CardCommentSchema, { id: this.id('cmt'), authorId: 'u1', authorName: 'You', text, createdAt: iso(0) }))
    c.commentCount++
  }

  async deleteComment(id: string) {
    for (const k of Object.keys(this.comments)) this.comments[k] = this.comments[k]!.filter(c => c.id !== id)
  }

  async relate(id: string, kind: CardRelationKind, other: string, remove = false) {
    const c = this.card(id)
    const o = this.card(other)
    c.relations = c.relations.filter(r => !(r.cardId === o.id && r.kind === kind))
    if (!remove) c.relations.push({ kind, cardId: o.id, key: o.key, title: o.title, $typeName: 'harness.library.v1.CardRelation' })
    return c
  }

  async linkSession(id: string, sessionId: string, branch: string) {
    const c = this.card(id)
    if (!c.sessionIds.includes(sessionId)) c.sessionIds.push(sessionId)
    c.branch = branch
    return c
  }

  async listViews() {
    return [...this.views]
  }

  async saveView(name: string, filter: CardFilterInit) {
    const v = create(SavedViewSchema, { id: this.id('view'), name, filter, updatedAt: iso(0) })
    this.views.push(v)
    return v
  }

  async deleteView(id: string) {
    this.views = this.views.filter(v => v.id !== id)
  }

  async listConnections() {
    return { connections: [...this.connections], providers: this.providers }
  }

  async saveConnection(scope: TrackerConnectionScope, input: ConnectionInputInit) {
    if (!input.baseUrl?.trim()) throw new Error('the address must be an http(s) URL')
    const existing = this.connections.find(c => c.id === input.id)
    const c = create(TrackerConnectionSchema, { id: input.id || this.id('tc'), scope, provider: input.provider ?? 'youtrack', name: input.name || input.baseUrl, baseUrl: input.baseUrl, auth: input.auth, login: input.login ?? '', oauthClientId: input.oauthClientId ?? '', hasSecret: !!input.secret || !!existing?.hasSecret, enabled: input.enabled ?? true, knowledgeEnabled: input.knowledgeEnabled ?? false, knowledgeQuery: input.knowledgeQuery ?? '', allowPrivateNetwork: input.allowPrivateNetwork ?? false })
    this.connections = existing ? this.connections.map(x => (x === existing ? c : x)) : [...this.connections, c]
    return c
  }

  async deleteConnection(_scope: TrackerConnectionScope, id: string) {
    this.connections = this.connections.filter(c => c.id !== id)
  }

  async testConnection() {
    return { ok: true, error: '', login: 'you', displayName: 'You' }
  }

  async searchConnection() {
    return [create(TrackerIssueSchema, { id: 'DEMO-9', title: 'A matching issue', url: 'https://yt.example.test/issue/DEMO-9' })]
  }

  async listFields() {
    return [create(TrackerFieldSchema, { name: 'Stage', kind: 'bundle', values: ['Backlog', 'Develop', 'Review'] })]
  }

  async startOAuth() {
    return { url: 'https://yt.example.test/hub/api/rest/oauth2/auth', flowId: 'flow_1' }
  }

  async oauthStatus() {
    return { done: true, error: '' }
  }

  async deviceIssue() {
    return undefined
  }

  async syncDevice() {}

  async listFolderLinks() {
    return [...this.links]
  }

  async setFolderLink(folder: string, projectId: string) {
    this.links = this.links.filter(l => l.folder !== folder)
    if (projectId) this.links.push(create(FolderProjectSchema, { folder, projectId }))
  }

  async prepareSession(cardId: string) {
    const c = this.card(cardId)
    return { folder: this.links.find(l => l.projectId === c.projectId)?.folder ?? '', branch: `issue/${c.key.toLowerCase()}`, prompt: `Work on ${c.key}: ${c.title}` }
  }
}
