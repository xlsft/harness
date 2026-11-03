// Small builder for harness.agent.v1.SessionEvent sequences, used by the mock transport and the tests.

import { create } from '@bufbuild/protobuf'
import {
  Compacted_Trigger,
  Notice_Level,
  QuestionItem_Type,
  QuestionOutcome,
  SessionEventSchema,
  SessionInfoSchema,
  ShowRequested_Kind,
  TaskFinished_Reason,
  Todo_Status,
  TurnFinished_Result,
  UsageLimit_State
} from '~/gen/harness/agent/v1/agent_pb'
import type {
  BackgroundReason,
  PermissionMode,
  RespondToPermissionRequest_Decision,
  RespondToPlanRequest_Decision,
  SessionEvent,
  SessionInfo
} from '~/gen/harness/agent/v1/agent_pb'
import { UserShellFinished_Reason } from '~/gen/harness/agent/v1/shell_pb'
import { ContentBlockSchema, StopReason, type ContentBlock } from '~/gen/harness/llm/v1/content_pb'

type EventCase = SessionEvent['event']

export interface QuestionInit {
  question: string
  header: string
  type?: 'single' | 'multi' | 'text' | 'yes_no'
  multiSelect?: boolean
  options?: { label: string, description?: string, recommended?: boolean }[]
  min?: number
  max?: number
  allowOther?: boolean
}

export interface UsageInit {
  input?: number
  output?: number
  cacheRead?: number
}

export interface UsageLimitInit {
  state: 'near' | 'wrap_up' | 'reached' | 'cleared'
  providerId?: string
  providerName?: string
  windowLabel?: string
  usedPercent?: number
  resetsAt?: string
  balanceText?: string
  wrappedUp?: boolean
}

export interface UserShellInit {
  command: string
  cwd: string
  exitCode: number
  durationMs: number
  stdout?: string
  stderr?: string
  reason?: 'exited' | 'stopped' | 'time_limit' | 'failed'
  truncated?: boolean
  error?: string
}

export type AssistantPart
  = | { thinking: string }
    | { text: string }
    | { toolUse: { id: string, name: string, input: Record<string, unknown> } }

export function text(t: string): ContentBlock {
  return create(ContentBlockSchema, { block: { case: 'text', value: { text: t } } })
}

function usage(u?: UsageInit) {
  return {
    inputTokens: BigInt(u?.input ?? 0),
    outputTokens: BigInt(u?.output ?? 0),
    cacheReadInputTokens: BigInt(u?.cacheRead ?? 0),
    cacheCreationInputTokens: 0n
  }
}

export function sessionInfo(init: Parameters<typeof create<typeof SessionInfoSchema>>[1]): SessionInfo {
  return create(SessionInfoSchema, init)
}

export class EventBuilder {
  seq: number
  t: number
  readonly events: SessionEvent[] = []

  constructor(readonly sessionId: string, startMs: number, startSeq = 0) {
    this.t = startMs
    this.seq = startSeq
  }

  // Advance the clock (ms) before the next event.
  wait(ms: number) {
    this.t += ms
    return this
  }

  at(ms: number) {
    this.t = ms
    return this
  }

  private emit(event: EventCase, durable = true): SessionEvent {
    if (durable) this.seq += 1
    const ev = create(SessionEventSchema, {
      seq: BigInt(this.seq),
      sessionId: this.sessionId,
      timestamp: new Date(this.t).toISOString(),
      event: event as never
    })
    this.events.push(ev)
    return ev
  }

  sessionUpdated(info: SessionInfo) {
    return this.emit({ case: 'sessionUpdated', value: info })
  }

  // clientUuids: the frontend ids of the messages this entry carries (read receipts).
  user(uuid: string, body: string, clientUuids: string[] = []) {
    return this.emit({ case: 'userMessage', value: { uuid, content: [text(body)], clientUuids } as never })
  }

  turnStarted(userMessageUuid: string, model: string) {
    return this.emit({ case: 'turnStarted', value: { userMessageUuid, model } as never })
  }

  delta(messageId: string, index: number, kind: 'text' | 'thinking', value: string) {
    return this.emit({ case: 'assistantDelta', value: { messageId, index, delta: { case: kind, value } } as never }, false)
  }

  assistant(messageId: string, model: string, parts: AssistantPart[], u?: UsageInit) {
    const content = parts.map((p) => {
      if ('thinking' in p) return create(ContentBlockSchema, { block: { case: 'thinking', value: { thinking: p.thinking } } })
      if ('text' in p) return text(p.text)
      return create(ContentBlockSchema, { block: { case: 'toolUse', value: { id: p.toolUse.id, name: p.toolUse.name, inputJson: JSON.stringify(p.toolUse.input) } } })
    })
    const hasTools = parts.some(p => 'toolUse' in p)
    return this.emit({
      case: 'assistantMessage',
      value: { messageId, model, content, stopReason: hasTools ? StopReason.TOOL_USE : StopReason.END_TURN, usage: usage(u) } as never
    })
  }

