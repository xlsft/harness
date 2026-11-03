// Folds harness.agent.v1 SessionEvents into a SessionState (./types.ts).
//
// Rules (see proto/harness/agent/v1/agent.proto):
// - durable events carry a seq; anything at or below lastSeq is a replay duplicate and is skipped;
// - AssistantDelta is ephemeral (it reuses the seq of the previous durable event), so it is never deduplicated;
//   deltas are merged into a streaming assistant item, which the final AssistantMessage replaces;
// - ToolCallStarted / ToolCallFinished are paired by tool_use_id;
// - PermissionRequest / Question / PlanProposed cards are resolved by the matching *Resolved event.

import type { ContentBlock, Usage as PbUsage } from '~/gen/harness/llm/v1/content_pb'
import type { AssistantDelta, SessionEvent, SessionInfo, ShowRequested, UserMessage } from '~/gen/harness/agent/v1/agent_pb'
import {
  Compacted_Trigger,
  Notice_Level,
  PermissionMode,
  PullRequest_State,
  RespondToPermissionRequest_Decision,
  RespondToPlanRequest_Decision,
  RewindRequest_Mode,
  SessionStatus,
  ShowRequested_Kind,
  TaskFinished_Reason,
  Todo_Status,
  TurnFinished_Result
} from '~/gen/harness/agent/v1/agent_pb'
import type {
  AssistantItem,
  Attachment,
  CompactItem,
  PermissionModeName,
  SessionMeta,
  SessionState,
  ShowTarget,
  Status,
  ToolItem,
  TranscriptItem,
  Usage
} from './types'
import { answerVM, questionItemVM, questionState } from './question'

const COMPACT_TRIGGER: Partial<Record<Compacted_Trigger, CompactItem['trigger']>> = {
  [Compacted_Trigger.MANUAL]: 'manual',
  [Compacted_Trigger.AUTO]: 'auto',
  [Compacted_Trigger.REWIND]: 'rewind',
  [Compacted_Trigger.IDLE]: 'idle'
}

export function emptyUsage(): Usage {
  return { inputTokens: 0, outputTokens: 0, cacheReadTokens: 0, cacheCreationTokens: 0 }
}

export function createSessionState(id: string, meta?: Partial<SessionMeta>): SessionState {
  return {
    meta: {
      id,
      title: '',
      cwd: '',
      gitBranch: '',
      model: '',
      permissionMode: 'default',
      status: 'idle',
      updatedAt: '',
      repo: '',
      worktree: null,
      deviceName: '',
      remote: false,
      deviceOnline: null,
      remoteControllable: false,
      additions: 0,
      deletions: 0,
      filesChanged: 0,
      pullRequest: null,
      pinned: false,
      unread: false,
      archived: false,
      parentId: '',
      ...meta
    },
    logId: '',
    lastSeq: 0,
    items: [],
    index: {},
    tasks: {},
    taskOrder: [],
    todos: [],
    queue: [],
    delivered: {},
    turnStartedAt: 0,
    turnModel: '',
    turnTokens: 0,
    usage: emptyUsage(),
    costUsd: 0,
    contextTokens: 0,
    pendingPermission: null,
    pendingQuestion: null,
    pendingPlan: null,
    showRequest: null,
    endedReason: '',
    thinkingStartedAt: {},
    toolOwners: {}
  }
}

// ── conversions ──────────────────────────────────────────────────────────────

export function permissionModeName(mode: PermissionMode): PermissionModeName {
  switch (mode) {
    case PermissionMode.ACCEPT_EDITS: return 'accept_edits'
    case PermissionMode.PLAN: return 'plan'
    case PermissionMode.DONT_ASK: return 'dont_ask'
    case PermissionMode.BYPASS_PERMISSIONS: return 'bypass'
    case PermissionMode.AUTO: return 'auto'
    default: return 'default'
  }
}

