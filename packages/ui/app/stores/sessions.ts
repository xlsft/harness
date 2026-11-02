// Session store: one SessionState per session, fed by the transport's Subscribe stream through the reducer.

import { defineStore } from 'pinia'
import { markRaw } from 'vue'
import {
  PermissionMode,
  RespondToPermissionRequest_Decision,
  RespondToPlanRequest_Decision,
  SendUserMessageRequest_Delivery
} from '~/gen/harness/agent/v1/agent_pb'
import { Effort } from '~/gen/harness/llm/v1/content_pb'
import { createSessionState, reduce, sessionMeta } from '~/lib/session/reducer'
import type { EffortName, PermissionModeName, SessionState } from '~/lib/session/types'
import { browserAgentToken, createTransport, type ModelInfo, type SessionTransport } from '~/lib/transport'

const MODE_TO_PB: Record<PermissionModeName, PermissionMode> = {
  default: PermissionMode.DEFAULT,
  accept_edits: PermissionMode.ACCEPT_EDITS,
  plan: PermissionMode.PLAN,
  dont_ask: PermissionMode.DONT_ASK,
  bypass: PermissionMode.BYPASS_PERMISSIONS,
  auto: PermissionMode.AUTO
}

const EFFORT_TO_PB: Record<EffortName, Effort> = {
  low: Effort.LOW,
  medium: Effort.MEDIUM,
  high: Effort.HIGH,
  xhigh: Effort.XHIGH,
  max: Effort.MAX
}

let uid = 0
export function newUuid() {
  uid += 1
  return (globalThis.crypto?.randomUUID?.() ?? `ui-${Date.now()}-${uid}`)
}

export interface LocalQueued {
  id: string
  text: string
}