  toolStarted(toolUseId: string, name: string, input: Record<string, unknown>, summary: string) {
    return this.emit({ case: 'toolCallStarted', value: { toolUseId, name, inputJson: JSON.stringify(input), summary } as never })
  }

  // The model-written label of a call (Bash), sent shortly after the call starts.
  toolTitle(toolUseId: string, title: string) {
    return this.emit({ case: 'toolTitle', value: { toolUseId, title } as never })
  }

  toolFinished(toolUseId: string, o: { output?: string, result?: unknown, summary?: string, durationMs?: number, isError?: boolean, interrupted?: boolean, background?: { taskId: string, reason: BackgroundReason } } = {}) {
    return this.emit({
      case: 'toolCallFinished',
      value: {
        toolUseId,
        isError: !!o.isError,
        interrupted: !!o.interrupted,
        content: o.output ? [text(o.output)] : [],
        durationMs: BigInt(o.durationMs ?? 0),
        structuredResultJson: o.result ? JSON.stringify(o.result) : '',
        summary: o.summary ?? '',
        background: o.background
      } as never
    })
  }

  permissionRequest(requestId: string, toolUseId: string, toolName: string, input: Record<string, unknown>, suggestedRules: string[], reason: string) {
    return this.emit({ case: 'permissionRequest', value: { requestId, toolUseId, toolName, inputJson: JSON.stringify(input), suggestedRules, reason } as never })
  }

  permissionResolved(requestId: string, decision: RespondToPermissionRequest_Decision, resolvedBy = 'local') {
    return this.emit({ case: 'permissionResolved', value: { requestId, decision, resolvedBy } as never })
  }

  // Typed questions; without a type an item is sent the way an older client sends it (multi_select only).
  question(requestId: string, toolUseId: string, items: QuestionInit[]) {
    const TYPE = { single: QuestionItem_Type.SINGLE, multi: QuestionItem_Type.MULTI, text: QuestionItem_Type.TEXT, yes_no: QuestionItem_Type.YES_NO }
    return this.emit({ case: 'question', value: { requestId, toolUseId, items: items.map(i => ({
      question: i.question,
      header: i.header,
      type: i.type ? TYPE[i.type] : QuestionItem_Type.UNSPECIFIED,
      multiSelect: i.type ? i.type === 'multi' : !!i.multiSelect,
      options: (i.type === 'yes_no' && !i.options ? [{ label: 'Yes' }, { label: 'No' }] : i.options ?? [])
        .map(o => ({ label: o.label, description: o.description ?? '', recommended: !!o.recommended })),
      min: i.min ?? 0,
      max: i.max ?? 0,
      allowOther: i.type ? i.allowOther ?? i.type !== 'text' : false
    })) } as never })
  }

  questionResolved(requestId: string, resolvedBy = 'local', outcome: QuestionOutcome = QuestionOutcome.ANSWERED,
    answers: { selected?: string[], text?: string, skipped?: boolean }[] = [], message = '') {
    return this.emit({ case: 'questionResolved', value: { requestId, resolvedBy, outcome, message,
      answers: answers.map(a => ({ selected: a.selected ?? [], text: a.text ?? '', skipped: !!a.skipped })) } as never })
  }

  planProposed(requestId: string, toolUseId: string, plan: string, planFilePath: string) {
    return this.emit({ case: 'planProposed', value: { requestId, toolUseId, plan, planFilePath } as never })
  }

  planResolved(requestId: string, decision: RespondToPlanRequest_Decision, resolvedBy = 'local') {
    return this.emit({ case: 'planResolved', value: { requestId, decision, resolvedBy } as never })
  }

  show(toolUseId: string, target: { file: string, lines?: [number, number] } | { diff: string } | { url: string }) {
    const value = 'file' in target
      ? { toolUseId, kind: ShowRequested_Kind.FILE, path: target.file, lineStart: target.lines?.[0] ?? 0, lineEnd: target.lines?.[1] ?? 0, url: '' }
      : 'diff' in target
        ? { toolUseId, kind: ShowRequested_Kind.DIFF, path: target.diff, lineStart: 0, lineEnd: 0, url: '' }
        : { toolUseId, kind: ShowRequested_Kind.URL, path: '', lineStart: 0, lineEnd: 0, url: target.url }
    return this.emit({ case: 'showRequested', value: value as never })
  }

  queue(messages: { uuid: string, text: string, queuedAt?: number }[]) {
    return this.emit({ case: 'queueChanged', value: { queued: messages.map(m => ({ uuid: m.uuid, content: [text(m.text)], queuedAt: m.queuedAt ? new Date(m.queuedAt).toISOString() : '' })) } as never })
  }

