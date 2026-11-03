// EnvironmentApi: a session's "Environment" (harness.agent.v1.AgentService GetEnvironment / UpdateEnvironment /
// GetFolderInfo / SetSessionFolder / AddSessionDirectory). An environment belongs to a primary folder (project)
// and is stored by the client of that machine: a name, additional folders, new-session defaults (worktree, base
// branch) and environment variables in .env syntax (encrypted at rest by the client). Two implementations:
// connect.ts (the client) and mock.ts (in memory, for the mock transport).

import type { SessionInfo } from '~/gen/harness/agent/v1/agent_pb'

export interface EnvironmentFolder {
  // Absolute path.
  path: string
  // New-session defaults for this folder (stored; the client applies additional folders as plain working
  // directories today).
  worktree: boolean
  // Branch new worktrees start from; '' means the repository's current branch.
  baseRef: string
  // Output only: the folder exists on the client's machine (missing folders are not applied) and its current
  // branch ('' outside a repository).
  exists: boolean
  gitBranch: string
}

export interface EnvironmentView {
  // Absolute path of the primary folder: the key.
  folder: string
  // '' means the folder's name.
  name: string
  // New-session defaults of the primary folder (prefill the new-session form's worktree option).
  worktree: boolean
  baseRef: string
  additionalFolders: EnvironmentFolder[]
  // The .env text as the user wrote it.
  env: string
  // Stored on the client; false: defaults.
  exists: boolean
  // A remote session's environment: lives on its machine, cannot be read or edited here (everything else empty).
  readOnly: boolean
  // RFC 3339; '' when not stored.
  updatedAt: string
  // Names of the variables env defines, in order.
  variableNames: string[]
}

// What the edit modal sends: a full replacement of the folder's environment.
export interface EnvironmentDraft {
  folder: string
  name?: string
  worktree?: boolean
  baseRef?: string
  additionalFolders?: { path: string, worktree?: boolean, baseRef?: string }[]
  env?: string
}

export interface EnvironmentUpdate {
  environment: EnvironmentView
  // Open sessions in the folder it was applied to (live: working directories and variables, from their next
  // tool call; worktree / base branch only affect new sessions).
  appliedSessionIds: string[]
}

export interface FolderInfo {
  path: string
  exists: boolean
  // '' outside a repository.
  repositoryPath: string
  currentBranch: string
  defaultBranch: string
  // The remote as configured, and as a browsable URL (https://github.com/owner/repo); '' when none.
  remoteUrl: string
  webUrl: string
  // Normalized remote, e.g. 'github.com/owner/repo'.
  repo: string
}

// A line of the .env text that does not parse (1-based).
export interface EnvLineError {
  line: number
  message: string
}

// Thrown by update() for .env text that does not parse; nothing was stored.
export class EnvironmentParseError extends Error {
  constructor(readonly errors: EnvLineError[]) {
    super(errors.map(e => `line ${e.line}: ${e.message}`).join('; ') || 'invalid environment variables')
    this.name = 'EnvironmentParseError'
  }
}

export interface EnvironmentApi {
  // The environment of a folder, or of a session's primary folder (a remote session: readOnly).
  get(target: { folder: string } | { sessionId: string }): Promise<EnvironmentView>
  // Replaces the folder's environment and applies it to the folder's open sessions. Invalid .env text rejects
  // with EnvironmentParseError. sessionId: the session the edit comes from (a remote one is refused).
  // validateOnly: check without storing.
  update(draft: EnvironmentDraft, opts?: { sessionId?: string, validateOnly?: boolean }): Promise<EnvironmentUpdate>
  // Git facts of a folder for its menu ("Open repository on GitHub" uses webUrl).
  folderInfo(path: string): Promise<FolderInfo>
  // Moves an idle session to another primary folder; the conversation is kept and the tools restart there.
  // Subscribers get SessionEnded (reason 'folder_changed') and must subscribe again from seq 0.
  setSessionFolder(sessionId: string, cwd: string): Promise<SessionInfo>
  // Adds a working directory to the session's environment (stored) and applies it to the folder's open sessions.
  addSessionDirectory(sessionId: string, path: string): Promise<EnvironmentUpdate>
}

// SessionEnded reason of a session that moved to another folder (subscribe again).
export const FOLDER_CHANGED = 'folder_changed'

// The web link of a session ("Open on the web" / "Copy link"): InitializeResponse.webUrl + '?session=<id>';
// '' when the client reports no web UI.
export function sessionWebUrl(webUrl: string, sessionId: string): string {
  if (!webUrl || !sessionId) return ''
  return `${webUrl.replace(/\/+$/, '')}/?session=${encodeURIComponent(sessionId)}`
}
