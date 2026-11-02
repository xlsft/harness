// SessionTransport: everything the UI needs from a harness client (harness.agent.v1.AgentService).
// Two implementations: MockTransport (scripted events, the default) and ConnectTransport (connect-web).

import type {
  InitializeResponse,
  PermissionMode,
  RespondToPermissionRequest_Decision,
  RespondToPlanRequest_Decision,
  SendUserMessageRequest_Delivery,
  SessionEvent,
  SessionInfo,
  BackgroundTask
} from '~/gen/harness/agent/v1/agent_pb'
import type { Effort } from '~/gen/harness/llm/v1/content_pb'

export interface ModelInfo {
  id: string
  provider: string
  contextWindow: number
  // e.g. "$0.60/M"; empty when unknown.
  price: string
  offline?: boolean
}

export interface SubscribeHandlers {
  onEvent: (event: SessionEvent) => void
  onError?: (err: unknown) => void
  onEnd?: () => void
}

export interface SessionTransport {
  readonly kind: 'mock' | 'connect'

  initialize(): Promise<InitializeResponse>
  listSessions(): Promise<SessionInfo[]>
  // Replays events after `afterSeq`, then streams live ones. Returns an unsubscribe function.
  subscribe(sessionId: string, afterSeq: number, handlers: SubscribeHandlers): () => void

  sendUserMessage(sessionId: string, uuid: string, text: string, delivery: SendUserMessageRequest_Delivery): Promise<void>
  interrupt(sessionId: string): Promise<void>
  respondToPermission(sessionId: string, requestId: string, decision: RespondToPermissionRequest_Decision, rule?: string, comment?: string): Promise<void>
  answerQuestion(sessionId: string, requestId: string, answers: string[][]): Promise<void>
  respondToPlan(sessionId: string, requestId: string, decision: RespondToPlanRequest_Decision, feedback?: string): Promise<void>

  setModel(sessionId: string, model: string): Promise<void>
  setEffort(sessionId: string, effort: Effort): Promise<void>
  setPermissionMode(sessionId: string, mode: PermissionMode): Promise<void>

  suggestFiles(sessionId: string, query: string, limit: number): Promise<string[]>
  compact(sessionId: string, instructions?: string): Promise<void>
  listTasks(sessionId: string): Promise<BackgroundTask[]>
  stopTask(sessionId: string, taskId: string): Promise<void>

  // Not part of AgentService yet: the model catalog (provider, context, price) comes from the server in M1+.
  // Connect falls back to InitializeResponse.model_ids.
  listModels(): Promise<ModelInfo[]>
}
