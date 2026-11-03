// View model of one session, built from harness.agent.v1 SessionEvents by the reducer (./reducer.ts).
// It is plain data (no protobuf messages) so it can live in a Pinia store and be rendered directly.

export type Status = 'idle' | 'working' | 'needs_input' | 'waiting_for_connection' | 'error' | 'ended'

export type PermissionModeName = 'default' | 'accept_edits' | 'plan' | 'dont_ask' | 'bypass' | 'auto'

export type EffortName = 'low' | 'medium' | 'high' | 'xhigh' | 'max'

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
  // Auto mode verdict for this call, when the classifier decided it.
  autoDecision?: { decision: string, reason: string, provider: string }
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
}

export interface CompactItem {
  kind: 'compact'
  id: string
  trigger: 'manual' | 'auto' | 'rewind' | 'idle'
  tokensBefore: number
  tokensAfter: number
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

export interface TaskItem {
  kind: 'task'
  id: string
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
}

export interface QueuedMessage {
  id: string
  text: string
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

export interface SessionState {
  meta: SessionMeta
  lastSeq: number
  items: TranscriptItem[]
  // Index of items by id for O(1) updates (tool calls, permissions, deltas).
  index: Record<string, number>
  tasks: Record<string, TaskVM>
  taskOrder: string[]
  todos: TodoVM[]
  queue: QueuedMessage[]
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
  endedReason: string
  // Internal: thinking start per message (for "Thought for 12s").
  thinkingStartedAt: Record<string, number>
  // Internal: which assistant message asked for each tool call, and how many calls it made.
  toolOwners: Record<string, { messageId: string, count: number }>
}
