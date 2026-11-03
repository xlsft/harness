// SessionTransport: everything the UI needs from a harness client (harness.agent.v1.AgentService).
// Two implementations: MockTransport (scripted events, the default) and ConnectTransport (connect-web).

import type {
  InitializeResponse,
  ListBranchesResponse,
  ListDirectoryResponse,
  RecentFolder,
  PermissionMode,
  RespondToPermissionRequest_Decision,
  RespondToPlanRequest_Decision,
  RewindRequest_Mode,
  SendUserMessageRequest_Delivery,
  SessionEvent,
  SessionInfo,
  BackgroundTask
} from '~/gen/harness/agent/v1/agent_pb'
import type { Effort } from '~/gen/harness/llm/v1/content_pb'
import type { EffortName, QuestionReply } from '~/lib/session/types'
import type { ProvidersApi } from '~/lib/providers/types'
import type { UsersApi } from '~/lib/users/types'
import type { AccountApi } from '~/lib/account/types'
import type { UsageApi } from '~/lib/usage/types'
import type { TerminalsApi } from '~/lib/terminal/types'
import type { SideChatsApi } from '~/lib/sidechat/types'
import type { EnvironmentApi } from '~/lib/environment/types'
import type { ContextUsageVM } from '~/lib/session/context'
import type { FilesApi } from '~/lib/files/types'

export interface ModelInfo {
  id: string
  // Human name from the catalog; empty when the server has none (the id is shown then).
  displayName?: string
  provider: string
  contextWindow: number
  // e.g. "$0.60/M"; empty when unknown.
  price: string
  offline?: boolean
  // Effort levels the model supports, lowest first; empty when it has none.
  efforts?: EffortName[]
  thinking?: boolean
}

// What a new session starts with (the new-session screen's chips).
export interface NewSessionOptions {
  cwd: string
  model?: string
  effort?: Effort
  permissionMode?: PermissionMode
  // Resume this closed session (from its transcript) instead of starting a new one.
  resumeSessionId?: string
  // With resumeSessionId: start a new session from a copy of that transcript.
  fork?: boolean
  // With fork: copy only the conversation before this user message (UserMessage.uuid, a checkpoint id).
  forkAtMessageId?: string
  // Run in a new git worktree of the repository containing cwd.
  worktree?: { name?: string, branch?: string, baseRef?: string }
}

// Per-user preferences the client honours for every session (stored in the user's synced settings).
export interface UserPreferences {
  // Compact sessions idle for 30 minutes whose context is above half the window, in the background.
  compactIdleSessions: boolean
}

// Per-user session state kept on the harness server; unset fields stay as they are.
export interface SessionPatch {
  pinned?: boolean
  unread?: boolean
  // true also stops the session on the client.
  archived?: boolean
  // A rename; it wins over the AI title.
  title?: string
  // Clears the parent: a sub-session (fork) becomes a top-level session.
  detachFromParent?: boolean
}

// A control message of the stream (no event): the event log id, plus the session as it is now on the one sent
// right after the replay. Heartbeats carry the log id only.
export interface StreamControl {
  logId: string
  session?: SessionInfo
}

export interface SubscribeHandlers {
  onEvent: (event: SessionEvent) => void
  // Control messages; a transport (or client) without them never calls it.
  onControl?: (control: StreamControl) => void
  onError?: (err: unknown) => void
  onEnd?: () => void
}

export interface SessionTransport {
  readonly kind: 'mock' | 'connect'

  initialize(): Promise<InitializeResponse>
  // Sessions of the client, archived ones left out.
  listSessions(): Promise<SessionInfo[]>
  // Starts a session on the client's machine (model: the client's default when empty).
  createSession(opts: NewSessionOptions): Promise<SessionInfo>
  // New-session pickers: recent folders, a directory browser and the branches of a repository.
  listRecentFolders(): Promise<RecentFolder[]>
  listDirectory(path: string): Promise<ListDirectoryResponse>
  listBranches(cwd: string): Promise<ListBranchesResponse>
  // Replays events after `afterSeq`, then streams live ones. Returns an unsubscribe function.
  subscribe(sessionId: string, afterSeq: number, handlers: SubscribeHandlers): () => void

