// View model of one session, built from harness.agent.v1 SessionEvents by the reducer (./reducer.ts).
// It is plain data (no protobuf messages) so it can live in a Pinia store and be rendered directly.
import type { BrowserActionVM } from '~/lib/browser/types'
import type { AcpSessionState } from '~/gen/harness/agent/v1/acp_pb'

export type Status = 'idle' | 'working' | 'needs_input' | 'waiting_for_connection' | 'error' | 'ended'

export type PermissionModeName = 'default' | 'accept_edits' | 'plan' | 'dont_ask' | 'bypass' | 'auto'

export type EffortName = 'low' | 'medium' | 'high' | 'xhigh' | 'max'

export type UsageLimitStateName = 'near' | 'wrap_up' | 'reached' | 'cleared'

// The provider behind the session is close to (or past) its usage limit (SessionEvent.usage_limit, also
// SessionInfo.usage_limit). Numbers are empty / 0 when unknown or hidden from the user by the server's policies.
export interface UsageLimitVM {
  state: UsageLimitStateName
  providerName: string
  // "5h", "weekly", "credits", "monthly budget", ...
  windowLabel: string
  // 0..100.
  usedPercent: number
  // RFC 3339; empty when unknown.
  resetsAt: string
  // e.g. "$0.40 left".
  balanceText: string
  // The last turn ended after the agent was asked to wrap up ("Wrapped up past your usage limit").
  wrappedUp: boolean
  // The last turn was cut off by the limit and nothing has continued it yet (also on 'cleared').
  interrupted: boolean
  // "Continue when limits return" is on for the session.
  autoContinue: boolean
}

export interface Usage {
  inputTokens: number
  outputTokens: number
  cacheReadTokens: number
  cacheCreationTokens: number
}

export interface Attachment {
  kind: 'image' | 'document'
  name: string
  mediaType: string
  size: number
}

export interface UserItem {
  kind: 'user'
  id: string
  text: string
  attachments: Attachment[]
  // When the agent took the message (the event's time; 0 when unknown).
  at?: number
  // Frontend ids (SendUserMessageRequest.uuid) of the messages this entry carries.
  clientIds?: string[]
}

export interface AssistantBlock {
  type: 'text' | 'thinking'
  text: string
  // Thinking only: how long the model thought, when known (from delta timestamps).
  durationMs?: number
  // Content-block index while streaming (AssistantDelta.index).
  idx?: number
}

export interface AssistantItem {
  kind: 'assistant'
  id: string
  model: string
  blocks: AssistantBlock[]
  streaming: boolean
  // tool_use ids in this message: two or more render as one compact group.
  toolUseIds: string[]
}

export type ToolStatus = 'running' | 'ok' | 'error' | 'interrupted'

export interface SubagentStep {
  tool: string
  target: string
  meta?: string
  status: 'done' | 'running' | 'waiting' | 'failed'
}

// Why a Bash command runs in the background (harness.agent.v1.BackgroundReason): the user moved it, it was moved
// so a queued message could be delivered now, the agent started it with run_in_background, or it hit its timeout.
export type BackgroundReasonName = 'user' | 'queued_message' | 'agent' | 'auto'

export interface ToolItem {
  kind: 'tool'
  id: string
  name: string
  input: Record<string, unknown>
  summary: string
  status: ToolStatus
  // Text of the tool_result blocks.
  output: string
  // Parsed structured_result_json (Claude Code's toolUseResult shape).
  result: Record<string, unknown> | null
  resultSummary: string
  durationMs: number
  startedAt: number
  // The assistant message that requested it, and whether it shares that message with other calls.
  messageId: string
  grouped: boolean
  // Short model-written label of the call (Bash for now; ToolTitle), a lowercase noun phrase shown as
  // "Run <title>" / "Ran <title>". Absent until it arrives, and for calls that never get one.
  title?: string
  // Auto mode verdict for this call, when the classifier decided it.
  autoDecision?: { decision: string, reason: string, provider: string }
  // Set when the Bash command runs on as background task `taskId` (ToolCallFinished.background); the card shows
  // a short note about it inside the output.
  background?: { taskId: string, reason: BackgroundReasonName }
  // Subagents (Task / Agent tool): progress reported by the client.
  subagent?: { steps: SubagentStep[], tokens: number, toolCalls: number, model: string, active: number }
}

export type PermissionState = 'pending' | 'allowed' | 'allowed_always' | 'denied'

export interface PermissionItem {
  kind: 'permission'
  id: string
  toolUseId: string
  toolName: string
  input: Record<string, unknown>
  suggestedRules: string[]
  reason: string
  state: PermissionState
  resolvedBy: string
}

