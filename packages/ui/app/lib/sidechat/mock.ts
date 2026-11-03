// SideChatsApi for the mock transport: side chats in memory, answers streamed in small chunks from canned text
// that mentions the question and the quote, so the pane's streaming, Stop and model picker can be exercised
// without a client.

import type { EffortName } from '~/lib/session/types'
import type { AskHandle, AskHandlers, AskSideChatOptions, CreateSideChatOptions, SideChatInfo, SideChatMessage, SideChatsApi, UpdateSideChatPatch } from './types'

const DEFAULT_TITLE = 'Side chat'

interface MockChat {
  info: SideChatInfo
  messages: SideChatMessage[]
  stop?: () => void
}

export interface MockSideChatsOptions {
  // The main session's model / effort when create() is given none.
  sessionModel?: (sessionId: string) => string | undefined
  sessionEffort?: (sessionId: string) => EffortName | undefined
  // Milliseconds between chunks (0 in tests); default 30.
  chunkDelay?: number
  // Characters per chunk; default 6.
  chunkSize?: number
}

// The canned answer of a question.
export function mockAnswer(question: string, quote?: string) {
  const about = quote ? `the excerpt *“${quote.length > 80 ? `${quote.slice(0, 79)}…` : quote}”*` : 'the main session'
  return [
    `This is a simulated answer about ${about}.`,
    '',
    `You asked: **${question.trim()}**`,
    '',
    'In a real client the side chat model reads the main conversation (read-only) and answers here:',
    '',
    '1. No tools are offered, so nothing in the session changes.',
    '2. The answer streams in, and **Stop** keeps what arrived.',
    '3. The model and effort picked below apply to this side chat only.',
    '',
    'For example, `createConnectRouter` plus a catch-all route serves Connect from Nitro.'
  ].join('\n')
}

let seq = 0
const nextId = (prefix: string) => `${prefix}${Date.now().toString(36)}${(++seq).toString(36)}`
const now = () => new Date().toISOString()
const titleFrom = (q: string) => {
  const line = q.trim().split('\n')[0]!.trim()
  return line.length > 60 ? `${line.slice(0, 60).trim()}…` : line || DEFAULT_TITLE
}

export class MockSideChatsApi implements SideChatsApi {
  private chats = new Map<string, MockChat>()

  constructor(private opts: MockSideChatsOptions = {}) {}

  private chat(sessionId: string, id: string) {
    const c = this.chats.get(id)
    if (!c || c.info.sessionId !== sessionId) throw new Error(`[not_found] side chat ${id} not found`)
    return c
  }

  private snapshot(c: MockChat): SideChatInfo {
    return { ...c.info, messageCount: c.messages.length, answering: !!c.stop }
  }

  async list(sessionId: string) {
    return [...this.chats.values()].filter(c => c.info.sessionId === sessionId).map(c => this.snapshot(c))
  }

  async create(sessionId: string, opts: CreateSideChatOptions = {}) {
    const t = now()
    const c: MockChat = {
      info: {
        id: nextId('sc_'),
        sessionId,
        title: opts.title?.trim() || DEFAULT_TITLE,
        model: opts.model || this.opts.sessionModel?.(sessionId) || 'claude-sonnet-5',
        effort: opts.effort ?? this.opts.sessionEffort?.(sessionId),
        createdAt: t,
        updatedAt: t,
        messageCount: 0,
        answering: false
      },
      messages: []
    }
    this.chats.set(c.info.id, c)
    return this.snapshot(c)
  }

  async get(sessionId: string, id: string) {
    const c = this.chat(sessionId, id)
    return { chat: this.snapshot(c), messages: c.messages.map(m => ({ ...m })) }
  }

  async update(sessionId: string, id: string, patch: UpdateSideChatPatch) {
    const c = this.chat(sessionId, id)
    if (patch.title?.trim()) c.info.title = patch.title.trim()
    if (patch.model) c.info.model = patch.model
    if (patch.effort) c.info.effort = patch.effort
    c.info.updatedAt = now()
    return this.snapshot(c)
  }

  async remove(sessionId: string, id: string) {
    const c = this.chat(sessionId, id)
    c.stop?.()
    this.chats.delete(id)
  }

  ask(sessionId: string, id: string, opts: AskSideChatOptions, handlers: AskHandlers): AskHandle {
    let cancel = () => {}
    const done = new Promise<void>((resolve, reject) => {
      let c: MockChat
      try {
        c = this.chat(sessionId, id)
      } catch (err) {
        reject(err)
        return
      }
      const text = opts.text.trim()
      if (!text) return reject(new Error('[invalid_argument] text is required'))
      if (c.stop) return reject(new Error('[failed_precondition] this side chat is already answering; stop it first'))
      if (opts.model) c.info.model = opts.model
      if (opts.effort) c.info.effort = opts.effort
      if (!c.messages.length && c.info.title === DEFAULT_TITLE) c.info.title = titleFrom(text)
      const question: SideChatMessage = { id: nextId('scm_'), role: 'user', text, quote: opts.quote?.trim() || undefined, createdAt: now() }
      c.messages.push(question)
      handlers.onQuestion?.({ ...question })

      const full = mockAnswer(text, question.quote)
      const size = this.opts.chunkSize ?? 6
      const delay = this.opts.chunkDelay ?? 30
      let at = 0
      let timer: ReturnType<typeof setTimeout> | undefined
      const finish = (stopped: boolean) => {
        clearTimeout(timer)
        c.stop = undefined
        const answer: SideChatMessage = {
          id: nextId('scm_'),
          role: 'assistant',
          text: full.slice(0, at),
          model: c.info.model,
          effort: c.info.effort,
          usage: { inputTokens: 1800 + c.messages.length * 120, outputTokens: Math.ceil(at / 4), cacheReadInputTokens: 0, cacheCreationInputTokens: 0 },
          createdAt: now(),
          stopped: stopped || undefined
        }
        if (this.chats.has(id)) {
          c.messages.push(answer)
          c.info.updatedAt = answer.createdAt
          handlers.onAnswer?.({ ...answer }, this.snapshot(c))
        }
        resolve()
      }
      const step = () => {
        const piece = full.slice(at, at + size)
        at += piece.length
        handlers.onDelta?.(piece)
        if (at >= full.length) finish(false)
        else timer = setTimeout(step, delay)
      }
      c.stop = () => finish(true)
      cancel = c.stop
      timer = setTimeout(step, delay)
    })
    return { done, cancel: () => cancel() }
  }

  async stop(sessionId: string, id: string) {
    const c = this.chat(sessionId, id)
    if (!c.stop) return false
    c.stop()
    return true
  }
}
