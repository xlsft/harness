// Session store: one SessionState per session, fed by the transport's Subscribe stream through the reducer.

import { defineStore } from 'pinia'
import { markRaw, toRaw } from 'vue'
import {
  PermissionMode,
  RespondToPermissionRequest_Decision,
  RespondToPlanRequest_Decision,
  RewindRequest_Mode,
  SendUserMessageRequest_Delivery
} from '~/gen/harness/agent/v1/agent_pb'
import type { SessionEvent, SessionInfo } from '~/gen/harness/agent/v1/agent_pb'
import { Effort } from '~/gen/harness/llm/v1/content_pb'
import { applyLiveInfo, createSessionState, permissionModeName, reduce, sessionMeta, settleTurn } from '~/lib/session/reducer'
import type { EffortName, PermissionModeName, QuestionAnswerVM, SessionState } from '~/lib/session/types'
import { liveOutbox, type OutboxEntry } from '~/lib/session/delivery'
import { descendants } from '~/lib/session/tree'
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
// Stream liveness. A stream that sent control messages beats every 15 s: one silent for STALL_MS is dead (a sleep,
// a WebView fetch that stopped delivering, a proxy) and is opened again after lastSeq. A broken stream is retried
// with backoff up to RETRY_MAX_MS. A busy session the list has seen idle for RECONCILE_MS, or still busy
// INTERRUPT_SETTLE_MS after Stop, gets its stream opened again too: the stream's first message after the replay
// carries the client's live status, which wins.
export const STALL_MS = 35_000
const RETRY_MIN_MS = 1000
const RETRY_MAX_MS = 15_000
export const RECONCILE_MS = 6000
export const INTERRUPT_SETTLE_MS = 4000
// A replay that never says it is over is shown anyway after this long.
const REPLAY_MAX_MS = 20_000

// A plain deep copy of a session state, to reduce a replay into without rendering every event.
function copyState(s: SessionState): SessionState {
  const raw = toRaw(s)
  try {
    return structuredClone(raw)
  } catch {
    return JSON.parse(JSON.stringify(raw)) as SessionState
  }
}

// Per-stream bookkeeping kept out of the reactive state.
const watchdogs = new Map<string, ReturnType<typeof setTimeout>>()
const resuming = new Map<string, Promise<void>>()
const retries = new Map<string, { attempt: number, timer?: ReturnType<typeof setTimeout> }>()
const listedIdleSince = new Map<string, number>()
// Marks the current stream of a session as replaced (restartStream): it may deliver no more events.
const replacers = new Map<string, () => void>()

function clearWatchdog(id: string) {
  const t = watchdogs.get(id)
  if (t) clearTimeout(t)
  watchdogs.delete(id)
}

function clearRetry(id: string) {
  const r = retries.get(id)
  if (r?.timer) clearTimeout(r.timer)
  retries.delete(id)
}

// Errors that mean the client is unreachable rather than that a call was refused.
export const isNetworkError = (msg: string) => /unavailable|failed to fetch|load failed|networkerror|network error|connection (refused|reset|lost)/i.test(msg)

let uid = 0
// The message a code block's Run sends once its command finished; UserMessage shows it as a quiet line.
export const RAN_PREFIX = '[ran the command] '
const INLINE_KEY = 'harness-inline-shells'
function loadInlineShells(): Record<string, { command: string, done: boolean }> {
  try {
    return JSON.parse(localStorage.getItem(INLINE_KEY) ?? '{}')
  } catch {
    return {}
  }
}
function saveInlineShells(v: Record<string, { command: string, done: boolean }>) {
  try {
    // The last 200 are enough to keep their cards out of the transcript.
    localStorage.setItem(INLINE_KEY, JSON.stringify(Object.fromEntries(Object.entries(v).slice(-200))))
  } catch { /* storage unavailable */ }
}

