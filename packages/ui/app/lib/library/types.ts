// LibraryApi: the screens over the user's artifacts, knowledge base and task tracker (harness.library.v1 through the
// client's proxy), and memory, export and transcript search (harness.agent.v1.LibraryService on the client). The server
// and the client decide what exists and who may see it; this only names things for the screens. Two implementations:
// connect.ts and mock.ts.

import { tt } from '~/utils/i18n'
import type { Artifact, ArtifactVersion, GetArtifactResponse } from '~/gen/harness/library/v1/artifacts_pb'
import type { GetKnowledgeDocumentResponse, KnowledgeDocument, KnowledgeHit } from '~/gen/harness/library/v1/knowledge_pb'
import type { TrackerTask } from '~/gen/harness/library/v1/tracker_pb'
import type { AutoMemoryProject, TranscriptHit } from '~/gen/harness/agent/v1/library_pb'
import { ArtifactKind, ArtifactVisibility, ListArtifactsRequest_Filter } from '~/gen/harness/library/v1/artifacts_pb'
import { TrackerPriority, TrackerStatus } from '~/gen/harness/library/v1/tracker_pb'
import { ExportFormat } from '~/gen/harness/agent/v1/library_pb'
import { Scope } from '~/gen/harness/common/v1/scope_pb'
import type { IconName } from '~/utils/icons'

export type { Artifact, ArtifactVersion, GetArtifactResponse, KnowledgeDocument, KnowledgeHit, GetKnowledgeDocumentResponse, TrackerTask, AutoMemoryProject, TranscriptHit }
export { ArtifactKind, ArtifactVisibility, ExportFormat, ListArtifactsRequest_Filter as ArtifactFilter, Scope, TrackerPriority, TrackerStatus }

export interface TaskDraft {
  title: string
  description?: string
  status?: TrackerStatus
  priority?: TrackerPriority
  project?: string
  blockedBy?: string[]
}

export interface TaskPatch {
  title?: string
  description?: string
  status?: TrackerStatus
  priority?: TrackerPriority
  project?: string
}

export interface LibraryApi {
  listArtifacts(q: { filter: ListArtifactsRequest_Filter, query?: string, sessionId?: string }): Promise<Artifact[]>
  getArtifact(id: string, version?: number): Promise<GetArtifactResponse>
  renameArtifact(id: string, title: string): Promise<Artifact>
  setArtifactShared(id: string, shared: boolean): Promise<Artifact>
  deleteArtifact(id: string): Promise<void>
  // An address for a frame, image or link. It reads this artifact and nothing else; '' when there is no server.
  artifactUrl(id: string, opts?: { version?: number, download?: boolean }): Promise<string>

  listDocuments(q?: { scope?: Scope, query?: string }): Promise<KnowledgeDocument[]>
  getDocument(id: string): Promise<GetKnowledgeDocumentResponse>
  createDocument(d: { scope: Scope, title: string, content: string, sourceName?: string }): Promise<KnowledgeDocument>
  updateDocument(id: string, patch: { title?: string, content?: string }): Promise<KnowledgeDocument>
  deleteDocument(id: string): Promise<void>
  searchKnowledge(query: string, limit?: number): Promise<KnowledgeHit[]>

  listTasks(q?: { query?: string }): Promise<TrackerTask[]>
  createTask(d: TaskDraft): Promise<TrackerTask>
  updateTask(id: string, patch: TaskPatch): Promise<TrackerTask>
  deleteTask(id: string): Promise<void>

  listMemory(folder?: string): Promise<AutoMemoryProject[]>
  saveMemory(projectKey: string, name: string, content: string): Promise<AutoMemoryProject>
  deleteMemory(projectKey: string, name: string): Promise<AutoMemoryProject>

  exportSession(sessionId: string, format: ExportFormat): Promise<{ name: string, mediaType: string, content: Uint8Array<ArrayBuffer> }>
  searchTranscripts(query: string, limit?: number): Promise<TranscriptHit[]>
}

