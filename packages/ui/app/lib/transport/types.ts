// SessionTransport: everything the UI needs from a harness client (harness.agent.v1.AgentService).
// Two implementations: MockTransport (scripted events, the default) and ConnectTransport (connect-web).

import type { ObjectTicket } from '~/lib/session/objects'
import type { ComputerPermissionVM, ComputerProfileVM, ComputerTierName, EffortName, QuestionReply } from '~/lib/session/types'
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
import type { SubagentTranscriptItem } from '~/gen/harness/agent/v1/subagents_pb'
import type { ProvidersApi } from '~/lib/providers/types'
import type { UsersApi } from '~/lib/users/types'
import type { AccountApi } from '~/lib/account/types'
import type { UsageApi } from '~/lib/usage/types'
import type { TerminalsApi } from '~/lib/terminal/types'
import type { SideChatsApi } from '~/lib/sidechat/types'
import type { EnvironmentApi } from '~/lib/environment/types'
import type { ContextUsageVM } from '~/lib/session/context'
import type { FilesApi } from '~/lib/files/types'
import type { DiffBaseChoice, DiffBases, SessionDiff, SessionFileDiff } from '~/lib/files/sessionDiff'
import type { SpeechApi } from '~/lib/speech/types'
import type { LocalModelsApi } from './localModels'
import type { LimitsApi } from '~/lib/limits/types'
import type { AcpApi } from '~/lib/acp/types'
import type { BrowserApi } from '~/lib/browser/types'

// Computer use on this device (GetComputerUseStatus).
export interface ComputerUseStatusVM {
  enabled: boolean
  supported: boolean
  platform: string
  unavailableReason: string
  permissions: ComputerPermissionVM[]
  activeProfileId: string
  profiles: ComputerProfileVM[]
  blockEntries: { id: string, label: string, warning: string }[]
}

// An app of this machine for the profile editor (ListComputerUseApps).
export interface ComputerUseAppVM {
  id: string
  name: string
  tier: ComputerTierName
}

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
  // A model of a local provider on the client's machine (id "local:<provider>/<name>"): free, per device.
  local?: boolean
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
  // The session engine: omitted or 'harness' for the harness agent loop, 'acp:<agent id>' for an installed ACP agent
  // (AcpAgent.engine). A resume keeps the session's recorded engine. On an agent, `model` is one of AcpAgent.models.
  engine?: string
  // On an agent: the mode to start in (AcpAgent.modes).
  acpModeId?: string
}

// Per-user preferences the client honours for every session (stored in the user's synced settings).
export interface UserPreferences {
  // Compact sessions idle for 30 minutes whose context is above half the window, in the background.
  compactIdleSessions: boolean
  // How many times per hour a watched pull request may wake a session (1 to 240; the client validates).
  prMonitorWakesPerHour: number
  // How deep subagents nest (1 to 5) and how many of a session run at once (1 to 32); read when an agent starts.
  subagentMaxDepth: number
  subagentMaxRunning: number
}

// Preferences of this device, kept by the client on this machine and never synced.
export interface DeviceSettings {
  // 'system', 'light' or 'dark'; empty until something chose on this device.
  theme: string
  notifications: boolean
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
  // Computer use (device-local). The status is only read, which never makes the system prompt.
  getComputerUseStatus(): Promise<ComputerUseStatusVM>
  setComputerUseEnabled(enabled: boolean): Promise<ComputerUseStatusVM>
  // Picks the security profile; with `custom`, replaces the custom profile too.
  setComputerUseProfile(activeProfileId: string, custom?: ComputerProfileVM): Promise<ComputerUseStatusVM>
  listComputerUseApps(): Promise<ComputerUseAppVM[]>
  respondToComputerAccess(sessionId: string, requestId: string, approvedAppIds: string[]): Promise<void>
  stopComputerUse(sessionId: string): Promise<void>
  respondToPlan(sessionId: string, requestId: string, decision: RespondToPlanRequest_Decision, feedback?: string): Promise<void>

  // engine: the engine the picked model belongs to; omitted means the session's own. The client refuses any other.
  setModel(sessionId: string, model: string, engine?: string): Promise<void>
  setEffort(sessionId: string, effort: Effort): Promise<void>
  setPermissionMode(sessionId: string, mode: PermissionMode): Promise<void>
  // Turns remote control (mirroring to the harness server) on or off for an open local session; returns the
  // session as it is now (relayEnabled). Refused when remote control is not available.
  setSessionRelay(sessionId: string, enabled: boolean): Promise<SessionInfo>
  // "Continue when limits return": when the limit resets and the last turn was cut off by it, the client continues the
  // work by itself. Off by default; held messages go out at the reset either way.
  setAutoContinue(sessionId: string, enabled: boolean): Promise<void>
  // The user's click on a proposal's card; a SessionProposal event follows.
  resolveSessionProposal(sessionId: string, proposalId: string, action: 'start' | 'dismiss'): Promise<void>
  // The auto-fix switch: the client watches the session's pull request and wakes the session.
  setPrMonitor(sessionId: string, enabled: boolean): Promise<void>

