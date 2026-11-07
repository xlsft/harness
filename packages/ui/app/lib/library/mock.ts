// LibraryApi in memory, for the mock transport: enough behavior to drive the screens without a client or a server.

import { create } from '@bufbuild/protobuf'
import { TranscriptHitSchema, AutoMemoryProjectSchema, AutoMemoryFileSchema } from '~/gen/harness/agent/v1/library_pb'
import { ArtifactSchema, GetArtifactResponseSchema } from '~/gen/harness/library/v1/artifacts_pb'
import { GetKnowledgeDocumentResponseSchema, KnowledgeDocumentSchema, KnowledgeHitSchema, KnowledgeSourceSchema } from '~/gen/harness/library/v1/knowledge_pb'
import {
  ArtifactFilter,
  ArtifactKind,
  ArtifactVisibility,
  ExportFormat,
  Scope,
  KnowledgeSourceKind,
  type Artifact,
  type AutoMemoryProject,
  type KnowledgeDocument,
  type LibraryApi
} from './types'

const iso = (minutesAgo: number) => new Date(Date.now() - minutesAgo * 60_000).toISOString()

function artifact(id: string, title: string, kind: ArtifactKind, body: string, name: string, minutesAgo: number, over: { mine?: boolean, ownerName?: string, visibility?: ArtifactVisibility } = {}): { a: Artifact, body: string } {
  return {
    body,
    a: create(ArtifactSchema, {
      id, title, kind, visibility: ArtifactVisibility.PRIVATE, mine: true, createdAt: iso(minutesAgo), updatedAt: iso(minutesAgo),
      current: { number: 1, createdAt: iso(minutesAgo), ref: { sha256: id, size: BigInt(body.length), mediaType: kind === ArtifactKind.PAGE ? 'text/html' : 'text/markdown', name } },
      ...over
    })
  }
}

export class MockLibraryApi implements LibraryApi {
  private artifacts = [
    artifact('art_report', 'Release readiness report', ArtifactKind.PAGE, '<!doctype html><body style="font:14px sans-serif"><h1>Release readiness</h1><p id="n"></p><script>document.getElementById("n").textContent = "Runs in a sandbox: origin " + location.origin</script></body>', 'report.html', 12),
    artifact('art_notes', 'Migration notes', ArtifactKind.MARKDOWN, '# Migration notes\n\nThe upload path moved to the object store.', 'notes.md', 200),
    artifact('art_team', 'Team handbook', ArtifactKind.MARKDOWN, '# Handbook\n\nShared with everyone on the server.', 'handbook.md', 3000, { mine: false, ownerName: 'Dana', visibility: ArtifactVisibility.SHARED })
  ]

  private documents = [
    { d: create(KnowledgeDocumentSchema, { id: 'kb_runbook', scope: Scope.ORG, title: 'Deployment runbook', sourceName: 'runbook.md', size: 61n, editable: false, createdAt: iso(4000), updatedAt: iso(4000) }), content: 'To deploy the gateway, run the pipeline and watch the canary.' },
    { d: create(KnowledgeDocumentSchema, { id: 'kb_notes', scope: Scope.USER, title: 'My notes on billing', size: 34n, editable: true, createdAt: iso(90), updatedAt: iso(90) }), content: 'Invoices close on the last Friday.' }
  ]

  private memory = new Map<string, Map<string, string>>([
    ['github.com/xlsft/harness', new Map([['release_process.md', '---\nname: Release process\ndescription: Staging first, since the January outage\ntype: project\n---\n\nEvery release goes through staging.\n']])]
  ])

  private seq = 10

  async listArtifacts(q: { filter: ArtifactFilter, query?: string, sessionId?: string }) {
    const text = q.query?.trim().toLowerCase()
    return this.artifacts.map(x => x.a).filter((a) => {
      if (q.filter === ArtifactFilter.MINE && !a.mine) return false
      if (q.filter === ArtifactFilter.SHARED && a.mine) return false
      if (q.sessionId && a.sessionId !== q.sessionId) return false
      return !text || a.title.toLowerCase().includes(text)
    })
  }

