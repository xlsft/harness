// ConnectTransport: AgentService over connect-web (server streaming for Subscribe, unary for commands).
// The base URL comes from runtime config (NUXT_PUBLIC_AGENT_URL): `harness-client serve --listen 127.0.0.1:<port>`
// serves it. Every call carries `Authorization: Bearer <token>` when a token is given (see token.ts).

import { create } from '@bufbuild/protobuf'
import { createClient, type Client, type Interceptor } from '@connectrpc/connect'
import { createConnectTransport } from '@connectrpc/connect-web'
import {
  AgentService,
  type PermissionMode,
  type RespondToPermissionRequest_Decision,
  type RespondToPlanRequest_Decision,
  type SendUserMessageRequest_Delivery
} from '~/gen/harness/agent/v1/agent_pb'
import { ContentBlockSchema, type Effort } from '~/gen/harness/llm/v1/content_pb'
import type { ModelInfo, SessionTransport, SubscribeHandlers } from './types'

export class ConnectTransport implements SessionTransport {
  readonly kind = 'connect' as const
  private client: Client<typeof AgentService>

  constructor(baseUrl: string, token = '') {
    const interceptors: Interceptor[] = token ? [bearer(token)] : []
    this.client = createClient(AgentService, createConnectTransport({ baseUrl, interceptors }))
  }

  async initialize() {
    return this.client.initialize({ frontend: 'web', frontendVersion: '0.1.0' })
  }

  async listSessions() {
    return (await this.client.listSessions({})).sessions
  }

  async createSession(cwd: string, model = '') {
    const res = await this.client.createSession({ cwd, model })
    if (!res.session) throw new Error('the client returned no session')
    return res.session
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

  async interrupt(sessionId: string) {
    await this.client.interrupt({ sessionId })
  }

  async respondToPermission(sessionId: string, requestId: string, decision: RespondToPermissionRequest_Decision, rule = '', comment = '') {
    await this.client.respondToPermission({ sessionId, requestId, decision, rule, comment })
  }

  async answerQuestion(sessionId: string, requestId: string, answers: string[][]) {
    await this.client.answerQuestion({ sessionId, requestId, answers: answers.map(selected => ({ selected })) })
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
    return init.modelIds.map(id => ({ id, provider: '', contextWindow: 0, price: '' }))
  }
}

/** Adds the client's bearer token to every request (unary and streaming). */
export function bearer(token: string): Interceptor {
  return next => (req) => {
    req.header.set('Authorization', `Bearer ${token}`)
    return next(req)
  }
}
