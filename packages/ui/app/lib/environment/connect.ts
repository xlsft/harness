// EnvironmentApi over AgentService on the same connect transport (and agent token) as the rest of the client
// calls (packages/client/server/environment.go).

import { ConnectError, createClient, type Client, type Transport } from '@connectrpc/connect'
import {
  AgentService,
  EnvironmentErrorsSchema,
  type Environment,
  type GetFolderInfoResponse
} from '~/gen/harness/agent/v1/agent_pb'
import { EnvironmentParseError, type EnvironmentApi, type EnvironmentDraft, type EnvironmentView, type FolderInfo } from './types'

export function environmentView(e: Environment | undefined): EnvironmentView {
  return {
    folder: e?.folder ?? '',
    name: e?.name ?? '',
    worktree: e?.worktree ?? false,
    baseRef: e?.baseRef ?? '',
    additionalFolders: (e?.additionalFolders ?? []).map(f => ({
      path: f.path,
      worktree: f.worktree,
      baseRef: f.baseRef,
      exists: f.exists,
      gitBranch: f.gitBranch
    })),
    env: e?.env ?? '',
    exists: e?.exists ?? false,
    readOnly: e?.readOnly ?? false,
    updatedAt: e?.updatedAt ?? '',
    variableNames: [...(e?.variableNames ?? [])]
  }
}

export function folderInfoView(r: GetFolderInfoResponse): FolderInfo {
  return {
    path: r.path,
    exists: r.exists,
    repositoryPath: r.repositoryPath,
    currentBranch: r.currentBranch,
    defaultBranch: r.defaultBranch,
    remoteUrl: r.remoteUrl,
    webUrl: r.webUrl,
    repo: r.repo
  }
}

export function environmentMessage(d: EnvironmentDraft) {
  return {
    folder: d.folder,
    name: d.name ?? '',
    worktree: d.worktree ?? false,
    baseRef: d.baseRef ?? '',
    additionalFolders: (d.additionalFolders ?? []).map(f => ({ path: f.path, worktree: f.worktree ?? false, baseRef: f.baseRef ?? '' })),
    env: d.env ?? ''
  }
}

// An INVALID_ARGUMENT with line errors becomes an EnvironmentParseError; anything else is rethrown.
export function parseErrorOf(err: unknown): unknown {
  const ce = ConnectError.from(err)
  const details = ce.findDetails(EnvironmentErrorsSchema)
  if (!details.length) return err
  return new EnvironmentParseError(details.flatMap(d => d.errors.map(e => ({ line: e.line, message: e.message }))))
}

export class ConnectEnvironmentApi implements EnvironmentApi {
  private client: Client<typeof AgentService>

  constructor(transport: Transport) {
    this.client = createClient(AgentService, transport)
  }

  async get(target: { folder: string } | { sessionId: string }) {
    const req = 'sessionId' in target ? { sessionId: target.sessionId } : { folder: target.folder }
    return environmentView((await this.client.getEnvironment(req)).environment)
  }

  async update(draft: EnvironmentDraft, opts: { sessionId?: string, validateOnly?: boolean } = {}) {
    try {
      const res = await this.client.updateEnvironment({
        environment: environmentMessage(draft),
        sessionId: opts.sessionId ?? '',
        validateOnly: opts.validateOnly ?? false
      })
      return { environment: environmentView(res.environment), appliedSessionIds: res.appliedSessionIds }
    } catch (err) {
      throw parseErrorOf(err)
    }
  }

  async folderInfo(path: string) {
    return folderInfoView(await this.client.getFolderInfo({ path }))
  }

  async setSessionFolder(sessionId: string, cwd: string) {
    const res = await this.client.setSessionFolder({ sessionId, cwd })
    if (!res.session) throw new Error('the client returned no session')
    return res.session
  }

  async addSessionDirectory(sessionId: string, path: string) {
    const res = await this.client.addSessionDirectory({ sessionId, path })
    return { environment: environmentView(res.environment), appliedSessionIds: res.appliedSessionIds }
  }
}
