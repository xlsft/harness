// IssuesApi: the Issues screen and Settings > Issue trackers. Projects, boards and cards are the server's (harness.library.v1 through the
// client's proxy), connections on this device and folder links are the client's (harness.agent.v1.LocalTrackerService). Both decide
// what exists and who may do what; this only names things for the screens. Two implementations: connect.ts and mock.ts.

import { tt } from '~/utils/i18n'
import type { Board, BoardStage, Card, CardActivity, CardComment, CardFilter, SavedView } from '~/gen/harness/library/v1/boards_pb'
import { BoardScope, CardOutputStatus, CardPriority, CardRelationKind, ExternalState } from '~/gen/harness/library/v1/boards_pb'
import type { Project, ProjectMember, ProjectMetadata, ProjectShare, ProviderProject } from '~/gen/harness/library/v1/projects_pb'
import { ProjectLevel, ProjectScope, ProjectShareSubject } from '~/gen/harness/library/v1/projects_pb'
import type { ProviderAuthHint, ProviderOptionField, TrackerCapabilities, TrackerConnection, TrackerConnectionInput, TrackerConnectionRef, TrackerField, TrackerIssue } from '~/gen/harness/library/v1/tracker_connections_pb'
import { TrackerAuthMethod, TrackerConnectionScope } from '~/gen/harness/library/v1/tracker_connections_pb'
import type { FolderProject } from '~/gen/harness/agent/v1/trackers_pb'
import type { KnowledgeCapabilities, KnowledgeConnection, KnowledgeConnectionInput } from '~/gen/harness/library/v1/knowledge_connections_pb'

export type { Board, BoardStage, Card, CardActivity, CardComment, CardFilter, FolderProject, Project, ProjectMember, ProjectMetadata, ProjectShare, ProviderProject, SavedView, TrackerCapabilities, TrackerConnection, TrackerConnectionInput, TrackerConnectionRef, TrackerField, TrackerIssue }
export type { KnowledgeCapabilities, KnowledgeConnection, KnowledgeConnectionInput, ProviderAuthHint, ProviderOptionField }
export { BoardScope, CardOutputStatus, CardPriority, CardRelationKind, ExternalState, ProjectLevel, ProjectScope, ProjectShareSubject, TrackerAuthMethod, TrackerConnectionScope }

export type ConnectionInputInit = Partial<Omit<TrackerConnectionInput, '$typeName' | '$unknown'>>
export type KnowledgeInputInit = Partial<Omit<KnowledgeConnectionInput, '$typeName' | '$unknown'>>
export type MetadataInit = Partial<Omit<ProjectMetadata, '$typeName' | '$unknown'>>
export type ShareInit = { subject: ProjectShareSubject, subjectId: string, level: ProjectLevel }
export type CardFilterInit = Partial<Omit<CardFilter, '$typeName' | '$unknown'>>
export type StageInit = { id?: string, name: string, inputs?: { id?: string, connection: { scope: TrackerConnectionScope, id: string }, query: string, intervalSeconds?: number }[], outputs?: { id?: string, connection: { scope: TrackerConnectionScope, id: string }, fields: { name: string, value: string }[], comment?: string }[] }
export type CardPatch = { title?: string, description?: string, priority?: CardPriority, labels?: string[], assigneeIds?: string[], dueAt?: string }

export interface TestOutcome {
  ok: boolean
  error: string
  login: string
  displayName: string
}

export interface CardDetail {
  card: Card
  issue?: TrackerIssue
  issueError: string
  comments: CardComment[]
  activity: CardActivity[]
}

export interface IssuesApi {
  listProjects(): Promise<Project[]>
  ensureDefaultProject(): Promise<Project>
  createProject(d: { scope: ProjectScope, metadata: MetadataInit, connection?: { scope: TrackerConnectionScope, id: string }, externalKey?: string, provider?: MetadataInit }): Promise<Project>
  updateProject(id: string, patch: MetadataInit): Promise<Project>
  deleteProject(id: string): Promise<void>
  setShares(id: string, shares: ShareInit[]): Promise<Project>
  listMembers(id: string): Promise<ProjectMember[]>
  listProviderProjects(connection: { scope: TrackerConnectionScope, id: string }): Promise<ProviderProject[]>

  listBoards(projectId: string): Promise<Board[]>
  getBoard(id: string): Promise<{ board: Board, cards: Card[] }>
  createBoard(d: { projectId: string, scope: BoardScope, name: string }): Promise<Board>
  updateBoard(id: string, name: string, stages: StageInit[]): Promise<Board>
  deleteBoard(id: string): Promise<void>
  syncBoard(id: string): Promise<number>

