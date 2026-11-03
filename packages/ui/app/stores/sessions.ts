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
import type { SessionInfo } from '~/gen/harness/agent/v1/agent_pb'
import { createSessionState, permissionModeName, reduce, sessionMeta } from '~/lib/session/reducer'
import type { EffortName, PermissionModeName, QuestionAnswerVM, SessionState } from '~/lib/session/types'
import { browserAgentToken, createTransport, type ModelInfo, type SessionTransport } from '~/lib/transport'
import type { SessionPatch } from '~/lib/transport/types'
import { allowedModes } from '~/utils/composer'

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

// Event streams kept open at once (see trimStreams) and how often the list refreshes the other sessions.
const MAX_STREAMS = 4
const LIST_POLL_MS = 4000
// How often the permission mode policy is read again (an admin may change it while the app runs).
const MODES_POLL_MS = 30_000

// Errors that mean the client is unreachable rather than that a call was refused.
export const isNetworkError = (msg: string) => /unavailable|failed to fetch|load failed|networkerror|network error|connection (refused|reset|lost)/i.test(msg)

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
    // The client stopped answering (network error, process gone); the desktop app reconnects on this.
    connectionLost: false,
    userEmail: '',
    clientVersion: '',
    // The server's default session model (its main slot); empty when unknown.
    defaultModel: '',
    // The client's Shift+Tab cycle and the modes the server's policy disables (Initialize); disabled modes are
    // offered nowhere.
    permissionModes: [] as PermissionModeName[],
    disabledModes: [] as PermissionModeName[],
    // The signed-in account as the desktop shell reports it (whoami); empty on the web.
    account: { name: '', server: '', role: '' },
    models: [] as ModelInfo[],
    order: [] as string[],
    sessions: {} as Record<string, SessionState>,
    currentId: '',
    // Messages typed while a turn runs; sent one by one when the session becomes idle (✕ removes one).
    localQueue: {} as Record<string, LocalQueued[]>,
    effort: {} as Record<string, EffortName>,
    // Remote sessions the user chose to drive from here ("Continue via remote control"): the composer replaces
    // the "Running on <device>" notice while the session stays remote-controllable.
    remoteControl: {} as Record<string, boolean>,
    // Sessions with a compaction requested from here (button or /compact) that has not returned yet.
    compacting: {} as Record<string, boolean>,
    unsubscribers: {} as Record<string, () => void>
  }),

  getters: {
    current(state): SessionState | undefined {
      return state.sessions[state.currentId]
    },
    sessionList(state): SessionState[] {
      return state.order.map(id => state.sessions[id]!).filter(s => s && !s.meta.archived)
    },
    // The permission modes to offer (pickers, Settings), without the disabled ones.
    availableModes(state) {
      return allowedModes(state.disabledModes)
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
        this.clientVersion = init.clientVersion
        this.defaultModel = init.defaultModel
        this.applyModes(init)
        this.models = await this.transport.listModels()
        await this.refreshList()
        this.ready = true
        if (import.meta.client) {
          setInterval(() => this.quietly(this.refreshList()), LIST_POLL_MS)
          setInterval(() => this.refreshModes(), MODES_POLL_MS)
        }
      } catch (err) {
        this.error = err instanceof Error ? err.message : String(err)
      }
    },

    // Starts a session on the client and opens it; it goes to the top of the list. `forkOf` continues a copy of
    // that session's transcript (a remote session's is downloaded from the harness server: "Continue here").
    async createSession(opts: { cwd: string, model?: string, effort?: EffortName, mode?: PermissionModeName, worktree?: { name?: string, branch?: string, baseRef?: string }, forkOf?: string }) {
      if (!this.transport) throw new Error('not connected')
      const info = await this.transport.createSession({
        cwd: opts.cwd,
        model: opts.model,
        effort: opts.effort ? EFFORT_TO_PB[opts.effort] : undefined,
        permissionMode: opts.mode ? MODE_TO_PB[opts.mode] : undefined,
        worktree: opts.worktree,
        ...(opts.forkOf && { resumeSessionId: opts.forkOf, fork: true })
      })
      if (opts.effort) this.effort[info.id] = opts.effort
      this.sessions[info.id] = createSessionState(info.id, sessionMeta(info))
      this.order = [info.id, ...this.order.filter(id => id !== info.id)]
      this.open(info.id)
      return info.id
    },

    // The session list, and the state of sessions without an event stream (their status, title, marks). A busy
    // session that went idle while not open is left unread (a remote one only by its own machine).
    async refreshList() {
      if (!this.transport) return
      const list = await this.transport.listSessions()
      for (const info of list) {
        const s = this.sessions[info.id]
        if (!s) {
          this.sessions[info.id] = createSessionState(info.id, sessionMeta(info))
          continue
        }
        if (this.unsubscribers[info.id]) continue
        const wasBusy = s.meta.status === 'working' || s.meta.status === 'needs_input'
        s.meta = { ...s.meta, ...sessionMeta(info) }
        if (wasBusy && s.meta.status === 'idle' && this.currentId !== info.id && !s.meta.unread && !s.meta.remote) this.quietly(this.updateSession(info.id, { unread: true }))
      }
      this.order = list.map(i => i.id)
    },

    // Browsers allow about six HTTP/1.1 connections per host, and every event stream holds one; with a stream
    // per opened session, calls to the client would queue behind them. So only the open session, busy ones and
    // ones with queued messages keep a stream (at most MAX_STREAMS); the list poll covers the rest.
    trimStreams() {
      const needed = (id: string) => id === this.currentId || this.isBusy(id) || !!this.localQueue[id]?.length
      const ids = Object.keys(this.unsubscribers)
      const drop = ids.filter(id => !needed(id))
      const kept = ids.filter(id => needed(id) && id !== this.currentId)
      while (kept.length + (this.unsubscribers[this.currentId] ? 1 : 0) > MAX_STREAMS) drop.push(kept.shift()!)
      if (!drop.length) return
      const rest = { ...this.unsubscribers }
      for (const id of drop) {
        rest[id]?.()
        Reflect.deleteProperty(rest, id)
      }
      this.unsubscribers = rest
    },

    open(id: string, resumed = false) {
      const s = this.sessions[id]
      if (!s) return
      // A remote session without a stream shows a snapshot (or its relay stream ended): read it again from the start.
      if (s.meta.remote && !this.unsubscribers[id]) this.sessions[id] = createSessionState(id, s.meta)
      this.currentId = id
      if (this.sessions[id]!.meta.unread) this.quietly(this.updateSession(id, { unread: false }))
      this.trimStreams()
      this.subscribe(id, resumed)
    },

    // After the client reopened a session (its folder changed): drop the old stream and state, read it again from
    // the start with the new info.
    reopen(info: SessionInfo) {
      const id = info.id
      this.unsubscribers[id]?.()
      Reflect.deleteProperty(this.unsubscribers, id)
      this.sessions[id] = createSessionState(id, sessionMeta(info))
      this.subscribe(id)
    },

    // Opens the session's event stream (no-op while one is open).
    subscribe(id: string, resumed = false) {
      if (this.unsubscribers[id] || !this.transport || !this.sessions[id]) return
      const state = this.sessions[id]!
      this.unsubscribers[id] = this.transport.subscribe(id, state.lastSeq, {
        onEvent: (ev) => {
          const s = this.sessions[id]!
          const wasBusy = s.meta.status === 'working' || s.meta.status === 'needs_input'
          reduce(s, ev)
          if (wasBusy && s.meta.status === 'idle') this.flushQueue(id)
          // A turn that finished while another session was open leaves this one unread.
          if (ev.event.case === 'turnFinished' && this.currentId !== id && !s.meta.unread && !s.meta.remote) this.quietly(this.updateSession(id, { unread: true }))
          if (this.currentId !== id && s.meta.status === 'idle') this.trimStreams()
        },
        // The stream of a remote session ends after its server transcript (or when its relay stream closes); the
        // next open reads it again.
        onEnd: () => {
          Reflect.deleteProperty(this.unsubscribers, id)
        },
        onError: (err) => {
          Reflect.deleteProperty(this.unsubscribers, id)
          const msg = err instanceof Error ? err.message : String(err)
          if (this.sessions[id]?.meta.remote) {
            this.error = msg
            if (isNetworkError(msg)) this.connectionLost = true
            return
          }
          // A listed session that is not live on the client (closed, or the client restarted): resume it
          // from its transcript, then subscribe again. Tried once per open.
          if (!resumed && /not open|not_found|not found/i.test(msg)) {
            this.resume(id).catch((e) => {
              this.sessions[id]!.meta.status = 'waiting_for_connection'
              this.error = e instanceof Error ? e.message : String(e)
            })
            return
          }
          this.sessions[id]!.meta.status = 'waiting_for_connection'
          this.error = msg
          if (isNetworkError(msg)) this.connectionLost = true
        }
      })
    },

    // Drives a remote session from here through the relay: the composer replaces the notice, and a snapshot read
    // from the server is replaced by the live relay stream.
    continueRemotely(id: string) {
      const s = this.sessions[id]
      if (!s?.meta.remote || !s.meta.remoteControllable) return
      this.remoteControl[id] = true
      if (this.currentId === id && !this.unsubscribers[id]) this.open(id)
    },

    // A new session continuing from a copy of this one's transcript.
    async fork(id: string) {
      const s = this.sessions[id]
      if (!s || !this.transport) return
      const info = await this.transport.createSession({ cwd: s.meta.cwd, resumeSessionId: id, fork: true })
      this.sessions[info.id] = createSessionState(info.id, sessionMeta(info))
      this.order = [info.id, ...this.order.filter(x => x !== info.id)]
      this.open(info.id)
      return info.id
    },

    // Reports a failed background call (an unread mark) instead of throwing.
    quietly(p: Promise<unknown>) {
      p.catch((err) => {
        this.error = err instanceof Error ? err.message : String(err)
      })
    },

    // Per-user state (pinned, unread, archived, title) lives on the harness server: the change shows at once and
    // is rolled back when the client refuses it.
    async updateSession(id: string, patch: SessionPatch) {
      const s = this.sessions[id]
      if (!s || !this.transport) return
      const keys = (Object.keys(patch) as (keyof SessionPatch)[]).filter(k => patch[k] !== undefined)
      const before = Object.fromEntries(keys.map(k => [k, s.meta[k]]))
      Object.assign(s.meta, Object.fromEntries(keys.map(k => [k, patch[k]])))
      try {
        const info = await this.transport.updateSession(id, patch)
        Object.assign(s.meta, { pinned: info.pinned, unread: info.unread, archived: info.archived, title: info.title })
      } catch (err) {
        Object.assign(s.meta, before)
        throw err
      }
    },

    async setPinned(id: string, on: boolean) {
      await this.updateSession(id, { pinned: on })
    },

    async setUnread(id: string, on: boolean) {
      await this.updateSession(id, { unread: on })
    },

    async rename(id: string, title: string) {
      const t = title.trim()
      if (!t || t === this.sessions[id]?.meta.title) return
      await this.updateSession(id, { title: t })
    },

    // Stops following the session here (it is being archived or deleted).
    forget(id: string) {
      const { [id]: stop, ...rest } = this.unsubscribers
      stop?.()
      this.unsubscribers = rest
      if (this.currentId === id) this.currentId = ''
    },

    // Hides the session from the sidebar; the client stops it. Its transcript stays and it can be unarchived.
    async archive(id: string) {
      this.forget(id)
      await this.updateSession(id, { archived: true, pinned: false })
    },

    // Deletes the session for good (client and server); it comes back when the client refuses.
    async deleteSession(id: string) {
      if (!this.sessions[id] || !this.transport) return
      const at = this.order.indexOf(id)
      this.forget(id)
      this.order = this.order.filter(x => x !== id)
      try {
        await this.transport.deleteSession(id)
      } catch (err) {
        if (at >= 0 && !this.order.includes(id)) this.order = [...this.order.slice(0, at), id, ...this.order.slice(at)]
        throw err
      }
      Reflect.deleteProperty(this.sessions, id)
      Reflect.deleteProperty(this.localQueue, id)
      Reflect.deleteProperty(this.effort, id)
    },

    async resume(id: string) {
      const s = this.sessions[id]
      if (!s || !this.transport) return
      const info = await this.transport.createSession({ cwd: s.meta.cwd, resumeSessionId: id })
      Object.assign(s.meta, sessionMeta(info))
      if (this.currentId === id) this.open(id, true)
    },

    isBusy(id: string) {
      const st = this.sessions[id]?.meta.status
      return st === 'working' || st === 'needs_input'
    },

    // While a turn runs the message waits in the local queue; when idle it is sent right away. While a question
    // waits for an answer it goes to the client's queue instead: it does not end the wait, follows the answer
    // in the same turn (or becomes it with "Answer with message") and survives a reload.
    async send(id: string, text: string) {
      const body = text.trim()
      if (!body || !this.transport) return
      // The session gets a stream for the turn this starts, even when it is not the open one.
      this.subscribe(id)
      if (this.sessions[id]?.pendingQuestion) {
        await this.transport.sendUserMessage(id, newUuid(), body, SendUserMessageRequest_Delivery.QUEUE)
        return
      }
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

    // Sends a waiting message right away instead of after the turn: the client stops the turn (background
    // shell commands keep running) and the message starts the next one.
    async sendNow(id: string, queuedId: string) {
      const q = (this.localQueue[id] ?? []).find(x => x.id === queuedId)
      if (!q || !this.transport) return
      this.removeQueued(id, queuedId)
      this.subscribe(id)
      await this.transport.sendUserMessage(id, q.id, q.text, SendUserMessageRequest_Delivery.NOW)
    },

    async flushQueue(id: string) {
      const q = this.localQueue[id]
      if (!q?.length || !this.transport) return
      const next = q.shift()!
      await this.transport.sendUserMessage(id, next.id, next.text, SendUserMessageRequest_Delivery.QUEUE)
    },

    async interrupt(id: string) {
      try {
        await this.transport?.interrupt(id)
      } catch (err) {
        const msg = err instanceof Error ? err.message : String(err)
        this.error = msg
        if (isNetworkError(msg)) this.connectionLost = true
      }
    },

    async respondToPermission(id: string, requestId: string, decision: 'allow' | 'always' | 'deny', rule = '', comment = '') {
      const d = decision === 'allow'
        ? RespondToPermissionRequest_Decision.ALLOW
        : decision === 'always' ? RespondToPermissionRequest_Decision.ALLOW_ALWAYS : RespondToPermissionRequest_Decision.DENY
      await this.transport?.respondToPermission(id, requestId, d, rule, comment)
    },

    async answerQuestion(id: string, requestId: string, answers: QuestionAnswerVM[]) {
      await this.transport?.answerQuestion(id, requestId, { outcome: 'answered', answers })
    },

    // The model gets "the user did not answer, decide yourself".
    async skipQuestion(id: string, requestId: string) {
      await this.transport?.answerQuestion(id, requestId, { outcome: 'skipped' })
    },

    // Closes the question with the user's chat messages as the free-form answer: messages still in the local
    // queue are handed to the client first, then every queued message becomes the answer.
    async answerWithMessage(id: string, requestId: string) {
      if (!this.transport) return
      const local = this.localQueue[id] ?? []
      this.localQueue[id] = []
      for (const q of local) await this.transport.sendUserMessage(id, q.id, q.text, SendUserMessageRequest_Delivery.QUEUE)
      await this.transport.answerQuestion(id, requestId, { outcome: 'message' })
    },

    // Whether "Answer with message" has something to send.
    hasChatAnswer(id: string) {
      return !!this.localQueue[id]?.length || !!this.sessions[id]?.queue.length
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
      const prev = s?.meta.permissionMode
      if (s) s.meta.permissionMode = mode
      try {
        await this.transport?.setPermissionMode(id, MODE_TO_PB[mode])
      } catch (err) {
        // Refused (e.g. the mode was just disabled by the server's policy): show the mode the session is in.
        if (s && prev && s.meta.permissionMode === mode) s.meta.permissionMode = prev
        this.error = err instanceof Error ? err.message : String(err)
        await this.refreshModes()
      }
    },

    applyModes(init: { permissionModes: PermissionMode[], disabledPermissionModes: PermissionMode[] }) {
      this.permissionModes = init.permissionModes.map(permissionModeName)
      this.disabledModes = init.disabledPermissionModes.map(permissionModeName).filter(m => m !== 'default')
    },

    // Reads the permission mode policy again (Initialize); failures keep the last known one.
    async refreshModes() {
      if (!this.transport) return
      try {
        this.applyModes(await this.transport.initialize())
      } catch {
        // Unreachable for now; the next poll tries again.
      }
    },

    async suggestFiles(id: string, query: string) {
      return (await this.transport?.suggestFiles(id, query, 8)) ?? []
    },

    // Compacts now; the Compacted event adds the card to the transcript. Concurrent requests are dropped.
    async compact(id: string, instructions = '') {
      if (!this.transport || this.compacting[id]) return
      this.compacting[id] = true
      try {
        await this.transport.compact(id, instructions)
      } finally {
        this.compacting[id] = false
      }
    },

    async contextUsage(id: string) {
      if (!this.transport) throw new Error('not connected')
      return this.transport.getContextUsage(id)
    },

    async stopTask(id: string, taskId: string) {
      await this.transport?.stopTask(id, taskId)
    }
  }
})