export interface QuestionOptionVM {
  label: string
  description: string
  // Suggested by the model: shown first and highlighted.
  recommended: boolean
}

// single: one option; multi: min..max options; text: free text only; yes_no: the options Yes / No.
export type QuestionType = 'single' | 'multi' | 'text' | 'yes_no'

export interface QuestionItemVM {
  question: string
  header: string
  type: QuestionType
  options: QuestionOptionVM[]
  // multi: bounds of the number of choices, "Other" counting as one.
  min: number
  max: number
  // Offers "Other…" with free text (never for text questions).
  allowOther: boolean
}

// One answer per question: chosen labels, the Other / text answer, or skipped.
export interface QuestionAnswerVM {
  selected: string[]
  text: string
  skipped: boolean
}

// pending → answered (in the card), skipped (whole card), message (answered with chat messages) or cancelled
// (Stop, session closed, client restarted).
export type QuestionState = 'pending' | 'answered' | 'skipped' | 'message' | 'cancelled'

export interface QuestionItem {
  kind: 'question'
  id: string
  toolUseId: string
  items: QuestionItemVM[]
  state: QuestionState
  resolvedBy: string
  // state 'answered': one entry per question.
  answers: QuestionAnswerVM[]
  // state 'message': the chat text used as the answer.
  message: string
}

// How the user resolves a pending question (AnswerQuestion). With 'message' the queued chat messages (all of
// them when messageUuids is empty) become the free-form answer.
export type QuestionReply = { outcome: 'answered', answers: QuestionAnswerVM[] } | { outcome: 'skipped' } | { outcome: 'message', messageUuids?: string[] }

export type PlanState = 'pending' | 'approved_accept_edits' | 'approved_manual' | 'keep_planning'

export interface PlanItem {
  kind: 'plan'
  id: string
  plan: string
  planFilePath: string
  state: PlanState
  resolvedBy: string
}

export interface TodoVM {
  content: string
  activeForm: string
  status: 'pending' | 'in_progress' | 'completed'
}

export interface TodosItem {
  kind: 'todos'
  id: string
  todos: TodoVM[]
}

export interface NoticeItem {
  kind: 'notice'
  id: string
  level: 'info' | 'warning' | 'error'
  noticeKind: string
  text: string
  // A note from another session: the session it came from.
  session?: { id: string, name: string, title: string }
}

// A session the model proposed (SessionStart, own_initiative): the state of its card.
export interface SessionProposalVM {
  id: string
  state: 'pending' | 'started' | 'dismissed' | 'expired'
  expiresAt: string
  sessionId: string
  sessionName: string
}

export interface CompactItem {
  kind: 'compact'
  id: string
  trigger: 'manual' | 'auto' | 'rewind' | 'idle'
  tokensBefore: number
  tokensAfter: number
  // What the conversation continues from ('' when the client did not send it).
  summary: string
}

export interface TurnItem {
  kind: 'turn'
  id: string
  result: 'success' | 'interrupted' | 'max_turns' | 'error'
  model: string
  usage: Usage
  costUsd: number
  durationMs: number
  errorMessage: string
}

export type TaskState = 'running' | 'done' | 'failed' | 'stopped'

export interface TaskVM {
  id: string
  toolUseId: string
  command: string
  description: string
  outputPath: string
  state: TaskState
  exitCode: number
  durationMs: number
  startedAt: number
  tail: string
}

// A background task in the transcript: where it started, and (`end`) where it finished, so the history reads in
// order. `taskId` is the task's id (TaskVM); the end item's own id is "<task id>:end".
export interface TaskItem {
  kind: 'task'
  id: string
  taskId?: string
  end?: boolean
}

// A command the user ran in the composer's shell mode (RunShellCommand): run by the client, not the model.
export type UserShellState = 'running' | 'ok' | 'error' | 'stopped' | 'timeout' | 'failed'

export interface UserShellItem {
  kind: 'shell'
  id: string
  command: string
  cwd: string
  state: UserShellState
  // -1 when it did not exit by itself.
  exitCode: number
  durationMs: number
  startedAt: number
  // While running: the streamed output (stdout and stderr in arrival order). Once finished: stdout.
  output: string
  stderr: string
  truncated: boolean
  error: string
}

export interface RewoundItem {
  kind: 'rewound'
  id: string
  filesRestored: number
  filesSkipped: number
}

export type TranscriptItem
  = | UserItem
    | AssistantItem
    | ToolItem
    | PermissionItem
    | QuestionItem
    | PlanItem
    | TodosItem
    | NoticeItem
    | CompactItem
    | TurnItem
    | TaskItem
    | RewoundItem
    | UserShellItem