export function newUuid() {
  uid += 1
  return (globalThis.crypto?.randomUUID?.() ?? `ui-${Date.now()}-${uid}`)
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
    // Messages handed to the client (SendUserMessage) until the stream shows them queued or read (delivery.ts).
    outbox: {} as Record<string, OutboxEntry[]>,
    // Text to put into a session's composer (a rewound or forked message, a queued message taken back to edit);
    // the composer takes it and clears the entry.
    drafts: {} as Record<string, string>,
    effort: {} as Record<string, EffortName>,
    // Remote sessions the user chose to drive from here ("Continue via remote control"): the composer replaces
    // the "Running on <device>" notice while the session stays remote-controllable.
    remoteControl: {} as Record<string, boolean>,
    // Sessions with a compaction requested from here (button or /compact) that has not returned yet.
    compacting: {} as Record<string, boolean>,
    unsubscribers: {} as Record<string, () => void>,
    // Shell commands run from a code block (runShell inline): command id → command, and whether the agent was
    // already asked to go on (a replayed finish must not ask again).
    inlineShells: loadInlineShells()
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
        if (this.unsubscribers[info.id]) {
          this.reconcile(info.id, sessionMeta(info).status)
          continue
        }
        const wasBusy = s.meta.status === 'working' || s.meta.status === 'needs_input'
        s.meta = { ...s.meta, ...sessionMeta(info) }
        if (wasBusy && s.meta.status === 'idle' && this.currentId !== info.id && !s.meta.unread && !s.meta.remote) this.quietly(this.updateSession(info.id, { unread: true }))
      }
      this.order = list.map(i => i.id)
    },

    // A streamed session shows a running turn the client lists as not running: tolerated for a moment (the list
    // and the stream race), then the stream is opened again to catch up.
    reconcile(id: string, listed: string) {
      const s = this.sessions[id]
      const listedBusy = listed === 'working' || listed === 'needs_input'
      if (!s || s.meta.remote || !this.isBusy(id) || listedBusy) {
        listedIdleSince.delete(id)
        return
      }
      const since = listedIdleSince.get(id)
      if (since === undefined) {
        listedIdleSince.set(id, Date.now())
        return
      }
      if (Date.now() - since < RECONCILE_MS) return
      listedIdleSince.delete(id)
      this.restartStream(id)
    },

    // Opens the session's stream again (after lastSeq, or from the start with `reset`, for another event log).
    restartStream(id: string, reset = false) {
      replacers.get(id)?.()
      replacers.delete(id)
      const stop = this.unsubscribers[id]
      if (stop) {
        stop()
        const rest = { ...this.unsubscribers }
        Reflect.deleteProperty(rest, id)
        this.unsubscribers = rest
      }
      const s = this.sessions[id]
      if (!s) return
      if (reset) this.sessions[id] = createSessionState(id, s.meta)
      this.subscribe(id)
    },

    // A broken stream of a session that still needs one is opened again, with backoff.
    retryStream(id: string) {
      const r = retries.get(id) ?? { attempt: 0 }
      if (r.timer) clearTimeout(r.timer)
      const delay = Math.min(RETRY_MIN_MS * 2 ** r.attempt, RETRY_MAX_MS)
      r.attempt += 1
      r.timer = setTimeout(() => {
        r.timer = undefined
        const needed = id === this.currentId || this.isBusy(id)
        if (needed && this.sessions[id] && !this.unsubscribers[id]) this.subscribe(id)
      }, delay)
      retries.set(id, r)
    },

    // Browsers allow about six HTTP/1.1 connections per host, and every event stream holds one; with a stream
    // per opened session, calls to the client would queue behind them. So only the open session and busy ones keep
    // a stream (at most MAX_STREAMS); the list poll covers the rest.
    trimStreams() {
      const needed = (id: string) => id === this.currentId || this.isBusy(id)
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
      // done: the stream ended or was replaced (restartStream), so whatever it still delivers is ignored. stopped:
      // it was closed here (also when trimmed); events still in flight are harmless, but no watchdog or retry.
      let done = false
      let stopped = false
      // Control messages say when the replay is over (caughtUp); a stream without them never says it.
      let caughtUp = false
      let unsubscribe = () => {}
      replacers.set(id, () => {
        done = true
      })
      const end = () => {
        done = true
        stopped = true
        clearWatchdog(id)
        if (this.unsubscribers[id] === unsubscribe) Reflect.deleteProperty(this.unsubscribers, id)
      }
      // Any message proves the stream alive; once it sent control messages (so it beats), silence ends it.
      let beats = false
      const alive = () => {
        if (!beats || stopped) return
        clearWatchdog(id)
        watchdogs.set(id, setTimeout(() => {
          if (!stopped) this.restartStream(id)
        }, STALL_MS))
      }
      // A first open replays the whole history: it is reduced into a plain copy (no rendering per event) and shown
      // at once when the client says the replay is over (or after REPLAY_MAX_MS, or when the stream ends), so the
      // transcript never shows the intermediate states of old turns. Meanwhile the session shows as loading.
      let draft: SessionState | null = null
      let draftTimer: ReturnType<typeof setTimeout> | undefined
      const flush = () => {
        if (draftTimer) clearTimeout(draftTimer)
        draftTimer = undefined
        if (!draft) return
        const d = draft
        draft = null
        d.loading = false
        this.sessions[id] = d
      }
      // Only streams that say when the replay is over (control messages, the first one before the replay) buffer.
      const startDraft = () => {
        const cur = this.sessions[id]!
        if (draft || caughtUp || cur.lastSeq !== 0 || cur.items.length) return
        draft = copyState(cur)
        cur.loading = true
        draftTimer = setTimeout(flush, REPLAY_MAX_MS)
      }
      const live = () => draft ?? this.sessions[id]!
      const stop = this.transport.subscribe(id, state.lastSeq, {
        onControl: (c) => {
          if (done || stopped) return
          const s = live()
          beats = true
          alive()
          // Seqs count in one event log; a client that reopened the session (it restarted) has started another.
          if (s.logId ? c.logId !== s.logId : s.lastSeq > 0) {
            this.restartStream(id, true)
            return
          }
          s.logId = c.logId
          if (!c.session) {
            startDraft()
            return
          }
          caughtUp = true
          clearRetry(id)
          applyLiveInfo(s, c.session)
          flush()
          if (this.currentId !== id && s.meta.status === 'idle') this.trimStreams()
        },
        onEvent: (ev) => {
          if (done) return
          alive()
          const s = live()
          reduce(s, ev)
          this.pruneOutbox(id, ev)
          // A turn that finished while another session was open leaves this one unread.
          if (ev.event.case === 'turnFinished' && this.currentId !== id && !s.meta.unread && !s.meta.remote) this.quietly(this.updateSession(id, { unread: true }))
          // Not while the replay runs: an idle status from history would drop the stream before the live turn.
          if (this.currentId !== id && s.meta.status === 'idle' && (caughtUp || !beats)) this.trimStreams()
        },
        // The stream of a remote session ends after its server transcript (or when its relay stream closes); the
        // next open reads it again.
        onEnd: () => {
          flush()
          if (!stopped) end()
        },
        onError: (err) => {
          flush()
          if (stopped) return
          end()
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
              this.retryStream(id)
            })
            return
          }
          // Any other break (the client went away, a proxy cut the stream): try again, after lastSeq.
          this.sessions[id]!.meta.status = 'waiting_for_connection'
          this.error = msg
          if (isNetworkError(msg)) this.connectionLost = true
          this.retryStream(id)
        }
      })
      unsubscribe = () => {
        flush()
        stopped = true
        clearWatchdog(id)
        stop()
      }
      this.unsubscribers[id] = unsubscribe
    },

    // Drives a remote session from here through the relay: the composer replaces the notice, and a snapshot read
    // from the server is replaced by the live relay stream.
    continueRemotely(id: string) {
      const s = this.sessions[id]
      if (!s?.meta.remote || !s.meta.remoteControllable) return
      this.remoteControl[id] = true
      if (this.currentId === id && !this.unsubscribers[id]) this.open(id)
    },

    // A new sub-session continuing from a copy of this one's transcript (the client names the parent and titles it
    // "<parent title> (fork)"). `at` forks before that user message instead of at the end; `draft` goes into the
    // fork's composer (the message it was forked from, to send again or edit).
    async fork(id: string, opts: { at?: string, draft?: string } = {}) {
      const s = this.sessions[id]
      if (!s || !this.transport) return
      const info = await this.transport.createSession({ cwd: s.meta.cwd, resumeSessionId: id, fork: true, ...(opts.at && { forkAtMessageId: opts.at }) })
      this.sessions[info.id] = createSessionState(info.id, sessionMeta(info))
      this.order = [info.id, ...this.order.filter(x => x !== info.id)]
      if (opts.draft) this.drafts[info.id] = opts.draft
      this.open(info.id)
      return info.id
    },

    // Makes a sub-session a top-level session (the client records it and syncs it).
    async detach(id: string) {
      const s = this.sessions[id]
      if (!s || !this.transport || !s.meta.parentId) return
      const before = s.meta.parentId
      s.meta.parentId = ''
      try {
        const info = await this.transport.updateSession(id, { detachFromParent: true })
        s.meta.parentId = info.parentSessionId
      } catch (err) {
        s.meta.parentId = before
        throw err
      }
    },

    // Sub-sessions of a session (any depth) that are listed.
    childrenOf(id: string) {
      return descendants(this.order.map(x => this.sessions[x]!).filter(Boolean), id)
    },

    // Reports a failed background call (an unread mark) instead of throwing.
    quietly(p: Promise<unknown>) {
      p.catch((err) => {
        this.error = err instanceof Error ? err.message : String(err)
      })
    },

    // Per-user state (pinned, unread, archived, title) lives on the harness server: the change shows at once and
    // is rolled back when the client refuses it.
    // (Detaching a sub-session goes through detach.)
    async updateSession(id: string, patch: Omit<SessionPatch, 'detachFromParent'>) {
      const s = this.sessions[id]
      if (!s || !this.transport) return
      const keys = (Object.keys(patch) as (keyof typeof patch)[]).filter(k => patch[k] !== undefined)
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
      clearRetry(id)
      listedIdleSince.delete(id)
      const { [id]: stop, ...rest } = this.unsubscribers
      stop?.()
      this.unsubscribers = rest
      if (this.currentId === id) this.currentId = ''
    },

    // Hides the session from the sidebar; the client stops it. Its transcript stays and it can be unarchived.
    // Its sub-sessions are archived with it (they belong to its work; unarchiving brings each back).
    async archive(id: string) {
      const children = this.childrenOf(id).filter(c => !this.sessions[c]?.meta.archived)
      this.forget(id)
      await this.updateSession(id, { archived: true, pinned: false })
      for (const c of children) {
        this.forget(c)
        await this.updateSession(c, { archived: true, pinned: false })
      }
    },

    // Deletes the session for good (client and server); it comes back when the client refuses.
    // Its direct sub-sessions are detached first and stay as top-level sessions: deleting cannot be undone, so it
    // never takes other conversations along.
    async deleteSession(id: string) {
      if (!this.sessions[id] || !this.transport) return
      for (const c of this.order.filter(x => this.sessions[x]?.meta.parentId === id)) await this.detach(c)
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
      Reflect.deleteProperty(this.outbox, id)
      Reflect.deleteProperty(this.effort, id)
    },

    // Runs a call that needs the session open on the client: a listed session that is not (the client restarted) is
    // reopened first, once.
    async whenOpen<T>(id: string, call: () => Promise<T>): Promise<T> {
      try {
        return await call()
      } catch (e) {
        if (!/not open/i.test(e instanceof Error ? e.message : String(e))) throw e
        await this.resume(id)
        return call()
      }
    },

    // Reopens a listed session on the client (callers at the same moment share one reopen).
    resume(id: string) {
      const running = resuming.get(id)
      if (running) return running
      const p = this.doResume(id).finally(() => resuming.delete(id))
      resuming.set(id, p)
      return p
    },

    async doResume(id: string) {
      const s = this.sessions[id]
      if (!s || !this.transport) return
      const info = await this.transport.createSession({ cwd: s.meta.cwd, resumeSessionId: id })
      // Reopened, the session is idle whatever this state showed (a turn cut short by a client restart).
      applyLiveInfo(s, info)
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
      // Always to the client: it decides when the agent reads it (queues it, moves a command to the background, ...).
      await this.deliver(id, newUuid(), body, SendUserMessageRequest_Delivery.QUEUE)
    },

    // Hands a message to the client, tracked in the outbox (sending, then sent) until the stream shows it queued
    // or read. A refused message leaves the outbox and the error is thrown.
    async deliver(id: string, uuid: string, text: string, delivery: SendUserMessageRequest_Delivery) {
      if (!this.transport) return
      const box = this.outbox[id] ?? (this.outbox[id] = [])
      box.push({ id: uuid, text, at: Date.now(), state: 'sending' })
      try {
        await this.transport.sendUserMessage(id, uuid, text, delivery)
        const e = this.outbox[id]?.find(o => o.id === uuid)
        if (e) e.state = 'sent'
      } catch (err) {
        this.outbox[id] = (this.outbox[id] ?? []).filter(o => o.id !== uuid)
        throw err
      }
    },

    // Drops outbox entries the agent has read. A question answered with the queued messages takes them without a
    // UserMessage, so handed-over messages that wait in no queue leave then too.
    pruneOutbox(id: string, ev: SessionEvent) {
      const box = this.outbox[id]
      const s = this.sessions[id]
      if (!box?.length || !s) return
      let kept = liveOutbox(s, box)
      if (ev.event.case === 'questionResolved') kept = kept.filter(o => o.state === 'sending' || s.queue.some(q => q.id === o.id))
      if (kept.length !== box.length) this.outbox[id] = kept
    },

    // Cancels a waiting message: from this browser's queue, or taken back from the client's queue (false when the
    // agent already took it).
    async removeQueued(id: string, queuedId: string) {
      if (!this.sessions[id]?.queue.some(q => q.id === queuedId) || !this.transport) return false
      const removed = await this.transport.removeQueuedMessage(id, queuedId)
      if (removed) this.outbox[id] = (this.outbox[id] ?? []).filter(o => o.id !== queuedId)
      return removed
    },
    async editQueued(id: string, queuedId: string) {
      const text = this.sessions[id]?.queue.find(q => q.id === queuedId)?.text
      if (text === undefined) return
      if (await this.removeQueued(id, queuedId)) this.drafts[id] = text
    },
    // Asks the client to deliver a queued message now (it moves a running command to the background, or stops the turn).
    async sendNow(id: string, queuedId: string) {
      const q = this.sessions[id]?.queue.find(x => x.id === queuedId)
      if (!q || !this.transport) return
      this.subscribe(id)
      await this.transport.sendUserMessage(id, q.id, q.text, SendUserMessageRequest_Delivery.NOW)
    },

    // Rewinds code and conversation to just before a user message (the checkpoint of its turn); the message comes
    // back into the composer. A message without file checkpoints (injected into a running turn, or a session
    // without file history) rewinds the conversation only.
    async rewind(id: string, messageId: string) {
      if (!this.transport) return
      let res: { restoredPrompt: string }
      try {
        res = await this.transport.rewind(id, messageId, RewindRequest_Mode.CODE_AND_CONVERSATION)
      } catch (err) {
        const msg = err instanceof Error ? err.message : String(err)
        if (/in progress|busy|not open|not found/i.test(msg)) throw err
        res = await this.transport.rewind(id, messageId, RewindRequest_Mode.CONVERSATION)
      }
      if (res.restoredPrompt) this.drafts[id] = res.restoredPrompt
    },

    // Stop always ends in a state the client agrees with: nothing running (idle, or the session is not live on the
    // client) settles the turn here; a turn that does not end soon gets its stream opened again, whose live status
    // then wins.
    async interrupt(id: string) {
      if (!this.transport) return
      try {
        const res = await this.transport.interrupt(id)
        const s = this.sessions[id]
        if (!s) return
        if (res?.idle) {
          settleTurn(s)
          return
        }
        setTimeout(() => {
          if (this.sessions[id] === s && this.isBusy(id) && !s.pendingPermission && !s.pendingQuestion && !s.pendingPlan) this.restartStream(id)
        }, INTERRUPT_SETTLE_MS)
      } catch (err) {
        const msg = err instanceof Error ? err.message : String(err)
        const s = this.sessions[id]
        if (s && !s.meta.remote && /not open|not_found|not found/i.test(msg)) {
          settleTurn(s)
          return
        }
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

    // Closes the question with the user's chat messages as the free-form answer: every message the client has queued.
    async answerWithMessage(id: string, requestId: string) {
      if (!this.transport) return
      await this.transport.answerQuestion(id, requestId, { outcome: 'message' })
    },

    // Whether "Answer with message" has something to send.
    hasChatAnswer(id: string) {
      return !!this.sessions[id]?.queue.length
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

    async setPrMonitor(id: string, on: boolean) {
      await this.transport?.setPrMonitor(id, on)
    },

    async setAutoContinue(id: string, on: boolean) {
      await this.transport?.setAutoContinue(id, on)
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

    // The session relay switch: shows at once, rolled back when the client refuses it (the caller reports the error).
    async setSessionRelay(id: string, on: boolean) {
      const s = this.sessions[id]
      if (!s || !this.transport) return
      const prev = s.meta.relayEnabled
      s.meta.relayEnabled = on
      try {
        const info = await this.transport.setSessionRelay(id, on)
        Object.assign(s.meta, { relayEnabled: info.relayEnabled, relayAvailable: info.relayAvailable, relayReason: info.relayUnavailableReason })
      } catch (err) {
        if (s.meta.relayEnabled === on) s.meta.relayEnabled = prev
        throw err
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
    },

    // The composer's shell mode: the client runs the command at once (also while a turn runs, without queueing
    // or starting one) and the transcript shows it as a shell card; the model sees it on its next request.
    // `inline`: run from a code block's Run button. The block shows the output (the transcript draws no shell card
    // for it), and once it finishes a short message asks the agent to go on with the result.
    async runShell(id: string, command: string, opts: { inline?: boolean } = {}) {
      const body = command.trim()
      if (!body || !this.transport) return ''
      this.subscribe(id)
      const uuid = newUuid()
      if (opts.inline) {
        this.inlineShells[uuid] = { command: body, done: false }
        saveInlineShells(this.inlineShells)
      }
      await this.transport.runShellCommand(id, uuid, body)
      return uuid
    },

    async stopShell(id: string, commandId: string) {
      await this.transport?.stopShellCommand(id, commandId)
    }
  }
})