  private artifactOf(id: string) {
    const x = this.artifacts.find(a => a.a.id === id)
    if (!x) throw new Error('[not_found] no such artifact')
    return x
  }

  async getArtifact(id: string) {
    const x = this.artifactOf(id)
    const isText = x.a.kind === ArtifactKind.MARKDOWN || x.a.kind === ArtifactKind.TEXT
    return create(GetArtifactResponseSchema, { artifact: x.a, versions: x.a.current ? [x.a.current] : [], text: isText ? x.body : '' })
  }

  async renameArtifact(id: string, title: string) {
    const x = this.artifactOf(id)
    if (!title.trim()) throw new Error('[invalid_argument] title is required')
    x.a = { ...x.a, title: title.trim() }
    return x.a
  }

  async setArtifactShared(id: string, shared: boolean) {
    const x = this.artifactOf(id)
    if (!x.a.mine) throw new Error('[permission_denied] only the owner can change an artifact')
    x.a = { ...x.a, visibility: shared ? ArtifactVisibility.SHARED : ArtifactVisibility.PRIVATE }
    return x.a
  }

  async deleteArtifact(id: string) {
    this.artifactOf(id)
    this.artifacts = this.artifacts.filter(a => a.a.id !== id)
  }

  async artifactUrl(id: string) {
    return `data:text/html;charset=utf-8,${encodeURIComponent(this.artifactOf(id).body)}`
  }

  async listDocuments(q: { scope?: Scope, query?: string } = {}) {
    const text = q.query?.trim().toLowerCase()
    return this.documents.map(x => x.d).filter(d => (!q.scope || d.scope === q.scope) && (!text || d.title.toLowerCase().includes(text)))
  }

  private documentOf(id: string) {
    const x = this.documents.find(d => d.d.id === id)
    if (!x) throw new Error('[not_found] no such document')
    return x
  }

  async getDocument(id: string) {
    const x = this.documentOf(id)
    return create(GetKnowledgeDocumentResponseSchema, { document: x.d, content: x.content })
  }

  async createDocument(d: { scope: Scope, title: string, content: string, sourceName?: string }): Promise<KnowledgeDocument> {
    if (!d.title.trim()) throw new Error('[invalid_argument] title is required')
    if (!d.content.trim()) throw new Error('[invalid_argument] content is required')
    const doc = create(KnowledgeDocumentSchema, { id: `kb_${++this.seq}`, scope: d.scope, title: d.title.trim(), sourceName: d.sourceName ?? '', size: BigInt(d.content.length), editable: true, createdAt: iso(0), updatedAt: iso(0) })
    this.documents.unshift({ d: doc, content: d.content })
    return doc
  }

  async updateDocument(id: string, patch: { title?: string, content?: string }) {
    const x = this.documentOf(id)
    if (patch.content !== undefined) x.content = patch.content
    x.d = { ...x.d, title: patch.title ?? x.d.title, size: BigInt(x.content.length), updatedAt: iso(0) }
    return x.d
  }

  async deleteDocument(id: string) {
    this.documentOf(id)
    this.documents = this.documents.filter(d => d.d.id !== id)
  }

  private ref(d: KnowledgeDocument) {
    return d.scope === Scope.ORG
      ? { id: 'internal:org', kind: KnowledgeSourceKind.INTERNAL, scope: Scope.ORG, name: 'Internal, for everyone' }
      : { id: 'internal:user', kind: KnowledgeSourceKind.INTERNAL, scope: Scope.USER, name: 'Internal, only you' }
  }

  async listSources() {
    return [
      create(KnowledgeSourceSchema, { ref: { id: 'internal:org', kind: KnowledgeSourceKind.INTERNAL, scope: Scope.ORG, name: 'Internal, for everyone' }, documentCount: this.documents.filter(x => x.d.scope === Scope.ORG).length }),
      create(KnowledgeSourceSchema, { ref: { id: 'internal:user', kind: KnowledgeSourceKind.INTERNAL, scope: Scope.USER, name: 'Internal, only you' }, documentCount: this.documents.filter(x => x.d.scope === Scope.USER).length, writable: true }),
      create(KnowledgeSourceSchema, { ref: { id: 'tc_company', kind: KnowledgeSourceKind.PROVIDER, scope: Scope.ORG, name: 'Company YouTrack', provider: 'youtrack' } })
    ]
  }