export interface SessionMeta {
  id: string
  title: string
  cwd: string
  gitBranch: string
  model: string
  permissionMode: PermissionModeName
  status: Status
  updatedAt: string
  repo: string
  // Set when the session runs in a worktree the client created: its worktree, and the main checkout.
  worktree: { path: string, branch: string, baseRef: string, repositoryPath: string } | null
  deviceName: string
  // A session of another machine of the user (listed from the harness server): read-only here unless it is
  // remote-controllable (open on its machine, which is online). deviceOnline is null when presence is unknown.
  remote: boolean
  deviceOnline: boolean | null
  remoteControllable: boolean
  additions: number
  deletions: number
  filesChanged: number
  pullRequest: { number: number, url: string, state: 'open' | 'draft' | 'merged' | 'closed' } | null
  // Per-user state kept on the harness server (title includes a rename).
  pinned: boolean
  unread: boolean
  archived: boolean
  // The session this one was forked from (a sub-session listed under it); empty for top-level sessions.
  parentId: string
  // "Continue when limits return" is on for the session (setAutoContinue).
  autoContinue: boolean
  // Started without a folder: cwd is a private directory the client made for it (shown as "No folder", listed
  // under Chats, no repository).
  noFolder: boolean
  // Remote control (the session relay switch): mirrored to the harness server right now; whether it can be turned
  // on here, and if not, why (shown on the switch).
  relayEnabled: boolean
  relayAvailable: boolean
  relayReason: string
  // "harness", or "acp:<agent id>" for a session an ACP agent runs (SessionInfo.engine).
  engine: string
  // What the engine cannot do (SessionInfo.unsupported_features), e.g. 'rewind', 'fork', 'compaction'.
  unsupported: string[]
  // An ACP session's agent state: modes, sign-in, context, subscription limits (local to this computer).
  acp: AcpSessionState | null
}

export interface QueuedMessage {
  id: string
  text: string
  // When the client queued it (ms; 0 when unknown).
  at?: number
  // The usage limit holds it: it goes out by itself when the limit resets or the model moves to a provider that is fine.
  held?: boolean
}

// What the Show tool opens: a file (lines 1-based, inclusive, 0 when not given), the session diff (optionally one
// file) or a web page.
export type ShowTarget
  = | { kind: 'file', path: string, lineStart: number, lineEnd: number }
    | { kind: 'diff', path: string }
    | { kind: 'url', url: string }

// The latest ShowRequested of the current turn; id is the Show tool call's id.
export interface ShowRequest {
  id: string
  target: ShowTarget
}

// BrowserAction events of the current turn: the action and its running number in the turn.
export interface BrowserActionOverlay extends BrowserActionVM {
  step: number
}

export interface SessionState {
  meta: SessionMeta
  // The client's event log that lastSeq counts in (SubscribeResponse.log_id); empty until a stream said it.
  logId: string
  lastSeq: number
  items: TranscriptItem[]
  // Index of items by id for O(1) updates (tool calls, permissions, deltas).
  index: Record<string, number>
  tasks: Record<string, TaskVM>
  taskOrder: string[]
  todos: TodoVM[]
  // The session's proposals, by the SessionStart call that made them.
  proposals: Record<string, SessionProposalVM>
  // The client's queue (QueueChanged).
  queue: QueuedMessage[]
  // Read receipts: frontend message id → the user item that carries it (the agent took the message).
  delivered: Record<string, string>
  // Current turn.
  turnStartedAt: number
  turnModel: string
  // Output tokens generated in the current turn (the composer's status line).
  turnTokens: number
  // Running totals.
  usage: Usage
  costUsd: number
  contextTokens: number
  // Pending requests that block the turn.
  pendingPermission: string | null
  pendingQuestion: string | null
  pendingPlan: string | null
  // The latest Show request not yet followed by a user message; the UI opens it once the session is open.
  showRequest: ShowRequest | null
  // The latest usage-limit state of the session's provider; undefined until one was reported.
  usageLimit?: UsageLimitVM
  // The history is being replayed (first open): the transcript waits for it.
  loading?: boolean
  // The permission mode plan mode was entered from (approving a plan returns to it).
  prePlanMode?: PermissionModeName
  // The agent's latest browser action of this turn, for the Browser pane's overlay; null when the agent is not
  // acting in the browser (cleared when the turn ends).
  browserAction: BrowserActionOverlay | null
  // Browser tool calls started in the current turn (the overlay's "step N").
  browserSteps: number
  endedReason: string
  // Internal: thinking start per message (for "Thought for 12s").
  thinkingStartedAt: Record<string, number>
  // Internal: which assistant message asked for each tool call, and how many calls it made.
  toolOwners: Record<string, { messageId: string, count: number }>
  // Internal: ToolTitle labels that arrived before their tool call's item, applied when it appears.
  toolTitles: Record<string, string>
}