  listCards(filter: CardFilterInit): Promise<Card[]>
  getCard(id: string): Promise<CardDetail>
  createCard(d: { boardId: string, stageId?: string, title: string }): Promise<Card>
  updateCard(id: string, patch: CardPatch): Promise<Card>
  moveCard(id: string, to: { stageId: string, beforeCardId?: string, expectedVersion?: number }): Promise<Card>
  deleteCard(id: string): Promise<void>
  retryOutput(id: string): Promise<Card>
  comment(id: string, text: string): Promise<void>
  deleteComment(id: string): Promise<void>
  relate(id: string, kind: CardRelationKind, other: string, remove?: boolean): Promise<Card>
  linkSession(id: string, sessionId: string, branch: string): Promise<Card>

  listViews(): Promise<SavedView[]>
  saveView(name: string, filter: CardFilterInit): Promise<SavedView>
  deleteView(id: string): Promise<void>

  // Admin connections on the server and personal ones on this device, together.
  listConnections(): Promise<{ connections: TrackerConnection[], providers: TrackerCapabilities[] }>
  saveConnection(scope: TrackerConnectionScope, input: ConnectionInputInit): Promise<TrackerConnection>
  deleteConnection(scope: TrackerConnectionScope, id: string): Promise<void>
  testConnection(scope: TrackerConnectionScope, target: { id: string } | { draft: ConnectionInputInit }): Promise<TestOutcome>
  searchConnection(connection: { scope: TrackerConnectionScope, id: string }, query: string): Promise<TrackerIssue[]>
  listFields(connection: { scope: TrackerConnectionScope, id: string }, project: string): Promise<TrackerField[]>
  // Where to send the browser. A flow id on a device connection, to ask how it went; '' when the server finishes it.
  startOAuth(scope: TrackerConnectionScope, id: string): Promise<{ url: string, flowId: string }>
  oauthStatus(flowId: string): Promise<{ done: boolean, error: string }>
  deviceIssue(connectionId: string, id: string): Promise<TrackerIssue | undefined>

  // Knowledge source connections, company ones on the server and personal ones on this device. Each scope lists its own providers.
  listKnowledgeConnections(): Promise<{ connections: KnowledgeConnection[], deviceProviders: KnowledgeCapabilities[], companyProviders: KnowledgeCapabilities[] }>
  saveKnowledgeConnection(scope: TrackerConnectionScope, input: KnowledgeInputInit): Promise<KnowledgeConnection>
  deleteKnowledgeConnection(scope: TrackerConnectionScope, id: string): Promise<void>
  testKnowledgeConnection(scope: TrackerConnectionScope, target: { id: string } | { draft: KnowledgeInputInit }): Promise<TestOutcome>
  // Like startOAuth; a device flow is polled with oauthStatus.
  startKnowledgeOAuth(scope: TrackerConnectionScope, id: string): Promise<{ url: string, flowId: string }>
  syncDevice(): Promise<void>

  listFolderLinks(): Promise<FolderProject[]>
  setFolderLink(folder: string, projectId: string): Promise<void>
  prepareSession(cardId: string): Promise<{ folder: string, branch: string, prompt: string }>
}

export const PRIORITIES: { value: CardPriority, label: string }[] = [
  { value: CardPriority.URGENT, label: tt('Urgent') },
  { value: CardPriority.HIGH, label: tt('High') },
  { value: CardPriority.NORMAL, label: tt('Normal') },
  { value: CardPriority.LOW, label: tt('Low') }
]

export const LEVELS: { value: ProjectLevel, label: string }[] = [
  { value: ProjectLevel.VIEW, label: tt('Can view') },
  { value: ProjectLevel.EDIT, label: tt('Can edit') },
  { value: ProjectLevel.MANAGE, label: tt('Can manage') }
]

export const RELATIONS: { value: CardRelationKind, label: string }[] = [
  { value: CardRelationKind.BLOCKED_BY, label: tt('Blocked by') },
  { value: CardRelationKind.BLOCKS, label: tt('Blocks') },
  { value: CardRelationKind.RELATES, label: tt('Relates to') },
  { value: CardRelationKind.DUPLICATES, label: tt('Duplicates') }
]

export const AUTH_LABELS: Record<number, string> = {
  [TrackerAuthMethod.TOKEN]: tt('Token'),
  [TrackerAuthMethod.OAUTH]: tt('OAuth'),
  [TrackerAuthMethod.PASSWORD]: tt('Login and password'),
  [TrackerAuthMethod.EMAIL_TOKEN]: tt('Email and API token'),
  [TrackerAuthMethod.VCS_CONNECTION]: tt('Code host sign-in')
}

