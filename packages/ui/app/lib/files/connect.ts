// FilesApi over AgentService's files RPCs (packages/client/server/filebrowser.go), on the same connect transport
// (and agent token) as the rest of AgentService. Search reuses SuggestFiles (the composer's @-mentions).

import type { MessageInitShape } from '@bufbuild/protobuf'
import { createClient, type Client, type Transport } from '@connectrpc/connect'
import { AgentService } from '~/gen/harness/agent/v1/agent_pb'
import { FileKind, GitFileStatus, type FileEntry as FileEntryPb } from '~/gen/harness/agent/v1/files_pb'
import { DiffBaseKind as DiffBaseKindPb, FileDiffStatus, type DiffBaseSchema, type FileDiff, type GetSessionDiffResponse, type ListDiffBasesResponse } from '~/gen/harness/agent/v1/session_diff_pb'
import type { DiffBaseChoice, DiffBaseKind, DiffBases, SessionDiff, SessionFileDiff } from './sessionDiff'
import type { EntryKind, FileContent, FileEntry, FilesApi, GitStatus } from './types'

const STATUS: Record<GitFileStatus, GitStatus> = {
  [GitFileStatus.UNSPECIFIED]: '',
  [GitFileStatus.ADDED]: 'A',
  [GitFileStatus.MODIFIED]: 'M',
  [GitFileStatus.DELETED]: 'D',
  [GitFileStatus.UNTRACKED]: 'U',
  [GitFileStatus.RENAMED]: 'R',
  [GitFileStatus.CONFLICTED]: 'C'
}

const KIND: Record<FileKind, EntryKind> = {
  [FileKind.UNSPECIFIED]: 'other',
  [FileKind.FILE]: 'file',
  [FileKind.DIRECTORY]: 'dir',
  [FileKind.SYMLINK]: 'symlink',
  [FileKind.OTHER]: 'other'
}

export function gitStatusView(s: GitFileStatus): GitStatus {
  return STATUS[s] ?? ''
}

export function fileEntryView(e: FileEntryPb): FileEntry {
  return {
    name: e.name,
    path: e.path,
    kind: KIND[e.kind] ?? 'other',
    symlink: e.symlink,
    size: Number(e.size),
    modifiedAt: e.modifiedAt,
    git: gitStatusView(e.gitStatus),
    ignored: e.ignored,
    deleted: e.deleted
  }
}

const DIFF_KIND: Record<FileDiffStatus, SessionFileDiff['kind']> = {
  [FileDiffStatus.UNSPECIFIED]: 'M',
  [FileDiffStatus.ADDED]: 'A',
  [FileDiffStatus.MODIFIED]: 'M',
  [FileDiffStatus.DELETED]: 'D',
  [FileDiffStatus.RENAMED]: 'R'
}

const BASE_KIND: Record<DiffBaseKindPb, DiffBaseKind> = {
  [DiffBaseKindPb.UNSPECIFIED]: 'all',
  [DiffBaseKindPb.ALL]: 'all',
  [DiffBaseKindPb.UNCOMMITTED]: 'uncommitted',
  [DiffBaseKindPb.REF]: 'ref',
  [DiffBaseKindPb.COMMIT]: 'commit'
}

// The request's form of a choice; undefined stays unset, which the client reads as 'all'.
export function diffBaseMessage(base?: DiffBaseChoice): MessageInitShape<typeof DiffBaseSchema> | undefined {
  switch (base?.kind) {
    case 'all': return { kind: DiffBaseKindPb.ALL }
    case 'uncommitted': return { kind: DiffBaseKindPb.UNCOMMITTED }
    case 'ref': return { kind: DiffBaseKindPb.REF, ref: base.ref }
    case 'commit': return { kind: DiffBaseKindPb.COMMIT, sha: base.sha }
  }
  return undefined
}

export function sessionFileDiffView(f: FileDiff): SessionFileDiff {
  return {
    path: f.path,
    label: f.displayPath || f.path,
    kind: DIFF_KIND[f.status] ?? 'M',
    oldPath: f.oldPath,
    add: f.additions,
    del: f.deletions,
    binary: f.binary,
    tooLarge: f.tooLarge,
    hunks: f.hunks.map(h => ({ oldStart: h.oldStart, oldLines: h.oldLines, newStart: h.newStart, newLines: h.newLines, lines: h.lines, ...(h.header ? { header: h.header } : {}) }))
  }
}

export function sessionDiffView(r: GetSessionDiffResponse): SessionDiff {
  return {
    version: r.version,
    notModified: r.notModified,
    truncated: r.truncated,
    filesChanged: r.filesChanged,
    additions: r.additions,
    deletions: r.deletions,
    baseLabel: r.baseLabel,
    headLabel: r.headLabel,
    baseKind: BASE_KIND[r.baseKind] ?? 'all',
    large: r.large,
    refreshAfterMs: r.refreshAfterMs,
    files: r.files.map(sessionFileDiffView)
  }
}

export function diffBasesView(r: ListDiffBasesResponse): DiffBases {
  return {
    inGit: r.inGit,
    branches: r.branches.map(b => ({ name: b.name, current: b.current, remote: b.remote })),
    branchesTruncated: r.branchesTruncated,
    defaultRef: r.defaultBaseRef,
    defaultLabel: r.defaultBaseLabel,
    defaultSha: r.defaultBaseSha,
    commits: r.commits.map(c => ({ sha: c.sha, subject: c.subject, committedAt: c.committedAt })),
    commitsTruncated: r.commitsTruncated
  }
}

export class ConnectFilesApi implements FilesApi {
  private client: Client<typeof AgentService>

  constructor(transport: Transport) {
    this.client = createClient(AgentService, transport)
  }

  async roots(sessionId: string) {
    const res = await this.client.listFileRoots({ sessionId })
    return res.roots.map(r => ({ path: r.path, name: r.name, gitRoot: r.gitRoot }))
  }

  async list(sessionId: string, path: string, opts: { showIgnored?: boolean } = {}) {
    const res = await this.client.listFiles({ sessionId, path, showIgnored: !!opts.showIgnored })
    return { path: res.path, entries: res.entries.map(fileEntryView), truncated: res.truncated }
  }

  async read(sessionId: string, path: string, opts: { maxBytes?: number, ifNoneMatch?: string } = {}): Promise<FileContent> {
    const r = await this.client.readFile({ sessionId, path, maxBytes: BigInt(opts.maxBytes ?? 0), ifNoneMatch: opts.ifNoneMatch ?? '' })
    return {
      path: r.path,
      size: Number(r.size),
      modifiedAt: r.modifiedAt,
      etag: r.etag,
      notModified: r.notModified,
      mime: r.mime,
      binary: r.isBinary,
      text: r.text,
      data: r.data.length ? r.data : undefined,
      truncated: r.truncated,
      tooLarge: r.tooLarge
    }
  }

  async gitStatus(sessionId: string, path = '') {
    const r = await this.client.getGitStatus({ sessionId, path })
    const files: Record<string, GitStatus> = {}
    for (const [p, s] of Object.entries(r.files)) files[p] = gitStatusView(s)
    return { root: r.root, files, truncated: r.truncated }
  }

  async search(sessionId: string, query: string, limit: number) {
    return (await this.client.suggestFiles({ sessionId, query, limit })).paths
  }
}