export function permissionModePb(mode: PermissionModeName): PermissionMode {
  switch (mode) {
    case 'accept_edits': return PermissionMode.ACCEPT_EDITS
    case 'plan': return PermissionMode.PLAN
    case 'dont_ask': return PermissionMode.DONT_ASK
    case 'bypass': return PermissionMode.BYPASS_PERMISSIONS
    case 'auto': return PermissionMode.AUTO
    default: return PermissionMode.DEFAULT
  }
}

export function statusName(status: SessionStatus): Status {
  switch (status) {
    case SessionStatus.WORKING: return 'working'
    case SessionStatus.NEEDS_INPUT: return 'needs_input'
    case SessionStatus.WAITING_FOR_CONNECTION: return 'waiting_for_connection'
    case SessionStatus.ERROR: return 'error'
    default: return 'idle'
  }
}

export function sessionMeta(info: SessionInfo): SessionMeta {
  const pr = info.pullRequest
  const prState = pr?.state === PullRequest_State.DRAFT
    ? 'draft'
    : pr?.state === PullRequest_State.MERGED ? 'merged' : pr?.state === PullRequest_State.CLOSED ? 'closed' : 'open'
  return {
    id: info.id,
    // Sessions that never got a title (headless runs) still need a label in the list.
    title: info.title || 'Untitled session',
    cwd: info.cwd,
    gitBranch: info.gitBranch,
    model: info.model,
    permissionMode: permissionModeName(info.permissionMode),
    status: statusName(info.status),
    updatedAt: info.updatedAt,
    repo: info.repo,
    worktree: info.worktree ? { path: info.worktree.path, branch: info.worktree.branch, baseRef: info.worktree.baseRef, repositoryPath: info.worktree.repositoryPath } : null,
    deviceName: info.deviceName,
    remote: info.remote,
    deviceOnline: info.deviceOnline ?? null,
    remoteControllable: info.remoteControllable,
    additions: info.diffStats?.additions ?? 0,
    deletions: info.diffStats?.deletions ?? 0,
    filesChanged: info.diffStats?.filesChanged ?? 0,
    pullRequest: pr ? { number: pr.number, url: pr.url, state: prState } : null,
    pinned: info.pinned,
    unread: info.unread,
    archived: info.archived,
    parentId: info.parentSessionId
  }
}

function usageOf(u: PbUsage | undefined): Usage {
  if (!u) return emptyUsage()
  return {
    inputTokens: Number(u.inputTokens),
    outputTokens: Number(u.outputTokens),
    cacheReadTokens: Number(u.cacheReadInputTokens),
    cacheCreationTokens: Number(u.cacheCreationInputTokens)
  }
}

export function textOf(blocks: ContentBlock[]): string {
  const parts: string[] = []
  for (const b of blocks) {
    if (b.block.case === 'text') parts.push(b.block.value.text)
    else if (b.block.case === 'toolResult') parts.push(textOf(b.block.value.content))
  }
  return parts.join('\n')
}

function parseJson(json: string): Record<string, unknown> | null {
  if (!json) return null
  try {
    const v = JSON.parse(json)
    return v && typeof v === 'object' ? v as Record<string, unknown> : null
  } catch {
    return null
  }
}

function showTarget(r: ShowRequested): ShowTarget | null {
  switch (r.kind) {
    case ShowRequested_Kind.FILE: return r.path ? { kind: 'file', path: r.path, lineStart: r.lineStart, lineEnd: r.lineEnd } : null
    case ShowRequested_Kind.DIFF: return { kind: 'diff', path: r.path }
    case ShowRequested_Kind.URL: return r.url ? { kind: 'url', url: r.url } : null
    default: return null
  }
}

function userItem(id: string, msg: UserMessage, at: number): TranscriptItem {
  const attachments = msg.content.flatMap((b): Attachment[] => {
    if (b.block.case === 'image') return [{ kind: 'image' as const, name: 'image', mediaType: b.block.value.mediaType, size: b.block.value.data.length }]
    if (b.block.case === 'document') return [{ kind: 'document' as const, name: b.block.value.title || 'document', mediaType: b.block.value.mediaType, size: b.block.value.data.length }]
    return []
  })
  return { kind: 'user' as const, id, text: textOf(msg.content), attachments, at, clientIds: [...msg.clientUuids] }
}

