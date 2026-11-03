// MockTransport: replays scripted harness.agent.v1 histories (./scenario.ts) and answers commands with
// scripted live turns, so every screen and card of the design can be reached without a client.
//
// Live behavior:
// - a message sent while idle starts a turn: thinking and text deltas, then a tool call;
//   "plan" in the text proposes a plan, "ask"/"question" asks a question, otherwise a Bash call asks for permission;
// - a message sent while a turn runs is queued (QueueChanged) and starts the next turn;
// - Interrupt ends the turn as interrupted; permission, question and plan answers resume it.

import {
  InitializeResponseSchema,
  ListBranchesResponseSchema,
  ListDirectoryResponseSchema,
  QuestionOutcome,
  RecentFolderSchema,
  SessionInfoSchema,
  RespondToPermissionRequest_Decision,
  RespondToPlanRequest_Decision,
  SendUserMessageRequest_Delivery,
  type PermissionMode,
  type SessionEvent,
  type SessionInfo
} from '~/gen/harness/agent/v1/agent_pb'
import { create } from '@bufbuild/protobuf'
import type { Effort } from '~/gen/harness/llm/v1/content_pb'
import { MockProvidersApi } from '~/lib/providers/mock'
import { MockUsersApi } from '~/lib/users/mock'
import { EventBuilder } from './events'
import { MAIN_SESSION, MODELS, mainHistory, otherHistory, sessionList } from './scenario'
import type { NewSessionOptions, SessionPatch, SessionTransport, SubscribeHandlers } from './types'
import type { QuestionReply } from '~/lib/session/types'
import { permissionModeName, permissionModePb } from '~/lib/session/reducer'

// Folders and a small directory tree for the new-session pickers.
const MOCK_HOME = '/Users/xlsft'
const MOCK_FOLDERS = [
  { path: '/Users/xlsft/Projects/xlsft/harness', branch: 'main', repo: 'github.com/xlsft/harness' },
  { path: '/Users/xlsft/Projects/xlsft/proxy.xlsft.ru', branch: 'main', repo: 'github.com/xlsft/proxy.xlsft.ru' },
  { path: '/Users/xlsft/Projects/xlsft/dashboard', branch: 'develop', repo: 'github.com/xlsft/dashboard' },
  { path: '/Users/xlsft/Projects/xlsft/youtrack-mcp', branch: 'main', repo: 'github.com/xlsft/youtrack-mcp' },
  { path: '/Users/xlsft/Projects/beget/auth', branch: 'main', repo: 'gitlab.beget.ru/web/auth' },
  { path: '/Users/xlsft/Projects/beget/landing', branch: 'main', repo: 'gitlab.beget.ru/web/landing' }
]
const MOCK_TREE: Record<string, string[]> = {
  '/': ['Users'],
  '/Users': ['xlsft'],
  '/Users/xlsft': ['Desktop', 'Documents', 'Downloads', 'Projects'],
  '/Users/xlsft/Desktop': [],
  '/Users/xlsft/Documents': [],
  '/Users/xlsft/Downloads': [],
  '/Users/xlsft/Projects': ['beget', 'xlsft'],
  '/Users/xlsft/Projects/beget': ['auth', 'landing'],
  '/Users/xlsft/Projects/xlsft': ['dashboard', 'harness', 'proxy.xlsft.ru', 'youtrack-mcp'],
  '/Users/xlsft/Projects/beget/auth': [],
  '/Users/xlsft/Projects/beget/landing': [],
  '/Users/xlsft/Projects/xlsft/dashboard': [],
  '/Users/xlsft/Projects/xlsft/harness': ['deploy', 'docs', 'packages', 'proto', 'scripts'],
  '/Users/xlsft/Projects/xlsft/proxy.xlsft.ru': [],
  '/Users/xlsft/Projects/xlsft/youtrack-mcp': []
}

// A question answer as the mock resumes it (a message answer carries the taken text).
type MockReply = Exclude<QuestionReply, { outcome: 'message' }> | { outcome: 'message', message: string }