  suggestFiles(sessionId: string, query: string, limit: number): Promise<string[]>
  compact(sessionId: string, instructions?: string): Promise<void>
  // What fills the session's context window, by category (local sessions only).
  getContextUsage(sessionId: string): Promise<ContextUsageVM>
  listTasks(sessionId: string): Promise<BackgroundTask[]>
  stopTask(sessionId: string, taskId: string): Promise<void>
  stopSubagent(sessionId: string, agentId: string): Promise<void>
  subagentTranscript(sessionId: string, agentId: string): Promise<SubagentTranscriptItem[]>
  // Moves the running foreground Bash command of a tool call to the background without stopping it (the "Run in
  // background" button: reason 'user'; delivering a queued message now: 'queued_message'). The call then
  // finishes with ToolCallFinished.background and a TaskStarted follows. false: that call runs no foreground
  // command (anymore).
  backgroundToolCall(sessionId: string, toolUseId: string, reason: 'user' | 'queued_message'): Promise<boolean>
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

  // This device's preferences; an update changes only the fields it sets and returns them all.
  getDeviceSettings(): Promise<DeviceSettings>
  updateDeviceSettings(patch: Partial<DeviceSettings>): Promise<DeviceSettings>
  // "Keep computer awake" for a session; SessionInfo.keep_awake reports it.
  setKeepAwake(sessionId: string, enabled: boolean): Promise<void>

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

  // The session's changes as they are on disk, shell commands' edits included (AgentService GetSessionDiff,
  // ~/lib/files/sessionDiff). Fails for a session of another machine. With ifNoneMatch, an unchanged state comes
  // back as notModified. base picks what it compares with, for this answer alone (unset: 'all'). A large answer has
  // no hunks; refreshAfterMs says when to ask again.
  sessionDiff(sessionId: string, opts?: { ifNoneMatch?: string, base?: DiffBaseChoice }): Promise<SessionDiff>
  // One changed file with its hunks, for a large diff, against the same base (AgentService GetSessionFileDiff). Fails
  // with not_found for a file without changes against it.
  sessionFileDiff(sessionId: string, path: string, base?: DiffBaseChoice): Promise<SessionFileDiff>
  // What sessionDiff can compare with: branches, the default base, the commits since it (AgentService ListDiffBases).
  listDiffBases(sessionId: string): Promise<DiffBases>

  // Whether the user is looking at a session in this frontend (AgentService UpdateViewerPresence); renewed while focused.
  updateViewerPresence(sessionId: string, viewerId: string, focused: boolean): Promise<void>
  // The bytes of a file in the session's scope, for SendUserFile cards (AgentService ReadSentFile). Fails for a session
  // of another machine.
  readSentFile(sessionId: string, path: string): Promise<{ bytes: Uint8Array<ArrayBuffer>, mime: string, sha256: string }>
  // A ticket for reading the object store through the client (lib/session/objects.ts).
  createObjectTicket(): Promise<ObjectTicket>

  // Dictation and "Read aloud" (~/lib/speech): providers (local engines of the client's machine and the server's
  // global providers), the synced / device settings, transcribe, streamed synthesis, local model downloads, and the
  // admins' global provider keys.
  speech(): SpeechApi
  // Local LLM providers of the client's machine (Ollama, LM Studio, llama.cpp, MLX, vLLM, Jan, custom
  // OpenAI-compatible endpoints): detection, models, Ollama pulls. Per device, never synced (./localModels.ts).
  localModels(): LocalModelsApi
  // Provider balances, monthly spend and rate-limit windows (AgentService.GetProviderLimits; budgets and the admin
  // visibility policies forwarded to the server by the client, ~/lib/limits). Filtered by the server.
  limits(): LimitsApi
  // ACP agents as session engines: the registry, installs, sign-in and agent modes (~/lib/acp). Device-local.
  acp(): AcpApi
  // The session's real browser, shared with the agent's Browser* tools: availability and the Chrome for Testing
  // download, and the live view (BrowserConnection: screencast frames, tab state, agent actions; navigation and
  // input go back over it). See ~/lib/browser.
  browser(): BrowserApi
}