// ── item helpers ─────────────────────────────────────────────────────────────

function push(s: SessionState, item: TranscriptItem) {
  s.index[item.kind + ':' + item.id] = s.items.length
  s.items.push(item)
}

function find<K extends TranscriptItem['kind']>(s: SessionState, kind: K, id: string) {
  const i = s.index[kind + ':' + id]
  return i === undefined ? undefined : s.items[i] as Extract<TranscriptItem, { kind: K }>
}

function removeItem(s: SessionState, kind: TranscriptItem['kind'], id: string) {
  const i = s.index[kind + ':' + id]
  if (i === undefined) return
  s.items.splice(i, 1)
  reindex(s)
}

function reindex(s: SessionState) {
  s.index = {}
  s.items.forEach((it, i) => {
    s.index[it.kind + ':' + it.id] = i
  })
}

function setStatus(s: SessionState, status: Status) {
  if (s.meta.status !== 'ended') s.meta.status = status
}

function blockingStatus(s: SessionState): Status {
  return s.pendingPermission || s.pendingQuestion || s.pendingPlan ? 'needs_input' : 'working'
}

// A reply streamed outside a turn (the model's answer to a Stop) that was cancelled leaves deltas with no final
// message; they are dropped when the conversation moves on.
function dropUnfinished(s: SessionState) {
  if (s.turnStartedAt) return
  const kept = s.items.filter(it => !(it.kind === 'assistant' && it.streaming))
  if (kept.length === s.items.length) return
  s.items = kept
  reindex(s)
}

function isBusy(status: Status) {
  return status === 'working' || status === 'needs_input'
}

// The client says no turn runs (its live status, or an Interrupt it answered with idle) while this state still
// shows one: the end of that turn never arrived (a stream that died, a client restarted mid-turn). Whatever the
// transcript shows as running is cut short, as an interrupted turn would leave it.
export function settleTurn(s: SessionState) {
  for (const it of s.items) {
    if (it.kind === 'tool' && it.status === 'running') it.status = 'interrupted'
    if (it.kind === 'assistant') it.streaming = false
    if (it.kind === 'question' && it.state === 'pending') it.state = 'cancelled'
  }
  s.turnStartedAt = 0
  s.pendingPermission = null
  s.pendingQuestion = null
  s.pendingPlan = null
  if (isBusy(s.meta.status) || s.meta.status === 'waiting_for_connection') setStatus(s, 'idle')
}

// The session as the client has it now (SubscribeResponse.session after the replay). Its status wins over the
// replayed events: an idle session has no running turn, whatever the stream showed before.
export function applyLiveInfo(s: SessionState, info: SessionInfo) {
  const status = statusName(info.status)
  s.meta = { ...sessionMeta(info), status: s.meta.status === 'ended' ? 'ended' : status }
  if (status === 'idle' || status === 'error') settleTurn(s)
}

// ── reducer ──────────────────────────────────────────────────────────────────