interface MockSession {
  info: SessionInfo
  history: SessionEvent[]
  seq: number
  listeners: Set<SubscribeHandlers>
  working: boolean
  queue: { uuid: string, text: string }[]
  timers: ReturnType<typeof setTimeout>[]
  // Continuation after a permission / question / plan answer.
  waiting: { requestId: string, question?: boolean, resume: (b: EventBuilder, decision: number, reply?: MockReply) => number } | null
  turn: number
  afterResume: ((ok: boolean) => void) | null
}

export interface MockOptions {
  // Delay between streamed deltas (ms); 0 makes live turns synchronous-ish (tests).
  tick?: number
  now?: () => number
}

export class MockTransport implements SessionTransport {
  readonly kind = 'mock' as const
  private sessions = new Map<string, MockSession>()
  private tick: number
  private now: () => number

  constructor(opts: MockOptions = {}) {
    this.tick = opts.tick ?? 28
    this.now = opts.now ?? (() => Date.now())
    const now = this.now()
    for (const info of sessionList()) {
      const history = info.id === MAIN_SESSION ? mainHistory(now) : otherHistory(info, now)
      const durable = history.filter(e => e.event.case !== 'assistantDelta')
      this.sessions.set(info.id, {
        info,
        history,
        seq: Number(durable[durable.length - 1]?.seq ?? 0),
        listeners: new Set(),
        working: info.id === MAIN_SESSION,
        queue: [],
        timers: [],
        waiting: null,
        turn: 0,
        afterResume: null
      })
    }
  }

  private get(id: string) {
    const s = this.sessions.get(id)
    if (!s) throw new Error(`unknown session ${id}`)
    return s
  }

  async initialize() {
    const disabled = this.usersApi.disabledModes
    return create(InitializeResponseSchema, {
      clientVersion: '0.1.0-mock',
      userEmail: 'xlsft@self-hosted',
      modelIds: MODELS.map(m => m.id),
      permissionModes: (['default', 'accept_edits', 'plan', 'auto'] as const).filter(m => !disabled.includes(m)).map(permissionModePb),
      disabledPermissionModes: disabled.map(permissionModePb)
    })
  }

  async listSessions() {
    return [...this.sessions.values()].map(s => s.info).filter(i => !i.archived)
  }

  async updateSession(sessionId: string, patch: SessionPatch) {
    const s = this.get(sessionId)
    const title = patch.title?.trim()
    if (patch.title !== undefined && !title) throw new Error('title must not be empty')
    s.info = create(SessionInfoSchema, {
      ...s.info,
      ...(patch.pinned !== undefined && { pinned: patch.pinned }),
      ...(patch.unread !== undefined && { unread: patch.unread }),
      ...(patch.archived !== undefined && { archived: patch.archived }),
      ...(title && { title })
    })
    if (patch.archived) this.stop(s)
    return s.info
  }

  async deleteSession(sessionId: string) {
    this.stop(this.get(sessionId))
    this.sessions.delete(sessionId)
  }

  private stop(s: MockSession) {
    for (const t of s.timers) clearTimeout(t)
    s.timers = []
    s.working = false
    s.waiting = null
    s.afterResume = null
  }

  async createSession(opts: NewSessionOptions) {
    const name = opts.worktree ? (opts.worktree.name || `brisk-otter-${(this.sessions.size + 1).toString(16).padStart(4, '0')}`) : ''
    const repo = opts.cwd
    const info = create(SessionInfoSchema, {
      id: `ses_mock_${this.sessions.size + 1}`,
      title: 'New session',
      cwd: opts.worktree ? `${repo}/.harness/worktrees/${name}` : opts.cwd,
      gitBranch: opts.worktree ? (opts.worktree.branch || `harness/${name}`) : 'main',
      model: opts.model || MODELS[0]!.id,
      permissionMode: opts.permissionMode,
      worktree: opts.worktree ? { path: `${repo}/.harness/worktrees/${name}`, branch: opts.worktree.branch || `harness/${name}`, baseRef: opts.worktree.baseRef || 'main', repositoryPath: repo } : undefined,
      updatedAt: new Date(this.now()).toISOString()
    })
    this.sessions.set(info.id, { info, history: [], seq: 0, listeners: new Set(), working: false, queue: [], timers: [], waiting: null, turn: 0, afterResume: null })
    return info
  }