  async browseSource(sourceId: string, parentId = '') {
    if (sourceId === 'tc_company') {
      const top = create(KnowledgeDocumentSchema, { id: 'conn:tc_company:HELP-A-1', title: 'Handbook', scope: Scope.ORG, hasChildren: true, url: 'https://yt.example.test/articles/HELP-A-1' })
      const kid = create(KnowledgeDocumentSchema, { id: 'conn:tc_company:HELP-A-2', title: 'On call', scope: Scope.ORG, url: 'https://yt.example.test/articles/HELP-A-2' })
      return { documents: parentId ? [kid] : [top], hasMore: false }
    }
    const scope = sourceId === 'internal:org' ? Scope.ORG : Scope.USER
    return { documents: this.documents.filter(x => x.d.scope === scope).map(x => x.d), hasMore: false }
  }

  async readDocument(id: string) {
    if (id.startsWith('conn:')) return create(GetKnowledgeDocumentResponseSchema, { document: create(KnowledgeDocumentSchema, { id, title: 'Handbook', scope: Scope.ORG }), content: 'Read live from the provider.' })
    return this.getDocument(id)
  }

  async searchKnowledge(query: string) {
    const words = query.toLowerCase().split(/\s+/).filter(Boolean)
    const hits = this.documents
      .filter(x => words.some(w => `${x.d.title} ${x.content}`.toLowerCase().includes(w)))
      .map(x => create(KnowledgeHitSchema, { documentId: x.d.id, title: x.d.title, scope: x.d.scope, snippet: x.content, score: 1, source: this.ref(x.d) }))
    return { hits, warnings: [] }
  }

  private projectOf(key: string, current = false): AutoMemoryProject {
    const files = this.memory.get(key) ?? new Map<string, string>()
    const list = [...files].sort(([a], [b]) => a.localeCompare(b)).map(([name, content]) => {
      const description = /^description: (.*)$/m.exec(content)?.[1] ?? ''
      return create(AutoMemoryFileSchema, { name, content, description })
    })
    const index = list.map(f => `- [${f.name.replace(/\.md$/, '')}](${f.name}) - ${f.description}\n`).join('')
    return create(AutoMemoryProjectSchema, { projectKey: key, current, index, files: list })
  }

  async listMemory(folder = '') {
    const keys = [...this.memory.keys()]
    return keys.map((k, i) => this.projectOf(k, !!folder && i === 0))
  }

  async saveMemory(projectKey: string, name: string, content: string) {
    if (!/^[a-z0-9][a-z0-9_-]*(\.md)?$/.test(name)) throw new Error('[invalid_argument] use lowercase letters, digits, _ and -')
    const files = this.memory.get(projectKey) ?? new Map<string, string>()
    files.set(name.endsWith('.md') ? name : `${name}.md`, content)
    this.memory.set(projectKey, files)
    return this.projectOf(projectKey)
  }

  async deleteMemory(projectKey: string, name: string) {
    this.memory.get(projectKey)?.delete(name)
    return this.projectOf(projectKey)
  }

  async exportSession(sessionId: string, format: ExportFormat) {
    const md = format === ExportFormat.MARKDOWN
    const text = md ? `# Session ${sessionId}\n\n## User\n\nHello\n` : `{"type":"user","sessionId":"${sessionId}"}\n`
    return { name: `session-${sessionId}.${md ? 'md' : 'jsonl'}`, mediaType: md ? 'text/markdown' : 'application/x-ndjson', content: new TextEncoder().encode(text) as Uint8Array<ArrayBuffer> }
  }

  async searchTranscripts(query: string) {
    const q = query.trim().toLowerCase()
    if (!q) return []
    return [create(TranscriptHitSchema, { sessionId: 's1', title: 'Fix the login test', snippet: `…where we talked about ${q} in this session…`, lastActivityAt: iso(40) })]
  }
}