export function reduce(s: SessionState, ev: SessionEvent): SessionState {
  const e = ev.event
  const seq = Number(ev.seq)
  const at = ev.timestamp ? Date.parse(ev.timestamp) : Date.now()

  if (e.case === 'assistantDelta') {
    applyDelta(s, e.value.messageId, e.value.index, e.value.delta, at)
    return s
  }
  if (seq > 0 && seq <= s.lastSeq) return s
  if (seq > 0) s.lastSeq = seq

  switch (e.case) {
    case 'sessionUpdated': {
      const status = statusName(e.value.status)
      s.meta = { ...sessionMeta(e.value), status: s.meta.status === 'ended' ? 'ended' : status }
      // The client reports idle only after a turn's TurnFinished: a turn still open here lost its end.
      if (s.turnStartedAt && (status === 'idle' || status === 'error')) settleTurn(s)
      break
    }

    case 'userMessage': {
      dropUnfinished(s)
      const id = e.value.uuid || 'u' + seq
      if (!find(s, 'user', id)) push(s, userItem(id, e.value, at))
      // The agent took these messages: they are read, and no longer wait in the client's queue.
      for (const c of e.value.clientUuids) s.delivered[c] = id
      s.queue = s.queue.filter(q => q.id !== id && !e.value.clientUuids.includes(q.id))
      // The user moved on: a Show they did not see is stale (the transcript row still reopens it).
      s.showRequest = null
      break
    }

    case 'turnStarted':
      dropUnfinished(s)
      s.turnStartedAt = at
      s.turnModel = e.value.model
      s.turnTokens = 0
      setStatus(s, 'working')
      break

    case 'assistantMessage': {
      const m = e.value
      const blocks: AssistantItem['blocks'] = []
      const toolUseIds: string[] = []
      for (const b of m.content) {
        if (b.block.case === 'text' && b.block.value.text) blocks.push({ type: 'text', text: b.block.value.text })
        else if (b.block.case === 'thinking') blocks.push({ type: 'thinking', text: b.block.value.thinking })
        else if (b.block.case === 'toolUse') toolUseIds.push(b.block.value.id)
      }
      for (const id of toolUseIds) s.toolOwners[id] = { messageId: m.messageId, count: toolUseIds.length }
      const existing = find(s, 'assistant', m.messageId)
      if (existing) {
        // Keep the measured thinking time from the deltas.
        for (const b of blocks) {
          const prev = existing.blocks.find(p => p.type === 'thinking')
          if (b.type === 'thinking' && prev?.durationMs) b.durationMs = prev.durationMs
        }
        existing.blocks = blocks
        existing.model = m.model
        existing.streaming = false
        existing.toolUseIds = toolUseIds
      } else {
        push(s, { kind: 'assistant', id: m.messageId, model: m.model, blocks, streaming: false, toolUseIds })
      }
      // A message without visible text (tool calls only) needs no item of its own.
      const item = find(s, 'assistant', m.messageId)
      if (item && item.blocks.length === 0) removeItem(s, 'assistant', m.messageId)
      const u = usageOf(m.usage)
      s.turnTokens += u.outputTokens
      s.contextTokens = u.inputTokens + u.cacheReadTokens + u.cacheCreationTokens + u.outputTokens
      s.thinkingStartedAt[m.messageId] = 0
      break
    }

    case 'toolCallStarted': {
      const t = e.value
      if (find(s, 'tool', t.toolUseId)) break
      const owner = toolOwner(s, t.toolUseId)
      const item: ToolItem = {
        kind: 'tool',
        id: t.toolUseId,
        name: t.name,
        input: parseJson(t.inputJson) ?? {},
        summary: t.summary,
        status: 'running',
        output: '',
        result: null,
        resultSummary: '',
        durationMs: 0,
        startedAt: at,
        messageId: owner.messageId,
        grouped: owner.count > 1
      }
      push(s, item)
      break
    }

    case 'toolCallFinished': {
      const t = e.value
      const item = find(s, 'tool', t.toolUseId)
      if (!item) break
      item.status = t.interrupted ? 'interrupted' : t.isError ? 'error' : 'ok'
      item.output = textOf(t.content)
      item.result = parseJson(t.structuredResultJson)
      item.resultSummary = t.summary
      item.durationMs = Number(t.durationMs)
      break
    }

    case 'permissionRequest': {
      const p = e.value
      if (!find(s, 'permission', p.requestId)) {
        push(s, {
          kind: 'permission',
          id: p.requestId,
          toolUseId: p.toolUseId,
          toolName: p.toolName,
          input: parseJson(p.inputJson) ?? {},
          suggestedRules: [...p.suggestedRules],
          reason: p.reason,
          state: 'pending',
          resolvedBy: ''
        })
      }
      s.pendingPermission = p.requestId
      setStatus(s, 'needs_input')
      break
    }

    case 'permissionResolved': {
      const p = e.value
      const item = find(s, 'permission', p.requestId)
      if (item) {
        item.state = p.decision === RespondToPermissionRequest_Decision.ALLOW
          ? 'allowed'
          : p.decision === RespondToPermissionRequest_Decision.ALLOW_ALWAYS ? 'allowed_always' : 'denied'
        item.resolvedBy = p.resolvedBy
      }
      if (s.pendingPermission === p.requestId) s.pendingPermission = null
      if (s.meta.status === 'needs_input') setStatus(s, blockingStatus(s))
      break
    }

    case 'question': {
      const q = e.value
      if (!find(s, 'question', q.requestId)) {
        push(s, {
          kind: 'question',
          id: q.requestId,
          toolUseId: q.toolUseId,
          items: q.items.map(questionItemVM),
          state: 'pending',
          resolvedBy: '',
          answers: [],
          message: ''
        })
      }
      s.pendingQuestion = q.requestId
      setStatus(s, 'needs_input')
      break
    }

    case 'questionResolved': {
      const item = find(s, 'question', e.value.requestId)
      if (item) {
        item.state = questionState(e.value)
        item.resolvedBy = e.value.resolvedBy
        item.answers = e.value.answers.map(answerVM)
        item.message = e.value.message
      }
      if (s.pendingQuestion === e.value.requestId) s.pendingQuestion = null
      if (s.meta.status === 'needs_input') setStatus(s, blockingStatus(s))
      break
    }

    case 'planProposed': {
      const p = e.value
      if (!find(s, 'plan', p.requestId)) {
        push(s, { kind: 'plan', id: p.requestId, plan: p.plan, planFilePath: p.planFilePath, state: 'pending', resolvedBy: '' })
      }
      s.pendingPlan = p.requestId
      setStatus(s, 'needs_input')
      break
    }

    case 'planResolved': {
      const p = e.value
      const item = find(s, 'plan', p.requestId)
      if (item) {
        item.state = p.decision === RespondToPlanRequest_Decision.APPROVE_ACCEPT_EDITS
          ? 'approved_accept_edits'
          : p.decision === RespondToPlanRequest_Decision.APPROVE_MANUAL ? 'approved_manual' : 'keep_planning'
        item.resolvedBy = p.resolvedBy
      }
      if (s.pendingPlan === p.requestId) s.pendingPlan = null
      if (s.meta.status === 'needs_input') setStatus(s, blockingStatus(s))
      break
    }

    case 'queueChanged':
      s.queue = e.value.queued.map((m, i) => ({ id: m.uuid || 'q' + i, text: textOf(m.content), ...(m.queuedAt && { at: Date.parse(m.queuedAt) }) }))
      break

    case 'todosUpdated': {
      s.todos = e.value.todos.map(t => ({
        content: t.content,
        activeForm: t.activeForm,
        status: t.status === Todo_Status.COMPLETED ? 'completed' as const : t.status === Todo_Status.IN_PROGRESS ? 'in_progress' as const : 'pending' as const
      }))
      const existing = find(s, 'todos', 'todos')
      if (existing) existing.todos = s.todos
      else push(s, { kind: 'todos', id: 'todos', todos: s.todos })
      break
    }

    case 'turnFinished': {
      const t = e.value
      const usage = usageOf(t.usage)
      const result = t.result === TurnFinished_Result.INTERRUPTED
        ? 'interrupted'
        : t.result === TurnFinished_Result.ERROR_MAX_TURNS
          ? 'max_turns'
          : t.result === TurnFinished_Result.ERROR_DURING_EXECUTION ? 'error' : 'success'
      push(s, {
        kind: 'turn',
        id: 'turn' + seq,
        result,
        model: s.turnModel || s.meta.model,
        usage,
        costUsd: t.costUsd,
        durationMs: Number(t.durationMs),
        errorMessage: t.errorMessage
      })
      s.usage = {
        inputTokens: s.usage.inputTokens + usage.inputTokens,
        outputTokens: s.usage.outputTokens + usage.outputTokens,
        cacheReadTokens: s.usage.cacheReadTokens + usage.cacheReadTokens,
        cacheCreationTokens: s.usage.cacheCreationTokens + usage.cacheCreationTokens
      }
      s.costUsd += t.costUsd
      // Anything still running was cut short with the turn.
      for (const it of s.items) {
        if (it.kind === 'tool' && it.status === 'running' && !isSubagent(it)) it.status = result === 'interrupted' ? 'interrupted' : it.status
        if (it.kind === 'assistant') it.streaming = false
        // A question still open when the turn ends can no longer be answered.
        if (it.kind === 'question' && it.state === 'pending') it.state = 'cancelled'
      }
      s.turnStartedAt = 0
      s.pendingPermission = null
      s.pendingQuestion = null
      s.pendingPlan = null
      setStatus(s, result === 'error' || result === 'max_turns' ? 'error' : 'idle')
      break
    }

    case 'notice': {
      const n = e.value
      if (n.kind === 'subagent_progress') {
        applySubagentProgress(s, n.text)
        break
      }
      // Auto mode verdicts arrive as JSON in the text (client's AutoDecisionNotice). Allowed calls go on
      // the tool item only; blocked or deferred ones become a readable notice.
      if (n.kind === 'auto_decision') {
        const d = parseJson(n.text) as { toolUseId?: string, toolName?: string, decision?: string, reason?: string, provider?: string, text?: string } | null
        if (d) {
          const tool = d.toolUseId ? find(s, 'tool', d.toolUseId) : undefined
          if (tool) tool.autoDecision = { decision: d.decision ?? '', reason: d.reason ?? '', provider: d.provider ?? '' }
          if (d.decision === 'allow') break
          const what = d.toolName || 'a tool call'
          push(s, {
            kind: 'notice',
            id: 'n' + seq,
            level: d.decision === 'deny' ? 'warning' : 'info',
            noticeKind: n.kind,
            text: d.decision === 'deny'
              ? `Auto mode blocked ${what}${d.reason ? `: ${d.reason}` : ''}`
              : `Auto mode asked you about ${what}${d.reason ? `: ${d.reason}` : ''}`
          })
          break
        }
      }
      push(s, {
        kind: 'notice',
        id: 'n' + seq,
        level: n.level === Notice_Level.ERROR ? 'error' : n.level === Notice_Level.WARNING ? 'warning' : 'info',
        noticeKind: n.kind,
        text: n.text
      })
      if (n.kind === 'connection_lost') setStatus(s, 'waiting_for_connection')
      break
    }

    case 'taskStarted': {
      const t = e.value
      s.tasks[t.taskId] = {
        id: t.taskId,
        toolUseId: t.toolUseId,
        command: t.command,
        description: t.description,
        outputPath: t.outputPath,
        state: 'running',
        exitCode: 0,
        durationMs: 0,
        startedAt: at,
        tail: ''
      }
      if (!s.taskOrder.includes(t.taskId)) s.taskOrder.push(t.taskId)
      if (!find(s, 'task', t.taskId)) push(s, { kind: 'task', id: t.taskId })
      break
    }

    case 'taskOutput': {
      const t = s.tasks[e.value.taskId]
      if (t) t.tail = e.value.text
      break
    }

    case 'taskFinished': {
      const f = e.value
      const t = s.tasks[f.taskId]
      if (t) {
        t.exitCode = f.exitCode
        t.durationMs = Number(f.durationMs)
        t.state = f.reason === TaskFinished_Reason.STOPPED || f.reason === TaskFinished_Reason.SESSION_CLOSED
          ? 'stopped'
          : f.exitCode === 0 ? 'done' : 'failed'
      }
      break
    }

    case 'compacted': {
      const c = e.value
      push(s, {
        kind: 'compact',
        id: 'c' + seq,
        trigger: COMPACT_TRIGGER[c.trigger] ?? 'manual',
        tokensBefore: Number(c.tokensBefore),
        tokensAfter: Number(c.tokensAfter)
      })
      s.contextTokens = Number(c.tokensAfter)
      break
    }

    case 'rewound': {
      // A conversation rewind drops the message it went back to and everything after it (the client's
      // conversation now ends before that message); the marker stays.
      const m = e.value.mode
      const at = s.index['user:' + e.value.checkpointId]
      if ((m === RewindRequest_Mode.CODE_AND_CONVERSATION || m === RewindRequest_Mode.CONVERSATION) && at !== undefined) {
        s.items.splice(at)
        reindex(s)
      }
      push(s, { kind: 'rewound', id: 'r' + seq, filesRestored: e.value.filesRestored, filesSkipped: e.value.filesSkipped })
      break
    }

    case 'permissionModeChanged':
      s.meta.permissionMode = permissionModeName(e.value.mode)
      break

    case 'showRequested': {
      const target = showTarget(e.value)
      if (target) s.showRequest = { id: e.value.toolUseId || 'show' + seq, target }
      break
    }

    case 'sessionEnded':
      s.endedReason = e.value.reason
      s.meta.status = 'ended'
      break

    case 'checkpointCreated':
    default:
      break
  }
  return s
}

