// IssuesApi over the client: projects, boards and admin connections through its proxy to the server, device connections and folder
// links its own. All on the same connect transport and agent token.

import { createClient, type Client, type Transport } from '@connectrpc/connect'
import { LocalTrackerService } from '~/gen/harness/agent/v1/trackers_pb'
import { BoardService } from '~/gen/harness/library/v1/boards_pb'
import { ProjectService } from '~/gen/harness/library/v1/projects_pb'
import { TrackerConnectionService } from '~/gen/harness/library/v1/tracker_connections_pb'
import { TrackerConnectionScope, type BoardScope, type CardFilterInit, type CardPatch, type CardRelationKind, type ConnectionInputInit, type IssuesApi, type MetadataInit, type ProjectScope, type ShareInit, type StageInit } from './types'

function need<T>(v: T | undefined, what: string): T {
  if (v === undefined) throw new Error(`the server returned no ${what}`)
  return v
}

const isDevice = (s: TrackerConnectionScope) => s === TrackerConnectionScope.DEVICE

export class ConnectIssuesApi implements IssuesApi {
  private projects: Client<typeof ProjectService>
  private boards: Client<typeof BoardService>
  private conns: Client<typeof TrackerConnectionService>
  private local: Client<typeof LocalTrackerService>

  constructor(transport: Transport) {
    this.projects = createClient(ProjectService, transport)
    this.boards = createClient(BoardService, transport)
    this.conns = createClient(TrackerConnectionService, transport)
    this.local = createClient(LocalTrackerService, transport)
  }

  async listProjects() {
    return (await this.projects.listProjects({})).projects
  }

  async ensureDefaultProject() {
    return need((await this.projects.ensureDefaultProject({})).project, 'project')
  }

  async createProject(d: { scope: ProjectScope, metadata: MetadataInit, connection?: { scope: TrackerConnectionScope, id: string }, externalKey?: string, provider?: MetadataInit }) {
    return need((await this.projects.createProject({ scope: d.scope, metadata: d.metadata, connection: d.connection, externalKey: d.externalKey ?? '', provider: d.provider })).project, 'project')
  }

  async updateProject(id: string, patch: MetadataInit) {
    return need((await this.projects.updateProject({ id, ...patch })).project, 'project')
  }

  async deleteProject(id: string) {
    await this.projects.deleteProject({ id })
  }

  async setShares(id: string, shares: ShareInit[]) {
    return need((await this.projects.setProjectShares({ id, shares })).project, 'project')
  }

  async listMembers(id: string) {
    return (await this.projects.listProjectMembers({ id })).members
  }

  async listProviderProjects(c: { scope: TrackerConnectionScope, id: string }) {
    return isDevice(c.scope) ? (await this.local.listLocalProviderProjects({ connectionId: c.id })).projects : (await this.projects.listProviderProjects({ connectionId: c.id })).projects
  }

  async listBoards(projectId: string) {
    return (await this.boards.listBoards({ projectId })).boards
  }

  async getBoard(id: string) {
    const r = await this.boards.getBoard({ id })
    return { board: need(r.board, 'board'), cards: r.cards }
  }

  async createBoard(d: { projectId: string, scope: BoardScope, name: string }) {
    return need((await this.boards.createBoard(d)).board, 'board')
  }

  async updateBoard(id: string, name: string, stages: StageInit[]) {
    return need((await this.boards.updateBoard({ id, name, stages })).board, 'board')
  }

  async deleteBoard(id: string) {
    await this.boards.deleteBoard({ id })
  }

  async syncBoard(id: string) {
    const r = await this.boards.syncBoard({ id })
    await this.syncDevice().catch(() => {})
    return r.newCards
  }

  async listCards(filter: CardFilterInit) {
    return (await this.boards.listCards({ filter, limit: 500 })).cards
  }

  async getCard(id: string) {
    const r = await this.boards.getCard({ id })
    return { card: need(r.card, 'card'), issue: r.issue, issueError: r.issueError, comments: r.comments, activity: r.activity }
  }

  async createCard(d: { boardId: string, stageId?: string, title: string }) {
    return need((await this.boards.createCard(d)).card, 'card')
  }

  async updateCard(id: string, p: CardPatch) {
    return need((await this.boards.updateCard({
      id,
      title: p.title,
      description: p.description,
      priority: p.priority,
      setLabels: p.labels !== undefined,
      labels: p.labels ?? [],
      setAssignees: p.assigneeIds !== undefined,
      assigneeIds: p.assigneeIds ?? [],
      dueAt: p.dueAt
    })).card, 'card')
  }

