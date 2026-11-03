// In-memory EnvironmentApi for the mock transport. Follows the client's rules closely enough to exercise the
// Environment modal and the folder menus: .env validation with line errors, remote sessions are read-only,
// changing the folder needs an idle session outside a worktree, missing folders are kept but not applied.

import type { SessionInfo } from '~/gen/harness/agent/v1/agent_pb'
import { parseEnv } from './dotenv'
import { EnvironmentParseError, type EnvironmentApi, type EnvironmentDraft, type EnvironmentView, type FolderInfo } from './types'

export interface MockEnvironmentHost {
  // The mock user's home ("~" in paths expands to it).
  home: string
  sessions(): SessionInfo[]
  // Whether a folder exists on the mock machine.
  exists(path: string): boolean
  // Repository facts of a folder, when it is in a mock repository.
  repository(path: string): { path: string, branch: string, repo: string } | undefined
  // Moves a session to another folder (throws when it is working).
  moveSession(sessionId: string, cwd: string): SessionInfo
}

const failed = (msg: string) => new Error(`[failed_precondition] ${msg}`)
const invalid = (msg: string) => new Error(`[invalid_argument] ${msg}`)

export class MockEnvironmentApi implements EnvironmentApi {
  private stored = new Map<string, EnvironmentView>()

  constructor(private host: MockEnvironmentHost) {}

  // "~" expanded, no trailing slash, absolute.
  private absolute(p: string) {
    let s = p.trim()
    if (s === '~' || s.startsWith('~/')) s = this.host.home + s.slice(1)
    s = s.replace(/\/+$/, '') || '/'
    if (!s.startsWith('/')) throw invalid(`${p}: path must be absolute`)
    return s
  }

  private session(id: string) {
    const s = this.host.sessions().find(i => i.id === id)
    if (!s) throw new Error(`[not_found] session ${id} not found`)
    return s
  }

  private remote(id: string | undefined) {
    if (!id) return
    const s = this.session(id)
    if (s.remote) throw failed(`session ${s.id} runs on ${s.deviceName || s.deviceId}: its environment and folders can only be changed there`)
  }

  // The session's primary folder: its cwd, or the matching folder of the main checkout in a worktree.
  private folderOf(s: SessionInfo) {
    const wt = s.worktree
    if (wt?.repositoryPath && s.cwd.startsWith(wt.path)) return this.absolute(wt.repositoryPath + s.cwd.slice(wt.path.length))
    return this.absolute(s.cwd)
  }

  private view(folder: string, from?: EnvironmentView): EnvironmentView {
    const base = from ?? { folder, name: '', worktree: false, baseRef: '', additionalFolders: [], env: '', exists: false, readOnly: false, updatedAt: '', variableNames: [] }
    return {
      ...base,
      additionalFolders: base.additionalFolders.map(f => ({ ...f, exists: this.host.exists(f.path), gitBranch: this.host.repository(f.path)?.branch ?? '' })),
      variableNames: parseEnv(base.env).vars.map(v => v.name)
    }
  }

  private apply(folder: string) {
    return this.host.sessions().filter(s => !s.remote && this.folderOf(s) === folder).map(s => s.id).sort()
  }

  async get(target: { folder: string } | { sessionId: string }) {
    if ('sessionId' in target) {
      const s = this.session(target.sessionId)
      if (s.remote) return { ...this.view(s.cwd), readOnly: true } // a path on another machine
      const folder = this.folderOf(s)
      return this.view(folder, this.stored.get(folder))
    }
    const folder = this.absolute(target.folder)
    return this.view(folder, this.stored.get(folder))
  }

  async update(draft: EnvironmentDraft, opts: { sessionId?: string, validateOnly?: boolean } = {}) {
    this.remote(opts.sessionId)
    const folder = this.absolute(draft.folder)
    const name = (draft.name ?? '').trim()
    if (name.length > 200) throw invalid('name is longer than 200 characters')
    const seen = new Set([folder])
    const additionalFolders = []
    for (const f of draft.additionalFolders ?? []) {
      const path = this.absolute(f.path)
      if (seen.has(path)) continue
      seen.add(path)
      additionalFolders.push({ path, worktree: f.worktree ?? false, baseRef: (f.baseRef ?? '').trim(), exists: false, gitBranch: '' })
    }
    const env = draft.env ?? ''
    const { errors } = parseEnv(env)
    if (errors.length) throw new EnvironmentParseError(errors)
    const next: EnvironmentView = {
      folder, name, worktree: draft.worktree ?? false, baseRef: (draft.baseRef ?? '').trim(), additionalFolders, env,
      exists: !opts.validateOnly, readOnly: false, updatedAt: opts.validateOnly ? '' : new Date().toISOString(), variableNames: []
    }
    if (opts.validateOnly) return { environment: this.view(folder, next), appliedSessionIds: [] }
    this.stored.set(folder, next)
    return { environment: this.view(folder, next), appliedSessionIds: this.apply(folder) }
  }

  async folderInfo(path: string): Promise<FolderInfo> {
    const p = this.absolute(path)
    const repo = this.host.repository(p)
    const exists = this.host.exists(p)
    if (!exists || !repo) return { path: p, exists, repositoryPath: '', currentBranch: '', defaultBranch: '', remoteUrl: '', webUrl: '', repo: '' }
    const [host, ...rest] = repo.repo.split('/')
    return {
      path: p,
      exists,
      repositoryPath: repo.path,
      currentBranch: repo.branch,
      defaultBranch: 'main',
      remoteUrl: `git@${host}:${rest.join('/')}.git`,
      webUrl: `https://${repo.repo}`,
      repo: repo.repo
    }
  }

  async setSessionFolder(sessionId: string, cwd: string) {
    this.remote(sessionId)
    const s = this.session(sessionId)
    const dir = this.absolute(cwd)
    if (!this.host.exists(dir)) throw new Error(`[not_found] ${dir} does not exist`)
    if (s.worktree) throw failed('the session runs in a git worktree; start a new session in the other folder instead')
    return this.host.moveSession(sessionId, dir)
  }

  async addSessionDirectory(sessionId: string, path: string) {
    this.remote(sessionId)
    const s = this.session(sessionId)
    const dir = this.absolute(path)
    if (!this.host.exists(dir)) throw new Error(`[not_found] ${dir} does not exist`)
    const folder = this.folderOf(s)
    const cur = this.stored.get(folder) ?? this.view(folder)
    const additionalFolders = cur.additionalFolders.some(f => f.path === dir) || dir === folder
      ? cur.additionalFolders
      : [...cur.additionalFolders, { path: dir, worktree: false, baseRef: '', exists: true, gitBranch: '' }]
    const next = { ...cur, additionalFolders, exists: true, updatedAt: new Date().toISOString() }
    this.stored.set(folder, next)
    return { environment: this.view(folder, next), appliedSessionIds: this.apply(folder) }
  }
}
