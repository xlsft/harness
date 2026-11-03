// SideChatsApi over AgentService's side chat RPCs, on the same connect transport (and agent token) as the rest of
// AgentService.

import { createClient, type Client, type Transport } from '@connectrpc/connect'
import { AgentService } from '~/gen/harness/agent/v1/agent_pb'
import type { SideChatInfo as SideChatInfoPb, SideChatMessage as SideChatMessagePb } from '~/gen/harness/agent/v1/sidechat_pb'
import { Effort } from '~/gen/harness/llm/v1/content_pb'
import type { EffortName } from '~/lib/session/types'
import type { AskHandle, AskHandlers, AskSideChatOptions, CreateSideChatOptions, SideChatInfo, SideChatMessage, SideChatsApi, UpdateSideChatPatch } from './types'

const EFFORT_PB: Record<EffortName, Effort> = { low: Effort.LOW, medium: Effort.MEDIUM, high: Effort.HIGH, xhigh: Effort.XHIGH, max: Effort.MAX }
const EFFORT_NAME: Partial<Record<Effort, EffortName>> = { [Effort.LOW]: 'low', [Effort.MEDIUM]: 'medium', [Effort.HIGH]: 'high', [Effort.XHIGH]: 'xhigh', [Effort.MAX]: 'max' }

export const effortPb = (e?: EffortName) => (e ? EFFORT_PB[e] : Effort.UNSPECIFIED)

export function sideChatView(c: SideChatInfoPb | undefined): SideChatInfo {
  if (!c) throw new Error('the client returned no side chat')
  return {
    id: c.sideChatId,
    sessionId: c.sessionId,
    title: c.title,
    model: c.model,
    effort: EFFORT_NAME[c.effort],
    createdAt: c.createdAt,
    updatedAt: c.updatedAt,
    messageCount: c.messageCount,
    answering: c.answering
  }
}

export function sideMessageView(m: SideChatMessagePb): SideChatMessage {
  return {
    id: m.messageId,
    role: m.role === 'assistant' ? 'assistant' : 'user',
    text: m.text,
    quote: m.quote || undefined,
    model: m.model || undefined,
    effort: EFFORT_NAME[m.effort],
    usage: m.usage && {
      inputTokens: Number(m.usage.inputTokens),
      outputTokens: Number(m.usage.outputTokens),
      cacheReadInputTokens: Number(m.usage.cacheReadInputTokens),
      cacheCreationInputTokens: Number(m.usage.cacheCreationInputTokens)
    },
    createdAt: m.createdAt,
    stopped: m.stopped || undefined,
    error: m.error || undefined
  }
}

export class ConnectSideChatsApi implements SideChatsApi {
  private client: Client<typeof AgentService>

  constructor(transport: Transport) {
    this.client = createClient(AgentService, transport)
  }

  async list(sessionId: string) {
    return (await this.client.listSideChats({ sessionId })).sideChats.map(sideChatView)
  }

  async create(sessionId: string, opts: CreateSideChatOptions = {}) {
    const res = await this.client.createSideChat({ sessionId, title: opts.title ?? '', model: opts.model ?? '', effort: effortPb(opts.effort) })
    return sideChatView(res.sideChat)
  }

  async get(sessionId: string, id: string) {
    const res = await this.client.getSideChat({ sessionId, sideChatId: id })
    return { chat: sideChatView(res.sideChat), messages: res.messages.map(sideMessageView) }
  }

  async update(sessionId: string, id: string, patch: UpdateSideChatPatch) {
    const res = await this.client.updateSideChat({
      sessionId,
      sideChatId: id,
      title: patch.title,
      model: patch.model,
      effort: patch.effort ? EFFORT_PB[patch.effort] : undefined
    })
    return sideChatView(res.sideChat)
  }

  async remove(sessionId: string, id: string) {
    await this.client.deleteSideChat({ sessionId, sideChatId: id })
  }

  ask(sessionId: string, id: string, opts: AskSideChatOptions, handlers: AskHandlers): AskHandle {
    const abort = new AbortController()
    const done = (async () => {
      const stream = this.client.askSideChat({
        sessionId,
        sideChatId: id,
        text: opts.text,
        quote: opts.quote ?? '',
        model: opts.model ?? '',
        effort: effortPb(opts.effort)
      }, { signal: abort.signal })
      try {
        for await (const res of stream) {
          const ev = res.event
          if (ev.case === 'question') handlers.onQuestion?.(sideMessageView(ev.value))
          else if (ev.case === 'delta') handlers.onDelta?.(ev.value)
          else if (ev.case === 'answer') handlers.onAnswer?.(sideMessageView(ev.value), res.sideChat && sideChatView(res.sideChat))
        }
      } catch (err) {
        if (!abort.signal.aborted) throw err
      }
    })()
    return { done, cancel: () => abort.abort() }
  }

  async stop(sessionId: string, id: string) {
    return (await this.client.stopSideChat({ sessionId, sideChatId: id })).stopped
  }
}
