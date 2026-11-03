// FilesApi over AgentService's files RPCs (packages/client/server/filebrowser.go), on the same connect transport
// (and agent token) as the rest of AgentService. Search reuses SuggestFiles (the composer's @-mentions).

import { createClient, type Client, type Transport } from '@connectrpc/connect'
import { AgentService } from '~/gen/harness/agent/v1/agent_pb'
import { FileKind, GitFileStatus, type FileEntry as FileEntryPb } from '~/gen/harness/agent/v1/files_pb'
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