// ── deltas ───────────────────────────────────────────────────────────────────

function applyDelta(s: SessionState, messageId: string, index: number, delta: AssistantDelta['delta'], at: number) {
  if (delta.case !== 'text' && delta.case !== 'thinking') return
  let item = find(s, 'assistant', messageId)
  if (!item) {
    push(s, { kind: 'assistant', id: messageId, model: s.turnModel, blocks: [], streaming: true, toolUseIds: [] })
    item = find(s, 'assistant', messageId)!
  }
  item.streaming = true
  const type = delta.case
  let block = item.blocks.find(b => b.idx === index && b.type === type)
  if (!block) {
    item.blocks.push({ type, text: '', idx: index })
    block = item.blocks[item.blocks.length - 1]!
  }
  if (type === 'thinking') {
    if (!s.thinkingStartedAt[messageId]) s.thinkingStartedAt[messageId] = at
  } else {
    const started = s.thinkingStartedAt[messageId]
    const thinking = item.blocks.find(b => b.type === 'thinking')
    if (started && thinking && !thinking.durationMs) thinking.durationMs = Math.max(0, at - started)
  }
  block.text += delta.value
  // A reply outside a turn (the model answering a Stop) streams without making the session busy.
  if (s.turnStartedAt) setStatus(s, blockingStatus(s))
}

