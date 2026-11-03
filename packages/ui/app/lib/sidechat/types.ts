// SideChatsApi: side chats next to a session (harness.agent.v1 side chat RPCs, proto/harness/agent/v1/sidechat.proto).
// A side chat sees the main session's conversation but never changes it: no tools, nothing added to the main
// thread. The client stores side chats next to the session's transcript, so they survive reloads. Two
// implementations: connect.ts (a real client) and mock.ts (simulated streaming answers, for the mock transport).
//
// Only open sessions of the client's machine have side chats; a session of another machine is refused.

import type { EffortName } from '~/lib/session/types'

export interface SideChatInfo {
  id: string
  sessionId: string
  // "Side chat" until the first question names it.
  title: string
  // The model and effort of the next question, remembered for this side chat only.
  model: string
  effort?: EffortName
  // RFC 3339.
  createdAt: string
  updatedAt: string
  messageCount: number
  // An answer is streaming right now (e.g. started in another window).
  answering: boolean
}

export interface SideChatMessage {
  id: string
  role: 'user' | 'assistant'
  // Markdown for answers.
  text: string
  // User messages: the quoted excerpt of the main session.
  quote?: string
  // Assistant messages: what answered and what it cost.
  model?: string
  effort?: EffortName
  usage?: { inputTokens: number, outputTokens: number, cacheReadInputTokens: number, cacheCreationInputTokens: number }
  createdAt: string
  // The answer was stopped; text is what arrived until then.
  stopped?: boolean
  // The request failed.
  error?: string
}

export interface CreateSideChatOptions {
  title?: string
  // Omitted: the main session's current model / effort (as the client knows them).
  model?: string
  effort?: EffortName
}

export interface UpdateSideChatPatch {
  title?: string
  model?: string
  effort?: EffortName
}

export interface AskSideChatOptions {
  text: string
  quote?: string
  // Set: answer with this model / effort and remember it for the side chat.
  model?: string
  effort?: EffortName
}

export interface AskHandlers {
  // The stored question.
  onQuestion?: (m: SideChatMessage) => void
  // A piece of the answer's text.
  onDelta?: (text: string) => void
  // The stored answer (also when stopped or failed) and the side chat after it.
  onAnswer?: (m: SideChatMessage, chat?: SideChatInfo) => void
}

// A running question: done settles when the stream ends (rejects on a transport error); cancel drops the
// stream (the client then stops the answer and keeps its text).
export interface AskHandle {
  done: Promise<void>
  cancel(): void
}

export interface SideChatsApi {
  // Oldest first.
  list(sessionId: string): Promise<SideChatInfo[]>
  create(sessionId: string, opts?: CreateSideChatOptions): Promise<SideChatInfo>
  get(sessionId: string, id: string): Promise<{ chat: SideChatInfo, messages: SideChatMessage[] }>
  update(sessionId: string, id: string, patch: UpdateSideChatPatch): Promise<SideChatInfo>
  // Stops a running answer and deletes the side chat.
  remove(sessionId: string, id: string): Promise<void>
  ask(sessionId: string, id: string, opts: AskSideChatOptions, handlers: AskHandlers): AskHandle
  // Stops the running answer; false when none was running.
  stop(sessionId: string, id: string): Promise<boolean>
}
