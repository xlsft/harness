// ConnectTransport: AgentService over connect-web (server streaming for Subscribe, unary for commands).
// The base URL comes from runtime config (NUXT_PUBLIC_AGENT_URL): `harness-client serve --listen 127.0.0.1:<port>`
// serves it. Every call carries `Authorization: Bearer <token>` when a token is given (see token.ts).

import { create } from '@bufbuild/protobuf'
import { createClient, type Client, type Interceptor } from '@connectrpc/connect'
import { createConnectTransport } from '@connectrpc/connect-web'
import {
  AgentService,
  QuestionOutcome,
  type PermissionMode,
  type RespondToPermissionRequest_Decision,
  type RespondToPlanRequest_Decision,
  type SendUserMessageRequest_Delivery
} from '~/gen/harness/agent/v1/agent_pb'
import { ContentBlockSchema, Effort } from '~/gen/harness/llm/v1/content_pb'
import type { ModelInfo, NewSessionOptions, SessionPatch, SessionTransport, SubscribeHandlers } from './types'
import type { EffortName, QuestionReply } from '~/lib/session/types'
import { ConnectProvidersApi } from '~/lib/providers/connect'

const EFFORT_NAME: Partial<Record<Effort, EffortName>> = {
  [Effort.LOW]: 'low',
  [Effort.MEDIUM]: 'medium',
  [Effort.HIGH]: 'high',
  [Effort.XHIGH]: 'xhigh',
  [Effort.MAX]: 'max'
}

export class ConnectTransport implements SessionTransport {
  readonly kind = 'connect' as const
  private client: Client<typeof AgentService>

  private providersApi: ConnectProvidersApi

  constructor(baseUrl: string, token = '') {
    const interceptors: Interceptor[] = token ? [bearer(token)] : []
    const transport = createConnectTransport({ baseUrl, interceptors })
    this.client = createClient(AgentService, transport)
    this.providersApi = new ConnectProvidersApi(transport)
  }

  providers() {
    return this.providersApi
  }

  async initialize() {
    return this.client.initialize({ frontend: 'web', frontendVersion: '0.1.0' })
  }

  async listSessions() {
    return (await this.client.listSessions({})).sessions
  }

  async createSession(opts: NewSessionOptions) {
    const res = await this.client.createSession({
      cwd: opts.cwd,
      model: opts.model ?? '',
      effort: opts.effort,
      permissionMode: opts.permissionMode,
      resumeSessionId: opts.resumeSessionId ?? '',
      fork: opts.fork ?? false,
      worktree: opts.worktree ? { name: opts.worktree.name ?? '', branch: opts.worktree.branch ?? '', baseRef: opts.worktree.baseRef ?? '' } : undefined
    })
    if (!res.session) throw new Error('the client returned no session')
    return res.session
  }

  async listRecentFolders() {
    return (await this.client.listRecentFolders({})).folders
  }

  async listDirectory(path: string) {
    return this.client.listDirectory({ path })
  }

  async listBranches(cwd: string) {
    return this.client.listBranches({ cwd })
  }

  subscribe(sessionId: string, afterSeq: number, h: SubscribeHandlers) {
    const abort = new AbortController()
    void (async () => {
      try {
        for await (const res of this.client.subscribe({ sessionId, afterSeq: BigInt(afterSeq) }, { signal: abort.signal })) {
          if (res.event) h.onEvent(res.event)
        }
        h.onEnd?.()
      } catch (err) {
        if (!abort.signal.aborted) h.onError?.(err)
      }
    })()
    return () => abort.abort()
  }

  async sendUserMessage(sessionId: string, uuid: string, text: string, delivery: SendUserMessageRequest_Delivery) {
    const content = [create(ContentBlockSchema, { block: { case: 'text', value: { text } } })]
    await this.client.sendUserMessage({ sessionId, uuid, content, delivery })
  }

  async closeSession(sessionId: string) {
    await this.client.closeSession({ sessionId })
  }

  async updateSession(sessionId: string, patch: SessionPatch) {
    const res = await this.client.updateSession({ sessionId, ...patch })
    if (!res.session) throw new Error('the client returned no session')
    return res.session
  }

  async deleteSession(sessionId: string) {
    await this.client.deleteSession({ sessionId })
  }

  async interrupt(sessionId: string) {
    await this.client.interrupt({ sessionId })
  }

  async respondToPermission(sessionId: string, requestId: string, decision: RespondToPermissionRequest_Decision, rule = '', comment = '') {
    await this.client.respondToPermission({ sessionId, requestId, decision, rule, comment })
  }

  async answerQuestion(sessionId: string, requestId: string, reply: QuestionReply) {
    switch (reply.outcome) {
      case 'answered':
        await this.client.answerQuestion({ sessionId, requestId, outcome: QuestionOutcome.ANSWERED, answers: reply.answers })
        break
      case 'skipped':
        await this.client.answerQuestion({ sessionId, requestId, outcome: QuestionOutcome.SKIPPED })
        break
      case 'message':
        await this.client.answerQuestion({ sessionId, requestId, outcome: QuestionOutcome.MESSAGE, messageUuids: reply.messageUuids ?? [] })
    }
  }

  async respondToPlan(sessionId: string, requestId: string, decision: RespondToPlanRequest_Decision, feedback = '') {
    await this.client.respondToPlan({ sessionId, requestId, decision, feedback })
  }

  async setModel(sessionId: string, model: string) {
    await this.client.setModel({ sessionId, model })
  }

  async setEffort(sessionId: string, effort: Effort) {
    await this.client.setEffort({ sessionId, effort })
  }

  async setPermissionMode(sessionId: string, mode: PermissionMode) {
    await this.client.setPermissionMode({ sessionId, mode })
  }

  async suggestFiles(sessionId: string, query: string, limit: number) {
    return (await this.client.suggestFiles({ sessionId, query, limit })).paths
  }

  async compact(sessionId: string, instructions = '') {
    await this.client.compact({ sessionId, instructions })
  }

  async listTasks(sessionId: string) {
    return (await this.client.listTasks({ sessionId })).tasks
  }

  async stopTask(sessionId: string, taskId: string) {
    await this.client.stopTask({ sessionId, taskId })
  }

  async listModels(): Promise<ModelInfo[]> {
    const init = await this.initialize()
    // An older client sends ids only.
    if (!init.models.length) return init.modelIds.map(id => ({ id, provider: '', contextWindow: 0, price: '' }))
    const fmt = (n: number) => `$${Number(n.toFixed(2))}`
    return init.models.map(m => ({
      id: m.id,
      provider: m.provider,
      displayName: m.displayName,
      contextWindow: Number(m.contextWindow),
      price: m.inputPrice !== undefined && m.outputPrice !== undefined ? `${fmt(m.inputPrice)} / ${fmt(m.outputPrice)}` : '',
      efforts: m.efforts.map(e => EFFORT_NAME[e]).filter((e): e is EffortName => !!e),
      thinking: m.thinking
    }))
  }
}

/** Adds the client's bearer token to every request (unary and streaming). */
export function bearer(token: string): Interceptor {
  return next => (req) => {
    req.header.set('Authorization', `Bearer ${token}`)
    return next(req)
  }
}

// The store keeps the transport instance across hot updates, so a changed transport would keep running the
// old code (missing methods). Reload the page instead of hot-swapping this module.
if (import.meta.hot) import.meta.hot.accept(() => window.location.reload())
