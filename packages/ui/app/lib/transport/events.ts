// Small builder for harness.agent.v1.SessionEvent sequences, used by the mock transport and the tests.

import { create } from '@bufbuild/protobuf'
import {
  Compacted_Trigger,
  Notice_Level,
  SessionEventSchema,
  SessionInfoSchema,
  TaskFinished_Reason,
  Todo_Status,
  TurnFinished_Result
} from '~/gen/harness/agent/v1/agent_pb'
import type {
  PermissionMode,
  RespondToPermissionRequest_Decision,
  RespondToPlanRequest_Decision,
  SessionEvent,
  SessionInfo
} from '~/gen/harness/agent/v1/agent_pb'
import { ContentBlockSchema, StopReason, type ContentBlock } from '~/gen/harness/llm/v1/content_pb'

type EventCase = SessionEvent['event']

export interface UsageInit {
  input?: number
  output?: number
  cacheRead?: number
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

  user(uuid: string, body: string) {
    return this.emit({ case: 'userMessage', value: { uuid, content: [text(body)] } as never })
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

  toolFinished(toolUseId: string, o: { output?: string, result?: unknown, summary?: string, durationMs?: number, isError?: boolean, interrupted?: boolean } = {}) {
    return this.emit({
      case: 'toolCallFinished',
      value: {
        toolUseId,
        isError: !!o.isError,
        interrupted: !!o.interrupted,
        content: o.output ? [text(o.output)] : [],
        durationMs: BigInt(o.durationMs ?? 0),
        structuredResultJson: o.result ? JSON.stringify(o.result) : '',
        summary: o.summary ?? ''
      } as never
    })
  }

  permissionRequest(requestId: string, toolUseId: string, toolName: string, input: Record<string, unknown>, suggestedRules: string[], reason: string) {
    return this.emit({ case: 'permissionRequest', value: { requestId, toolUseId, toolName, inputJson: JSON.stringify(input), suggestedRules, reason } as never })
  }

  permissionResolved(requestId: string, decision: RespondToPermissionRequest_Decision, resolvedBy = 'local') {
    return this.emit({ case: 'permissionResolved', value: { requestId, decision, resolvedBy } as never })
  }

  question(requestId: string, toolUseId: string, items: { question: string, header: string, multiSelect?: boolean, options: { label: string, description: string }[] }[]) {
    return this.emit({ case: 'question', value: { requestId, toolUseId, items: items.map(i => ({ ...i, multiSelect: !!i.multiSelect })) } as never })
  }

  questionResolved(requestId: string, resolvedBy = 'local') {
    return this.emit({ case: 'questionResolved', value: { requestId, resolvedBy } as never })
  }

  planProposed(requestId: string, toolUseId: string, plan: string, planFilePath: string) {
    return this.emit({ case: 'planProposed', value: { requestId, toolUseId, plan, planFilePath } as never })
  }

  planResolved(requestId: string, decision: RespondToPlanRequest_Decision, resolvedBy = 'local') {
    return this.emit({ case: 'planResolved', value: { requestId, decision, resolvedBy } as never })
  }

  queue(messages: { uuid: string, text: string }[]) {
    return this.emit({ case: 'queueChanged', value: { queued: messages.map(m => ({ uuid: m.uuid, content: [text(m.text)] })) } as never })
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

  compacted(before: number, after: number, auto = false) {
    return this.emit({ case: 'compacted', value: { trigger: auto ? Compacted_Trigger.AUTO : Compacted_Trigger.MANUAL, tokensBefore: BigInt(before), tokensAfter: BigInt(after) } as never })
  }

  permissionModeChanged(mode: PermissionMode) {
    return this.emit({ case: 'permissionModeChanged', value: { mode } as never })
  }

  sessionEnded(reason: string) {
    return this.emit({ case: 'sessionEnded', value: { reason } as never })
  }
}