  sendUserMessage(sessionId: string, uuid: string, text: string, delivery: SendUserMessageRequest_Delivery): Promise<void>
  // Takes back a message still in the client's queue; false when it was already injected into the turn.
  removeQueuedMessage(sessionId: string, uuid: string): Promise<boolean>
  // Restores code and/or conversation to the checkpoint of a user message (the session must be idle); the
  // message's text comes back to put in the composer.
  rewind(sessionId: string, checkpointId: string, mode: RewindRequest_Mode): Promise<{ restoredPrompt: string, filesRestored: number }>
  // idle: no turn was running, so nothing was interrupted (an older client always says false).
  interrupt(sessionId: string): Promise<{ idle: boolean }>
  // Stops a live session on the client (its transcript stays, so it can be resumed).
  closeSession?(sessionId: string): Promise<void>
  // Pin, mark unread, archive or rename; returns the session as it is now.
  updateSession(sessionId: string, patch: SessionPatch): Promise<SessionInfo>
  // Deletes the session for good: stops it, removes its transcript and the server copy.
  deleteSession(sessionId: string): Promise<void>
  respondToPermission(sessionId: string, requestId: string, decision: RespondToPermissionRequest_Decision, rule?: string, comment?: string): Promise<void>
  answerQuestion(sessionId: string, requestId: string, reply: QuestionReply): Promise<void>
  respondToPlan(sessionId: string, requestId: string, decision: RespondToPlanRequest_Decision, feedback?: string): Promise<void>

  setModel(sessionId: string, model: string): Promise<void>
  setEffort(sessionId: string, effort: Effort): Promise<void>
  setPermissionMode(sessionId: string, mode: PermissionMode): Promise<void>

  suggestFiles(sessionId: string, query: string, limit: number): Promise<string[]>
  compact(sessionId: string, instructions?: string): Promise<void>
  // What fills the session's context window, by category (local sessions only).
  getContextUsage(sessionId: string): Promise<ContextUsageVM>
  listTasks(sessionId: string): Promise<BackgroundTask[]>
  stopTask(sessionId: string, taskId: string): Promise<void>
  // The composer's shell mode: the client runs the command in the session's shell (no model, no prompt) and
  // streams UserShellStarted / Output / Finished; the model sees the result on its next request. `uuid`
  // deduplicates and becomes the command id. Remote sessions refuse it.
  runShellCommand(sessionId: string, uuid: string, command: string): Promise<string>
  stopShellCommand(sessionId: string, commandId: string): Promise<void>

  // Not part of AgentService yet: the model catalog (provider, context, price) comes from the server in M1+.
  // Connect falls back to InitializeResponse.model_ids.
  listModels(): Promise<ModelInfo[]>

  // Per-user preferences; an update changes only the fields it sets and returns them all.
  getUserPreferences(): Promise<UserPreferences>
  updateUserPreferences(patch: Partial<UserPreferences>): Promise<UserPreferences>

  // LLM providers and the auto-mode classifier for the settings screens (harness.llm.v1.ProviderService,
  // forwarded to the server by the client).
  providers(): ProvidersApi

  // User administration and LDAP for admins (harness.auth.v1.AdminService, forwarded to the server by the client).
  users(): UsersApi

  // The signed-in user's own profile, avatar, password and devices (harness.auth.v1.DeviceAuthService account
  // methods, forwarded to the server by the client).
  account(): AccountApi

  // LLM usage reports for the Usage tab (harness.llm.v1.UsageService, forwarded to the server by the client).
  usage(): UsageApi

  // Shells in a PTY on the client's machine for the terminal pane (create / list / kill, and the interactive
  // TerminalConnection over the client's terminal WebSocket).
  terminals(): TerminalsApi

  // Side chats next to a session: read-only conversations that see the main session but never change it
  // (list / create / ask with streamed answers / stop), stored by the client next to the transcript.
  sideChats(): SideChatsApi

  // Session environments, folder facts and folder changes (AgentService GetEnvironment and friends). The web
  // link of a session is sessionWebUrl(initialize().webUrl, id) from ~/lib/environment/types.
  environment(): EnvironmentApi

  // The files pane: a session's working directories, listings with git status, file contents within limits
  // (AgentService ListFileRoots / ListFiles / ReadFile / GetGitStatus, ~/lib/files).
  files(): FilesApi
}