  async listRecentFolders() {
    return MOCK_FOLDERS.map((f, i) => create(RecentFolderSchema, {
      path: f.path,
      name: f.path.split('/').pop()!,
      gitBranch: f.branch,
      repo: f.repo,
      exists: true,
      lastUsedAt: new Date(this.now() - i * 3_600_000).toISOString()
    }))
  }

  async listDirectory(path: string) {
    const dir = (path || MOCK_HOME).replace(/^~(?=\/|$)/, MOCK_HOME).replace(/\/+$/, '') || '/'
    const children = MOCK_TREE[dir]
    if (!children) throw new Error(`${dir}: no such directory`)
    const parent = dir === '/' ? '' : dir.slice(0, dir.lastIndexOf('/')) || '/'
    return create(ListDirectoryResponseSchema, {
      path: dir,
      parent,
      home: MOCK_HOME,
      entries: children.map(name => ({ name, gitRepository: MOCK_FOLDERS.some(f => f.path === `${dir}/${name}`) }))
    })
  }

  async listBranches(cwd: string) {
    const folder = MOCK_FOLDERS.find(f => cwd === f.path || cwd.startsWith(`${f.path}/`))
    if (!folder) return create(ListBranchesResponseSchema, {})
    return create(ListBranchesResponseSchema, {
      repositoryPath: folder.path,
      current: folder.branch,
      branches: [folder.branch, 'feat/relay', 'fix/composer-height', 'release/0.1'].map((name, i) => ({
        name,
        checkedOut: i === 0,
        committedAt: new Date(this.now() - i * 86_400_000).toISOString()
      }))
    })
  }

  async listModels() {
    return MODELS
  }

  private providersApi = new MockProvidersApi()
  providers() {
    return this.providersApi
  }

  private usersApi = new MockUsersApi()
  users() {
    return this.usersApi
  }

  subscribe(sessionId: string, afterSeq: number, h: SubscribeHandlers) {
    const s = this.get(sessionId)
    let live = true
    queueMicrotask(() => {
      if (!live) return
      for (const ev of s.history) {
        // History replay yields durable events only; deltas of the in-flight turn are part of the live stream.
        if (ev.event.case === 'assistantDelta' || Number(ev.seq) > afterSeq) h.onEvent(ev)
      }
      // A session of another machine that cannot be driven is a snapshot of its server transcript.
      if (s.info.remote && !s.info.remoteControllable) return h.onEnd?.()
      s.listeners.add(h)
    })
    return () => {
      live = false
      s.listeners.delete(h)
    }
  }

  // Emits events built by `fn` on the session clock, appending durable ones to the history.
  private emit(s: MockSession, fn: (b: EventBuilder) => void) {
    const b = new EventBuilder(s.info.id, this.now(), s.seq)
    fn(b)
    s.seq = b.seq
    for (const ev of b.events) {
      if (ev.event.case !== 'assistantDelta') s.history.push(ev)
      for (const l of s.listeners) l.onEvent(ev)
    }
  }

  private later(s: MockSession, ms: number, fn: () => void) {
    if (this.tick === 0) {
      fn()
      return
    }
    s.timers.push(setTimeout(fn, ms))
  }

  private stream(s: MockSession, messageId: string, index: number, kind: 'text' | 'thinking', body: string, done: () => void) {
    const words = body.split(/(?<=\s)/)
    let i = 0
    const step = () => {
      if (i >= words.length) return done()
      const chunk = words.slice(i, i + 2).join('')
      i += 2
      this.emit(s, b => b.delta(messageId, index, kind, chunk))
      this.later(s, this.tick, step)
    }
    step()
  }

  async sendUserMessage(sessionId: string, uuid: string, text: string, delivery: SendUserMessageRequest_Delivery) {
    const s = this.get(sessionId)
    if (s.working && delivery !== SendUserMessageRequest_Delivery.NOW) {
      s.queue.push({ uuid, text })
      this.emit(s, b => b.queue(s.queue))
      return
    }
    this.startTurn(s, uuid, text)
  }