// How each kind of artifact looks on the screen.
export const ARTIFACT_VIEW: Record<ArtifactKind, { label: string, icon: IconName, tint: string, color: string }> = {
  [ArtifactKind.UNSPECIFIED]: { label: tt('File'), icon: 'file', tint: 'rgba(160,160,160,.1)', color: '#9a9a9a' },
  [ArtifactKind.PAGE]: { label: tt('Page'), icon: 'page', tint: 'rgba(90,163,255,.1)', color: '#5aa3ff' },
  [ArtifactKind.MARKDOWN]: { label: tt('Document'), icon: 'doc', tint: 'rgba(138,201,123,.1)', color: '#8ac97b' },
  [ArtifactKind.TEXT]: { label: tt('Text'), icon: 'file', tint: 'rgba(160,160,160,.1)', color: '#9a9a9a' },
  [ArtifactKind.IMAGE]: { label: tt('Image'), icon: 'image', tint: 'rgba(214,145,226,.1)', color: '#d691e2' },
  [ArtifactKind.PDF]: { label: 'PDF', icon: 'slides', tint: 'rgba(234,90,79,.1)', color: '#ea5a4f' },
  [ArtifactKind.FILE]: { label: tt('File'), icon: 'file', tint: 'rgba(160,160,160,.1)', color: '#9a9a9a' }
}

// The columns of the tracker, in the order work moves through them.
export const TASK_STATUSES: { status: TrackerStatus, label: string }[] = [
  { status: TrackerStatus.TODO, label: tt('To do') },
  { status: TrackerStatus.IN_PROGRESS, label: tt('In progress') },
  { status: TrackerStatus.BLOCKED, label: tt('Blocked') },
  { status: TrackerStatus.DONE, label: tt('Done') },
  { status: TrackerStatus.CANCELLED, label: tt('Cancelled') }
]

export const TASK_PRIORITIES: { priority: TrackerPriority, label: string }[] = [
  { priority: TrackerPriority.HIGH, label: tt('High') },
  { priority: TrackerPriority.NORMAL, label: tt('Normal') },
  { priority: TrackerPriority.LOW, label: tt('Low') }
]

export function statusLabel(s: TrackerStatus): string {
  return TASK_STATUSES.find(x => x.status === s)?.label ?? 'To do'
}

// The tasks by column; every column is there, empty or not, and the server's order holds inside it.
export function groupTasks(tasks: TrackerTask[]): { status: TrackerStatus, label: string, tasks: TrackerTask[] }[] {
  return TASK_STATUSES.map(c => ({ ...c, tasks: tasks.filter(t => t.status === c.status) }))
}

export function scopeLabel(s: Scope): string {
  return s === Scope.ORG ? 'Everyone' : 'Only you'
}

export function formatSize(n: bigint | number): string {
  const v = Number(n)
  if (v < 1024) return `${v} B`
  if (v < 1 << 20) return `${(v / 1024).toFixed(v < 10240 ? 1 : 0)} KB`
  return `${(v / (1 << 20)).toFixed(1)} MB`
}

// "2 min ago", "3 days ago", then the date: what a list needs to tell things apart.
export function timeAgo(iso: string, now = Date.now()): string {
  const t = Date.parse(iso)
  if (Number.isNaN(t)) return ''
  const s = Math.max(0, Math.round((now - t) / 1000))
  if (s < 60) return 'just now'
  if (s < 3600) return `${Math.floor(s / 60)} min ago`
  if (s < 86400) return `${Math.floor(s / 3600)} h ago`
  if (s < 30 * 86400) {
    const d = Math.floor(s / 86400)
    return d === 1 ? 'yesterday' : `${d} days ago`
  }
  return new Date(t).toLocaleDateString()
}

// Saves bytes as a file through the browser's download.
export function saveBytes(name: string, mediaType: string, bytes: Uint8Array<ArrayBuffer>) {
  const a = document.createElement('a')
  a.href = URL.createObjectURL(new Blob([bytes], { type: mediaType || 'application/octet-stream' }))
  a.download = name
  a.click()
  setTimeout(() => URL.revokeObjectURL(a.href), 1000)
}

export function errorText(e: unknown): string {
  return e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e)
}