  checkpoint(id: string, preview = '') {
    return this.emit({ case: 'checkpointCreated', value: { checkpoint: { id, userMessageUuid: id, promptPreview: preview, createdAt: new Date(this.t).toISOString(), hasCodeChanges: false } } as never })
  }

  rewound(checkpointId: string, mode: number, restoredPrompt = '', filesRestored = 0) {
    return this.emit({ case: 'rewound', value: { checkpointId, mode, restoredPrompt, filesRestored, filesSkipped: 0 } as never })
  }

  todos(list: { content: string, activeForm: string, status: 'pending' | 'in_progress' | 'completed' }[]) {
    const st = { pending: Todo_Status.PENDING, in_progress: Todo_Status.IN_PROGRESS, completed: Todo_Status.COMPLETED }
    return this.emit({ case: 'todosUpdated', value: { todos: list.map(t => ({ content: t.content, activeForm: t.activeForm, status: st[t.status] })) } as never })
  }

  turnFinished(result: 'success' | 'interrupted' | 'error', o: { durationMs?: number, costUsd?: number, usage?: UsageInit, numTurns?: number, error?: string } = {}) {
    const r = { success: TurnFinished_Result.SUCCESS, interrupted: TurnFinished_Result.INTERRUPTED, error: TurnFinished_Result.ERROR_DURING_EXECUTION }[result]
    return this.emit({
      case: 'turnFinished',
      value: { result: r, numTurns: o.numTurns ?? 1, usage: usage(o.usage), costUsd: o.costUsd ?? 0, durationMs: BigInt(o.durationMs ?? 0), errorMessage: o.error ?? '' } as never
    })
  }

  notice(level: 'info' | 'warning' | 'error', kind: string, body: string | Record<string, unknown>) {
    const l = { info: Notice_Level.INFO, warning: Notice_Level.WARNING, error: Notice_Level.ERROR }[level]
    return this.emit({ case: 'notice', value: { level: l, kind, text: typeof body === 'string' ? body : JSON.stringify(body) } as never })
  }

  taskStarted(taskId: string, toolUseId: string, command: string, description: string) {
    return this.emit({ case: 'taskStarted', value: { taskId, toolUseId, command, description, outputPath: `/tmp/harness/tasks/${taskId}.log` } as never })
  }

  taskOutput(taskId: string, body: string) {
    return this.emit({ case: 'taskOutput', value: { taskId, text: body } as never })
  }

  taskFinished(taskId: string, exitCode: number, durationMs: number, stopped = false) {
    return this.emit({
      case: 'taskFinished',
      value: { taskId, reason: stopped ? TaskFinished_Reason.STOPPED : TaskFinished_Reason.EXITED, exitCode, durationMs: BigInt(durationMs) } as never
    })
  }

  userShellStarted(commandId: string, command: string, cwd: string) {
    return this.emit({ case: 'userShellStarted', value: { commandId, command, cwd } as never })
  }

  userShellOutput(commandId: string, body: string) {
    return this.emit({ case: 'userShellOutput', value: { commandId, text: body } as never })
  }

  userShellFinished(commandId: string, o: UserShellInit) {
    const reason = { exited: UserShellFinished_Reason.EXITED, stopped: UserShellFinished_Reason.STOPPED, time_limit: UserShellFinished_Reason.TIME_LIMIT,
      failed: UserShellFinished_Reason.FAILED }[o.reason ?? 'exited']
    return this.emit({ case: 'userShellFinished', value: { commandId, reason, exitCode: o.exitCode, durationMs: BigInt(o.durationMs), stdout: o.stdout ?? '',
      stderr: o.stderr ?? '', truncated: !!o.truncated, error: o.error ?? '', cwd: o.cwd, command: o.command } as never })
  }

  compacted(before: number, after: number, auto = false) {
    return this.emit({ case: 'compacted', value: { trigger: auto ? Compacted_Trigger.AUTO : Compacted_Trigger.MANUAL, tokensBefore: BigInt(before), tokensAfter: BigInt(after) } as never })
  }

  usageLimit(u: UsageLimitInit) {
    const state = { near: UsageLimit_State.NEAR, wrap_up: UsageLimit_State.WRAP_UP, reached: UsageLimit_State.REACHED, cleared: UsageLimit_State.CLEARED }[u.state]
    return this.emit({ case: 'usageLimit', value: { state, providerId: u.providerId ?? '', providerName: u.providerName ?? '', windowLabel: u.windowLabel ?? '',
      usedPercent: u.usedPercent ?? 0, resetsAt: u.resetsAt ?? '', balanceText: u.balanceText ?? '', wrappedUp: !!u.wrappedUp } as never })
  }

  permissionModeChanged(mode: PermissionMode) {
    return this.emit({ case: 'permissionModeChanged', value: { mode } as never })
  }

  sessionEnded(reason: string) {
    return this.emit({ case: 'sessionEnded', value: { reason } as never })
  }
}