  private startTurn(s: MockSession, uuid: string, body: string) {
    s.working = true
    s.turn += 1
    const n = s.turn
    const startedAt = this.now()
    const msg = `live-${n}`
    this.emit(s, (b) => {
      b.user(uuid, body)
      b.turnStarted(uuid, s.info.model)
    })

    if (body.trim().startsWith('/compact')) {
      this.later(s, 600, () => this.emit(s, (b) => {
        b.compacted(182_000, 24_000)
        b.turnFinished('success', { durationMs: this.now() - startedAt, costUsd: 0.01, usage: { input: 2000, output: 300 } })
        this.finishTurn(s)
      }))
      return
    }

    const lower = body.toLowerCase()
    const thinking = 'The user wants this done now. Check the current state first, then make the smallest change that works.'
    this.stream(s, msg, 0, 'thinking', thinking, () => {
      const intro = lower.includes('plan')
        ? 'Here is how I would approach it. Nothing changes until you approve the plan.'
        : lower.includes('ask') || lower.includes('question')
          ? 'One decision first, it changes the shape of the change.'
          : 'Rebuilding core from a clean output directory, then running the gateway tests.'
      this.stream(s, msg, 1, 'text', intro, () => {
        if (lower.includes('plan')) return this.planFlow(s, msg, thinking, intro, startedAt)
        if (lower.includes('ask') || lower.includes('question')) return this.questionFlow(s, msg, thinking, intro, startedAt)
        this.permissionFlow(s, msg, thinking, intro, startedAt)
      })
    })
  }

  private permissionFlow(s: MockSession, msg: string, thinking: string, intro: string, startedAt: number) {
    const tool = `${msg}-bash`
    const req = `${msg}-perm`
    const command = 'rm -rf packages/core/.output && pnpm --filter core build'
    this.emit(s, (b) => {
      b.assistant(msg, s.info.model, [{ thinking }, { text: intro }, { toolUse: { id: tool, name: 'Bash', input: { command } } }], { input: 31_000, output: 800 })
      b.toolStarted(tool, 'Bash', { command }, command)
      b.permissionRequest(req, tool, 'Bash', { command, description: 'Remove the old core build before rebuilding.' }, ['Bash(rm -rf packages/core/.output)'], 'Remove the old core build before rebuilding.')
    })
    s.waiting = {
      requestId: req,
      resume: (b, decision) => {
        b.permissionResolved(req, decision)
        if (decision === RespondToPermissionRequest_Decision.DENY) {
          b.toolFinished(tool, { output: 'The user denied this command.', isError: true, summary: 'denied' })
          b.turnFinished('interrupted', { durationMs: this.now() - startedAt, costUsd: 0.01, usage: { input: 31_000, output: 800 } })
          return 0
        }
        b.wait(2400).toolFinished(tool, {
          output: '> core@0.1.0 build\n> nitro build\n\n✔ Generated public .output/public\n✔ Nitro server built in 3.1s',
          result: { stdout: '✔ Nitro server built in 3.1s', stderr: '', exitCode: 0 },
          summary: 'exit 0',
          durationMs: 2400
        })
        return 1
      }
    }
    s.afterResume = (decisionOk) => {
      if (!decisionOk) return this.finishTurn(s)
      const msg2 = `${msg}-done`
      const body = 'Core rebuilt from a clean `.output` in 3.1s. The gateway tests still pass; usage now arrives before **message_stop** on every provider.'
      this.stream(s, msg2, 0, 'text', body, () => this.emit(s, (b) => {
        b.assistant(msg2, s.info.model, [{ text: body }], { input: 33_000, output: 400 })
        b.turnFinished('success', { durationMs: this.now() - startedAt, costUsd: 0.03, usage: { input: 12_400, output: 2100 } })
        this.finishTurn(s)
      }))
    }
  }

