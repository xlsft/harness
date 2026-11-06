// LibraryApi over the client: harness.library.v1 goes through its proxy to the server, memory, export and search are its
// own (LibraryService), all on the same connect transport and agent token.

import { createClient, type Client, type Transport } from '@connectrpc/connect'
import { LibraryService } from '~/gen/harness/agent/v1/library_pb'
import { ArtifactService, ArtifactVisibility } from '~/gen/harness/library/v1/artifacts_pb'
import { KnowledgeService } from '~/gen/harness/library/v1/knowledge_pb'
import { TrackerService } from '~/gen/harness/library/v1/tracker_pb'
import { Scope, type ArtifactFilter, type ExportFormat, type LibraryApi, type TaskDraft, type TaskPatch } from './types'

function need<T>(v: T | undefined, what: string): T {
  if (v === undefined) throw new Error(`the server returned no ${what}`)
  return v
}

export class ConnectLibraryApi implements LibraryApi {
  private artifacts: Client<typeof ArtifactService>
  private knowledge: Client<typeof KnowledgeService>
  private tracker: Client<typeof TrackerService>
  private local: Client<typeof LibraryService>

  constructor(transport: Transport, private readonly baseUrl: string) {
    this.artifacts = createClient(ArtifactService, transport)
    this.knowledge = createClient(KnowledgeService, transport)
    this.tracker = createClient(TrackerService, transport)
    this.local = createClient(LibraryService, transport)
  }

  async listArtifacts(q: { filter: ArtifactFilter, query?: string, sessionId?: string }) {
    return (await this.artifacts.listArtifacts({ filter: q.filter, query: q.query ?? '', sessionId: q.sessionId ?? '', pageSize: 200 })).artifacts
  }

  getArtifact(id: string, version = 0) {
    return this.artifacts.getArtifact({ id, version })
  }

  async renameArtifact(id: string, title: string) {
    return need((await this.artifacts.updateArtifact({ id, title })).artifact, 'artifact')
  }

  async setArtifactShared(id: string, shared: boolean) {
    return need((await this.artifacts.updateArtifact({ id, visibility: shared ? ArtifactVisibility.SHARED : ArtifactVisibility.PRIVATE })).artifact, 'artifact')
  }

  async deleteArtifact(id: string) {
    await this.artifacts.deleteArtifact({ id })
  }

  async artifactUrl(id: string, opts: { version?: number, download?: boolean } = {}) {
    const t = await this.local.createArtifactTicket({ artifactId: id })
    if (!t.available) return ''
    const q = new URLSearchParams({ ticket: t.ticket })
    if (opts.version) q.set('version', String(opts.version))
    if (opts.download) q.set('download', '1')
    return `${this.baseUrl.replace(/\/+$/, '')}/artifacts/${encodeURIComponent(id)}?${q}`
  }

  async listDocuments(q: { scope?: Scope, query?: string } = {}) {
    return (await this.knowledge.listKnowledgeDocuments({ scope: q.scope ?? Scope.UNSPECIFIED, query: q.query ?? '' })).documents
  }

  getDocument(id: string) {
    return this.knowledge.getKnowledgeDocument({ id })
  }

  async createDocument(d: { scope: Scope, title: string, content: string, sourceName?: string }) {
    return need((await this.knowledge.createKnowledgeDocument({ scope: d.scope, title: d.title, content: d.content, sourceName: d.sourceName ?? '' })).document, 'document')
  }

  async updateDocument(id: string, patch: { title?: string, content?: string }) {
    return need((await this.knowledge.updateKnowledgeDocument({ id, title: patch.title, content: patch.content })).document, 'document')
  }

  async deleteDocument(id: string) {
    await this.knowledge.deleteKnowledgeDocument({ id })
  }

  async searchKnowledge(query: string, limit = 12) {
    return (await this.knowledge.searchKnowledge({ query, limit })).hits
  }

  async listTasks(q: { query?: string } = {}) {
    return (await this.tracker.listTrackerTasks({ query: q.query ?? '', limit: 500 })).tasks
  }

  async createTask(d: TaskDraft) {
    return need((await this.tracker.createTrackerTask({ title: d.title, description: d.description ?? '', status: d.status, priority: d.priority, project: d.project ?? '', blockedBy: d.blockedBy ?? [] })).task, 'task')
  }

  async updateTask(id: string, patch: TaskPatch) {
    return need((await this.tracker.updateTrackerTask({ id, title: patch.title, description: patch.description, status: patch.status, priority: patch.priority, project: patch.project })).task, 'task')
  }

  async deleteTask(id: string) {
    await this.tracker.deleteTrackerTask({ id })
  }

  async listMemory(folder = '') {
    return (await this.local.listAutoMemory({ folder })).projects
  }

  async saveMemory(projectKey: string, name: string, content: string) {
    return need((await this.local.saveAutoMemoryFile({ projectKey, name, content })).project, 'project')
  }

  async deleteMemory(projectKey: string, name: string) {
    return need((await this.local.deleteAutoMemoryFile({ projectKey, name })).project, 'project')
  }

  async exportSession(sessionId: string, format: ExportFormat) {
    const r = await this.local.exportSession({ sessionId, format })
    return { name: r.name, mediaType: r.mediaType, content: r.content as Uint8Array<ArrayBuffer> }
  }

  async searchTranscripts(query: string, limit = 30) {
    return (await this.local.searchTranscripts({ query, limit })).hits
  }
}