  async moveCard(id: string, to: { stageId: string, beforeCardId?: string, expectedVersion?: number }) {
    const card = need((await this.boards.moveCard({ id, ...to })).card, 'card')
    // An output on this device's connection waits for its next tick otherwise.
    if (card.output?.connection?.scope === TrackerConnectionScope.DEVICE) void this.syncDevice().catch(() => {})
    return card
  }

  async deleteCard(id: string) {
    await this.boards.deleteCard({ id })
  }

  async retryOutput(cardId: string) {
    const card = need((await this.boards.retryCardOutput({ cardId })).card, 'card')
    if (card.output?.connection?.scope === TrackerConnectionScope.DEVICE) void this.syncDevice().catch(() => {})
    return card
  }

  async comment(id: string, text: string) {
    await this.boards.commentCard({ id, text })
  }

  async deleteComment(id: string) {
    await this.boards.deleteCardComment({ id })
  }

  async relate(id: string, kind: CardRelationKind, other: string, remove = false) {
    return need((await this.boards.relateCard({ id, kind, other, remove })).card, 'card')
  }

  async linkSession(id: string, sessionId: string, branch: string) {
    return need((await this.boards.linkCard({ id, sessionId, branch })).card, 'card')
  }

  async listViews() {
    return (await this.boards.listSavedViews({})).views
  }

  async saveView(name: string, filter: CardFilterInit) {
    return need((await this.boards.saveView({ name, filter })).view, 'view')
  }

  async deleteView(id: string) {
    await this.boards.deleteSavedView({ id })
  }

  async listConnections() {
    const device = await this.local.listLocalTrackerConnections({})
    const server = await this.conns.listTrackerConnections({}).catch(() => ({ connections: [], providers: [] }))
    return { connections: [...device.connections, ...server.connections], providers: device.providers.length ? device.providers : server.providers }
  }

  async saveConnection(scope: TrackerConnectionScope, connection: ConnectionInputInit) {
    if (isDevice(scope)) return need((await this.local.saveLocalTrackerConnection({ connection })).connection, 'connection')
    return need((await this.conns.putTrackerConnection({ connection })).connection, 'connection')
  }

  async deleteConnection(scope: TrackerConnectionScope, id: string) {
    if (isDevice(scope)) await this.local.deleteLocalTrackerConnection({ id })
    else await this.conns.deleteTrackerConnection({ id })
  }

  async testConnection(scope: TrackerConnectionScope, target: { id: string } | { draft: ConnectionInputInit }) {
    const msg = 'id' in target ? { id: target.id } : { draft: target.draft }
    return isDevice(scope) ? await this.local.testLocalTrackerConnection(msg) : await this.conns.testTrackerConnection(msg)
  }

  async searchConnection(c: { scope: TrackerConnectionScope, id: string }, query: string) {
    return isDevice(c.scope) ? (await this.local.searchLocalTrackerIssues({ connectionId: c.id, query })).issues : (await this.conns.searchConnectionIssues({ connectionId: c.id, query })).issues
  }

  async listFields(c: { scope: TrackerConnectionScope, id: string }, project: string) {
    return isDevice(c.scope) ? (await this.local.listLocalTrackerFields({ connectionId: c.id, project })).fields : (await this.conns.listConnectionFields({ connectionId: c.id, project })).fields
  }

  async startOAuth(scope: TrackerConnectionScope, id: string) {
    if (isDevice(scope)) {
      const r = await this.local.startLocalTrackerOAuth({ connectionId: id })
      return { url: r.authorizeUrl, flowId: r.flowId }
    }
    return { url: (await this.conns.startTrackerOAuth({ connectionId: id })).authorizeUrl, flowId: '' }
  }

  async oauthStatus(flowId: string) {
    return await this.local.getLocalTrackerOAuth({ flowId })
  }

  async deviceIssue(connectionId: string, id: string) {
    return (await this.local.getLocalTrackerIssue({ connectionId, id })).issue
  }

  async syncDevice() {
    await this.local.syncLocalTrackers({})
  }

  async listFolderLinks() {
    return (await this.local.listFolderProjects({})).links
  }

  async setFolderLink(folder: string, projectId: string) {
    await this.local.setFolderProject({ folder, projectId })
  }

  async prepareSession(cardId: string) {
    return await this.local.prepareIssueSession({ cardId })
  }
}