  private questionFlow(s: MockSession, msg: string, thinking: string, intro: string, startedAt: number) {
    const tool = `${msg}-ask`
    const req = `${msg}-q`
    this.emit(s, (b) => {
      b.assistant(msg, s.info.model, [{ thinking }, { text: intro }, { toolUse: { id: tool, name: 'AskUserQuestion', input: {} } }], { input: 30_000, output: 300 })
      b.toolStarted(tool, 'AskUserQuestion', {}, 'Where should provider keys live?')
      b.question(req, tool, [{
        question: 'Where should provider keys live?',
        header: 'Keys',
        type: 'single',
        options: [
          { label: 'Container env', description: 'Simpler, but rotating a key means a redeploy' },
          { label: 'Postgres, encrypted', description: 'Master key in env, rotation without restart', recommended: true },
          { label: 'External Vault', description: 'One more service in docker-compose' }
        ]
      }, {
        question: 'Which providers should be enabled first?',
        header: 'Providers',
        type: 'multi',
        min: 1,
        max: 2,
        options: [{ label: 'Anthropic' }, { label: 'OpenAI' }, { label: 'OpenRouter' }]
      }, {
        question: 'Rotate existing keys on deploy?',
        header: 'Rotation',
        type: 'yes_no'
      }])
    })
    s.waiting = {
      requestId: req,
      question: true,
      resume: (b, _d, reply) => {
        if (reply?.outcome === 'skipped') b.questionResolved(req, 'local', QuestionOutcome.SKIPPED)
        else if (reply?.outcome === 'message') b.questionResolved(req, 'local', QuestionOutcome.MESSAGE, [], reply.message)
        else b.questionResolved(req, 'local', QuestionOutcome.ANSWERED, reply?.outcome === 'answered' ? reply.answers : [])
        b.toolFinished(tool, { output: 'Answered', summary: 'answered' })
        return 1
      }
    }
    s.afterResume = () => {
      const msg2 = `${msg}-done`
      const body = 'Going with encrypted Postgres: the master key stays in env and keys rotate without a restart.'
      this.stream(s, msg2, 0, 'text', body, () => this.emit(s, (b) => {
        b.assistant(msg2, s.info.model, [{ text: body }], { output: 120 })
        b.turnFinished('success', { durationMs: this.now() - startedAt, costUsd: 0.02, usage: { input: 8000, output: 420 } })
        this.finishTurn(s)
      }))
    }
  }

  private planFlow(s: MockSession, msg: string, thinking: string, intro: string, startedAt: number) {
    const tool = `${msg}-plan`
    const req = `${msg}-p`
    const plan = [
      '## Provider retries',
      '',
      '1. Retry only 429 and 5xx, with exponential backoff (base 500 ms, factor 2, at most 5 attempts).',
      '2. Surface each retry as a `retry` notice so the UI can show **Retry now** and **Switch model**.',
      '3. Add `gateway/retry.test.ts` with a fake provider that fails twice, then succeeds.',
      '',
      'Files: `packages/core/server/gateway/retry.ts`, `translate.ts`, `retry.test.ts`.'
    ].join('\n')
    this.emit(s, (b) => {
      b.assistant(msg, s.info.model, [{ thinking }, { text: intro }, { toolUse: { id: tool, name: 'ExitPlanMode', input: { plan } } }], { input: 30_000, output: 600 })
      b.toolStarted(tool, 'ExitPlanMode', { plan }, 'Plan')
      b.planProposed(req, tool, plan, '~/.harness/plans/provider-retries.md')
    })
    s.waiting = {
      requestId: req,
      resume: (b, decision) => {
        b.planResolved(req, decision)
        b.toolFinished(tool, { output: 'Plan resolved', summary: 'resolved' })
        return decision === RespondToPlanRequest_Decision.KEEP_PLANNING ? 0 : 1
      }
    }
    s.afterResume = (ok) => {
      const msg2 = `${msg}-done`
      const body = ok ? 'Plan approved. Starting with `retry.ts`.' : 'Keeping the plan open; tell me what to change.'
      this.stream(s, msg2, 0, 'text', body, () => this.emit(s, (b) => {
        b.assistant(msg2, s.info.model, [{ text: body }], { output: 80 })
        b.turnFinished('success', { durationMs: this.now() - startedAt, costUsd: 0.02, usage: { input: 9000, output: 680 } })
        this.finishTurn(s)
      }))
    }
  }