// What the form says under a method when the provider says nothing of its own.
export const AUTH_HELP: Record<number, string> = {
  [TrackerAuthMethod.TOKEN]: tt('A token you make on the service. It has your account\'s rights.'),
  [TrackerAuthMethod.OAUTH]: tt('Save, then Authorize: the service asks you to approve in the browser.'),
  [TrackerAuthMethod.PASSWORD]: tt('Your login and password on the service.'),
  [TrackerAuthMethod.EMAIL_TOKEN]: tt('Your account email and an API token made on the service.'),
  [TrackerAuthMethod.VCS_CONNECTION]: tt('On this device through gh or glab signed in to the host; for the company, through a connection from Settings, VCS.')
}

// What a provider's sign-in form needs, from TrackerCapabilities or KnowledgeCapabilities alike.
export interface SignInSpec {
  authMethods: TrackerAuthMethod[]
  optionFields: ProviderOptionField[]
  authHints: ProviderAuthHint[]
  baseUrlPlaceholder: string
  defaultBaseUrl: string
}

// The sign-in part of a connection form. Secrets are write-only: empty keeps the stored one.
export interface SignInForm {
  id: string
  baseUrl: string
  auth: TrackerAuthMethod
  login: string
  secret: string
  oauthClientId: string
  oauthClientSecret: string
  options: Record<string, string>
}

// The tile of a provider: its logo when providerLogos.ts has the capability's key, else two letters of its name.
export function providerTile(logo: string, displayName: string, logos: Record<string, string>): string {
  if (logo && logos[logo]) return logo
  if (logos[displayName.toLowerCase()]) return displayName.toLowerCase()
  const caps = displayName.match(/[A-Z]/g)?.join('') ?? ''
  return (caps.length >= 2 ? caps : displayName.replace(/[^A-Za-z]/g, '')).slice(0, 2).toUpperCase() || 'PR'
}

// Whether a method stores a secret the form asks for.
export const takesSecret = (m: TrackerAuthMethod) => m === TrackerAuthMethod.TOKEN || m === TrackerAuthMethod.PASSWORD || m === TrackerAuthMethod.EMAIL_TOKEN
// Whether a method takes a login.
export const takesLogin = (m: TrackerAuthMethod) => m === TrackerAuthMethod.PASSWORD || m === TrackerAuthMethod.EMAIL_TOKEN

export function priorityLabel(p: CardPriority): string {
  return PRIORITIES.find(x => x.value === p)?.label ?? ''
}

export function relationLabel(k: CardRelationKind): string {
  return RELATIONS.find(x => x.value === k)?.label ?? ''
}

// The cards of each stage, in the server's order.
export function cardsByStage(stages: { id: string }[], cards: Card[]): Record<string, Card[]> {
  const out: Record<string, Card[]> = {}
  for (const s of stages) out[s.id] = []
  for (const c of cards) (out[c.stageId] ??= []).push(c)
  return out
}

// What the card should show about the last time an output was applied. '' when there is nothing to say.
export function outputNote(c: Card): { text: string, bad: boolean } {
  const o = c.output
  if (!o) return { text: '', bad: false }
  switch (o.status) {
    case CardOutputStatus.FAILED:
      return { text: tt('Output failed: {0}', [o.error]), bad: true }
    case CardOutputStatus.PENDING:
      if (o.error) return { text: tt('Output will retry: {0}', [o.error]), bad: true }
      return { text: o.connection?.scope === TrackerConnectionScope.DEVICE ? tt('Output waits for the device with this connection') : tt('Output pending'), bad: false }
    case CardOutputStatus.RUNNING:
      return { text: tt('Output running'), bad: false }
  }
  return { text: '', bad: false }
}

export function externalNote(c: Card): string {
  switch (c.external?.state) {
    case ExternalState.MISSING:
      return tt('The linked issue is gone, or this account can\'t see it')
    case ExternalState.DISCONNECTED:
      return tt('The connection of the linked issue was removed')
  }
  return ''
}

// A new stage list from what the form holds, keeping ids so stages and inputs keep their state.
export function stagesToInit(stages: BoardStage[]): StageInit[] {
  return stages.map(s => ({
    id: s.id,
    name: s.name,
    inputs: s.inputs.map(i => ({ id: i.id, connection: { scope: i.connection?.scope ?? TrackerConnectionScope.ADMIN, id: i.connection?.id ?? '' }, query: i.query, intervalSeconds: i.intervalSeconds })),
    outputs: s.outputs.map(o => ({ id: o.id, connection: { scope: o.connection?.scope ?? TrackerConnectionScope.ADMIN, id: o.connection?.id ?? '' }, fields: o.fields.map(f => ({ name: f.name, value: f.value })), comment: o.comment }))
  }))
}