// ── tool ownership (which assistant message asked for a tool call) ───────────

function toolOwner(s: SessionState, toolUseId: string) {
  const known = s.toolOwners[toolUseId]
  if (known) return known
  for (let i = s.items.length - 1; i >= 0; i--) {
    const it = s.items[i]!
    if (it.kind === 'assistant' && it.toolUseIds.includes(toolUseId)) return { messageId: it.id, count: it.toolUseIds.length }
  }
  return { messageId: '', count: 1 }
}

export function isSubagent(t: ToolItem) {
  return t.name === 'Task' || t.name === 'Agent'
}

// Subagent progress travels as Notice{kind: "subagent_progress", text: JSON} until the contract has a dedicated event:
// {"tool_use_id": "...", "steps": [{"tool","target","meta","status"}], "tokens": 18200, "tool_calls": 4, "model": "qwen3-coder"}
function applySubagentProgress(s: SessionState, text: string) {
  const p = parseJson(text)
  if (!p || typeof p.tool_use_id !== 'string') return
  const item = find(s, 'tool', p.tool_use_id)
  if (!item) return
  const steps = Array.isArray(p.steps) ? p.steps as Record<string, unknown>[] : []
  item.subagent = {
    steps: steps.map(st => ({
      tool: String(st.tool ?? ''),
      target: String(st.target ?? ''),
      meta: st.meta ? String(st.meta) : undefined,
      status: st.status === 'running' || st.status === 'waiting' || st.status === 'failed' ? st.status : 'done'
    })),
    tokens: Number(p.tokens ?? 0),
    toolCalls: Number(p.tool_calls ?? steps.length),
    model: String(p.model ?? ''),
    active: Number(p.active ?? steps.filter(st => st.status === 'running').length)
  }
}

export function reduceAll(s: SessionState, events: SessionEvent[]) {
  for (const ev of events) reduce(s, ev)
  return s
}