export const useSessionStore = defineStore('sessions', {
  state: () => ({
    transport: null as SessionTransport | null,
    ready: false,
    error: '',
    userEmail: '',
    models: [] as ModelInfo[],
    order: [] as string[],
    sessions: {} as Record<string, SessionState>,
    currentId: '',
    // Messages typed while a turn runs; sent one by one when the session becomes idle (✕ removes one).
    localQueue: {} as Record<string, LocalQueued[]>,
    effort: {} as Record<string, EffortName>,
    unsubscribers: {} as Record<string, () => void>
  }),

  getters: {
    current(state): SessionState | undefined {
      return state.sessions[state.currentId]
    },
    sessionList(state): SessionState[] {
      return state.order.map(id => state.sessions[id]!).filter(Boolean)
    }
  },

  actions: {
    async init(transport?: SessionTransport) {
      if (this.ready) return
      const config = useRuntimeConfig()
      this.transport = markRaw(transport ?? createTransport(
        String(config.public.transport),
        String(config.public.agentUrl),
        browserAgentToken(String(config.public.agentToken ?? ''))
      ))
      try {
        const init = await this.transport.initialize()
        this.userEmail = init.userEmail
        this.models = await this.transport.listModels()
        const list = await this.transport.listSessions()
        for (const info of list) {
          if (!this.sessions[info.id]) this.sessions[info.id] = createSessionState(info.id, sessionMeta(info))
        }
        this.order = list.map(i => i.id)
        this.ready = true
      } catch (err) {
        this.error = err instanceof Error ? err.message : String(err)
      }
    },

    // Starts a session on the client and opens it; it goes to the top of the list.
    async createSession(opts: { cwd: string, model?: string, effort?: EffortName, mode?: PermissionModeName, worktree?: { name?: string, branch?: string, baseRef?: string } }) {
      if (!this.transport) throw new Error('not connected')
      const info = await this.transport.createSession({
        cwd: opts.cwd,
        model: opts.model,
        effort: opts.effort ? EFFORT_TO_PB[opts.effort] : undefined,
        permissionMode: opts.mode ? MODE_TO_PB[opts.mode] : undefined,
        worktree: opts.worktree
      })
      if (opts.effort) this.effort[info.id] = opts.effort
      this.sessions[info.id] = createSessionState(info.id, sessionMeta(info))
      this.order = [info.id, ...this.order.filter(id => id !== info.id)]
      this.open(info.id)
      return info.id
    },

    open(id: string) {
      if (!this.sessions[id]) return
      this.currentId = id
      if (this.unsubscribers[id] || !this.transport) return
      const state = this.sessions[id]!
      this.unsubscribers[id] = this.transport.subscribe(id, state.lastSeq, {
        onEvent: (ev) => {
          const s = this.sessions[id]!
          const wasBusy = s.meta.status === 'working' || s.meta.status === 'needs_input'
          reduce(s, ev)
          if (wasBusy && s.meta.status === 'idle') this.flushQueue(id)
        },
        onError: (err) => {
          this.sessions[id]!.meta.status = 'waiting_for_connection'
          this.error = err instanceof Error ? err.message : String(err)
          Reflect.deleteProperty(this.unsubscribers, id)
        }
      })
    },

    isBusy(id: string) {
      const st = this.sessions[id]?.meta.status
      return st === 'working' || st === 'needs_input'
    },

    // While a turn runs the message waits in the local queue; when idle it is sent right away.
    async send(id: string, text: string) {
      const body = text.trim()
      if (!body || !this.transport) return
      if (this.isBusy(id)) {
        const q = this.localQueue[id] ?? (this.localQueue[id] = [])
        q.push({ id: newUuid(), text: body })
        return
      }
      await this.transport.sendUserMessage(id, newUuid(), body, SendUserMessageRequest_Delivery.QUEUE)
    },

    removeQueued(id: string, queuedId: string) {
      this.localQueue[id] = (this.localQueue[id] ?? []).filter(q => q.id !== queuedId)
    },

    async flushQueue(id: string) {
      const q = this.localQueue[id]
      if (!q?.length || !this.transport) return
      const next = q.shift()!
      await this.transport.sendUserMessage(id, next.id, next.text, SendUserMessageRequest_Delivery.QUEUE)
    },

    async interrupt(id: string) {
      await this.transport?.interrupt(id)
    },

    async respondToPermission(id: string, requestId: string, decision: 'allow' | 'always' | 'deny', rule = '', comment = '') {
      const d = decision === 'allow'
        ? RespondToPermissionRequest_Decision.ALLOW
        : decision === 'always' ? RespondToPermissionRequest_Decision.ALLOW_ALWAYS : RespondToPermissionRequest_Decision.DENY
      await this.transport?.respondToPermission(id, requestId, d, rule, comment)
    },

    async answerQuestion(id: string, requestId: string, answers: string[][]) {
      await this.transport?.answerQuestion(id, requestId, answers)
    },

    async respondToPlan(id: string, requestId: string, decision: 'accept_edits' | 'manual' | 'keep_planning', feedback = '') {
      const d = decision === 'accept_edits'
        ? RespondToPlanRequest_Decision.APPROVE_ACCEPT_EDITS
        : decision === 'manual' ? RespondToPlanRequest_Decision.APPROVE_MANUAL : RespondToPlanRequest_Decision.KEEP_PLANNING
      await this.transport?.respondToPlan(id, requestId, d, feedback)
    },

    async setModel(id: string, model: string) {
      const s = this.sessions[id]
      if (s) s.meta.model = model
      await this.transport?.setModel(id, model)
    },

    async setEffort(id: string, effort: EffortName) {
      this.effort[id] = effort
      await this.transport?.setEffort(id, EFFORT_TO_PB[effort])
    },

    async setPermissionMode(id: string, mode: PermissionModeName) {
      const s = this.sessions[id]
      if (s) s.meta.permissionMode = mode
      await this.transport?.setPermissionMode(id, MODE_TO_PB[mode])
    },

    async suggestFiles(id: string, query: string) {
      return (await this.transport?.suggestFiles(id, query, 8)) ?? []
    },

    async compact(id: string) {
      await this.transport?.compact(id)
    },

    async stopTask(id: string, taskId: string) {
      await this.transport?.stopTask(id, taskId)
    }
  }
})