  private resume(sessionId: string, requestId: string, decision: number, reply?: MockReply) {
    const s = this.get(sessionId)
    if (!s.waiting || s.waiting.requestId !== requestId) return
    const w = s.waiting
    s.waiting = null
    let ok = 0
    this.emit(s, (b) => {
      ok = w.resume(b, decision, reply)
    })
    const next = s.afterResume
    s.afterResume = null
    this.later(s, 300, () => next?.(ok === 1))
  }

  private finishTurn(s: MockSession) {
    s.working = false
    const next = s.queue.shift()
    if (next) {
      this.emit(s, b => b.queue(s.queue))
      this.later(s, 400, () => this.startTurn(s, next.uuid, next.text))
    }
  }

  async interrupt(sessionId: string) {
    const s = this.get(sessionId)
    if (!s.working) return
    for (const t of s.timers) clearTimeout(t)
    s.timers = []
    const pending = s.waiting
    s.waiting = null
    s.afterResume = null
    this.emit(s, (b) => {
      if (pending?.question) b.questionResolved(pending.requestId, 'interrupt', QuestionOutcome.CANCELLED)
      else if (pending) b.permissionResolved(pending.requestId, RespondToPermissionRequest_Decision.DENY, 'interrupt')
      b.turnFinished('interrupted', { durationMs: 0 })
    })
    s.working = false
    s.queue = []
    this.emit(s, b => b.queue([]))
  }

  async respondToPermission(sessionId: string, requestId: string, decision: RespondToPermissionRequest_Decision) {
    this.resume(sessionId, requestId, decision)
  }

  // Like the client: a message answer takes the queued messages (all, or the given ones) out of the queue.
  async answerQuestion(sessionId: string, requestId: string, reply: QuestionReply) {
    const s = this.get(sessionId)
    if (reply.outcome !== 'message') return this.resume(sessionId, requestId, 1, reply)
    const want = reply.messageUuids?.length ? new Set(reply.messageUuids) : null
    const taken = s.queue.filter(q => !want || want.has(q.uuid))
    if (!taken.length) throw new Error('no queued message to answer with')
    s.queue = s.queue.filter(q => !taken.includes(q))
    this.emit(s, b => b.queue(s.queue))
    this.resume(sessionId, requestId, 1, { outcome: 'message', message: taken.map(q => q.text).join('\n\n') })
  }

  async respondToPlan(sessionId: string, requestId: string, decision: RespondToPlanRequest_Decision) {
    this.resume(sessionId, requestId, decision)
  }

  async setModel(sessionId: string, model: string) {
    const s = this.get(sessionId)
    s.info = { ...s.info, model }
    this.emit(s, b => b.sessionUpdated(s.info))
  }

  async setEffort(_sessionId: string, _effort: Effort) {}

  async setPermissionMode(sessionId: string, mode: PermissionMode) {
    const s = this.get(sessionId)
    if (this.usersApi.disabledModes.includes(permissionModeName(mode))) throw new Error('[permission_denied] this permission mode is disabled by your administrator')
    s.info = { ...s.info, permissionMode: mode }
    this.emit(s, b => b.permissionModeChanged(mode))
  }

  async suggestFiles(_sessionId: string, query: string, limit: number) {
    const files = [
      'packages/core/server/gateway/translate.ts',
      'packages/core/server/gateway/',
      'packages/core/server/routes/gateway.ts',
      'packages/core/server/gateway/providers/openrouter.ts',
      'packages/core/server/gateway/providers/ollama.ts',
      'proto/gateway/v1/gateway.proto',
      'docs/PLAN.md'
    ]
    const q = query.toLowerCase()
    return files.filter(f => f.toLowerCase().includes(q)).slice(0, limit)
  }

  async compact(sessionId: string) {
    const s = this.get(sessionId)
    this.emit(s, b => b.compacted(182_000, 24_000))
  }

  async listTasks() {
    return []
  }

  async stopTask(sessionId: string, taskId: string) {
    const s = this.get(sessionId)
    this.emit(s, b => b.taskFinished(taskId, 143, 0, true))
  }
}

// The store keeps the transport instance across hot updates, so a changed transport would keep running the
// old code (missing methods). Reload the page instead of hot-swapping this module.
if (import.meta.hot) import.meta.hot.accept(() => window.location.reload())
