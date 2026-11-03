// MockTransport: replays scripted harness.agent.v1 histories (./scenario.ts) and answers commands with
// scripted live turns, so every screen and card of the design can be reached without a client.
//
// Live behavior:
// - a message sent while idle starts a turn: thinking and text deltas, then a tool call;
//   "plan" in the text proposes a plan, "ask"/"question" asks a question, otherwise a Bash call asks for permission;
// - a message sent while a turn runs is queued (QueueChanged) and starts the next turn;
// - Interrupt ends the turn as interrupted; permission, question and plan answers resume it;
// - a shell-mode command runs in a tiny scripted shell (./mockShell.ts) without a turn.

import {
  ContextUsage_Category,
  ContextUsageSchema,
  InitializeResponseSchema,
  ListBranchesResponseSchema,
  ListDirectoryResponseSchema,
  QuestionOutcome,
  RecentFolderSchema,
  SessionInfoSchema,
  SessionStatus,
  RespondToPermissionRequest_Decision,
  RespondToPlanRequest_Decision,
  type RewindRequest_Mode,
  SendUserMessageRequest_Delivery,
  type PermissionMode,
  type SessionEvent,
  type SessionInfo
} from '~/gen/harness/agent/v1/agent_pb'
import { create } from '@bufbuild/protobuf'
import type { Effort } from '~/gen/harness/llm/v1/content_pb'
import { runMockShell } from './mockShell'
import { MockProvidersApi } from '~/lib/providers/mock'
import { MockUsersApi } from '~/lib/users/mock'
import { MockAccountApi } from '~/lib/account/mock'
import { MockUsageApi } from '~/lib/usage/mock'
import { MockLimitsApi } from '~/lib/limits/mock'
import { MockTerminalsApi } from '~/lib/terminal/mock'
import { MockSideChatsApi } from '~/lib/sidechat/mock'

import { MockEnvironmentApi } from '~/lib/environment/mock'
import { MockFilesApi } from '~/lib/files/mock'
import { MockSpeechApi } from '~/lib/speech/mock'
import { EventBuilder } from './events'
import { MAIN_SESSION, MODELS, mainHistory, otherHistory, sessionList } from './scenario'
import type { ModelInfo, NewSessionOptions, SessionPatch, SessionTransport, SubscribeHandlers, UserPreferences } from './types'
import type { AddLocalProviderInput, LocalModel, LocalModelsApi, LocalProvider, LocalProviderKindName, LocalProviderPatch, LocalProvidersState, PullHandle, PullHandlers, PullProgress } from './localModels'
import { contextUsageFromProto } from '~/lib/session/context'
import type { QuestionReply } from '~/lib/session/types'
import { permissionModeName, permissionModePb } from '~/lib/session/reducer'

// Folders and a small directory tree for the new-session pickers.
const MOCK_HOME = '/Users/xlsft'
// The private directory of a session without a folder (the client's HARNESS_HOME/chats/<id>).
const mockChatDir = (id: string) => `${MOCK_HOME}/.harness/chats/${id}`
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
  queue: { uuid: string, text: string, queuedAt: number }[]
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
  // Message tokens per session for the context breakdown (a compaction shrinks them).
  private messageTokens = new Map<string, number>()
  private preferences: UserPreferences = { compactIdleSessions: true }
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
      disabledPermissionModes: disabled.map(permissionModePb),
      // The mock UI itself opens sessions by ?session=<id>.
      webUrl: typeof location !== 'undefined' ? location.origin : ''
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
      ...(title && { title }),
      ...(patch.detachFromParent && { parentSessionId: '' })
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
    if (opts.fork && opts.resumeSessionId) return this.fork(opts.resumeSessionId, opts.forkAtMessageId)
    const id = `ses_mock_${this.sessions.size + 1}`
    if (!opts.cwd) {
      // No folder, as the client does it: a private directory named after the session, no repository.
      if (opts.worktree) throw new Error('[invalid_argument] worktree requires a folder (cwd)')
      const info = create(SessionInfoSchema, {
        id,
        title: 'New session',
        cwd: mockChatDir(id),
        noFolder: true,
        model: opts.model || MODELS[0]!.id,
        permissionMode: opts.permissionMode,
        updatedAt: new Date(this.now()).toISOString()
      })
      this.sessions.set(id, { info, history: [], seq: 0, listeners: new Set(), working: false, queue: [], timers: [], waiting: null, turn: 0, afterResume: null })
      return info
    }
    const name = opts.worktree ? (opts.worktree.name || `brisk-otter-${(this.sessions.size + 1).toString(16).padStart(4, '0')}`) : ''
    const repo = opts.cwd
    const info = create(SessionInfoSchema, {
      id,
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

  // A fork as the client makes it: a copy of the conversation (before forkAt, a user message uuid), the parent
  // named, and the title "<parent title> (fork)".
  private fork(parentId: string, forkAt?: string) {
    const parent = this.get(parentId)
    const id = `ses_mock_${this.sessions.size + 1}`
    let durable = parent.history.filter(e => e.event.case !== 'assistantDelta')
    if (forkAt) {
      const at = durable.findIndex(e => e.event.case === 'userMessage' && e.event.value.uuid === forkAt)
      if (at < 0) throw new Error(`message ${forkAt} is not in the transcript`)
      durable = durable.slice(0, at)
    }
    // The conversation replays like a resumed transcript (no session state of the parent), then the fork's info.
    const history = durable
      .filter(e => e.event.case !== 'sessionUpdated' && e.event.case !== 'queueChanged')
      .map((e, i) => ({ ...e, seq: BigInt(i + 1), sessionId: id }))
    const title = parent.info.title.endsWith(' (fork)') ? parent.info.title : `${parent.info.title} (fork)`
    // A fork of a session without a folder gets a private directory of its own.
    const info = create(SessionInfoSchema, { ...parent.info, id, title, parentSessionId: parentId, status: SessionStatus.IDLE, pinned: false, unread: false, updatedAt: new Date(this.now()).toISOString(), ...(parent.info.noFolder && { cwd: mockChatDir(id) }) })
    const s: MockSession = { info, history, seq: history.length, listeners: new Set(), working: false, queue: [], timers: [], waiting: null, turn: 0, afterResume: null }
    this.sessions.set(id, s)
    this.emit(s, b => b.sessionUpdated(info))
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
    return [...MODELS, ...this.localModels().pickerModels()]
  }

  // Local providers of this (mock) machine; their models join listModels() marked local.
  private localModelsApi?: MockLocalModelsApi
  localModels() {
    return (this.localModelsApi ??= new MockLocalModelsApi({ tick: this.tick * 4 }))
  }

  private providersApi = new MockProvidersApi()
  providers() {
    return this.providersApi
  }

  private usersApi = new MockUsersApi()
  users() {
    return this.usersApi
  }

  private accountApi = new MockAccountApi()
  account() {
    return this.accountApi
  }

  private limitsApi = new MockLimitsApi()
  limits() {
    return this.limitsApi
  }

  private usageApi = new MockUsageApi()
  usage() {
    return this.usageApi
  }

  private terminalsApi = new MockTerminalsApi({ sessionCwd: id => this.sessions.get(id)?.info.cwd })
  terminals() {
    return this.terminalsApi
  }

  private speechApi = new MockSpeechApi()
  speech() {
    return this.speechApi
  }

  private sideChatsApi = new MockSideChatsApi({ sessionModel: id => this.sessions.get(id)?.info.model })
  sideChats() {
    return this.sideChatsApi
  }

  private environmentApi = new MockEnvironmentApi({
    home: MOCK_HOME,
    sessions: () => [...this.sessions.values()].map(s => s.info),
    exists: path => path in MOCK_TREE || MOCK_FOLDERS.some(f => path.startsWith(`${f.path}/`)),
    repository: (path) => {
      const f = MOCK_FOLDERS.find(f => path === f.path || path.startsWith(`${f.path}/`))
      return f && { path: f.path, branch: f.branch, repo: f.repo }
    },
    moveSession: (id, cwd) => this.moveSession(id, cwd)
  })

  environment() {
    return this.environmentApi
  }

  // SetSessionFolder as the client does it: the session must be idle; subscribers get SessionEnded
  // ('folder_changed', not kept in the history) and their stream ends, so they subscribe again.
  private moveSession(id: string, cwd: string) {
    const s = this.get(id)
    if (s.working || s.waiting) throw new Error('[failed_precondition] the session is working; change its folder once it is idle')
    if (s.info.cwd === cwd) return s.info
    s.info = create(SessionInfoSchema, { ...s.info, cwd })
    const b = new EventBuilder(id, this.now(), s.seq)
    b.sessionEnded('folder_changed')
    s.seq = b.seq
    const listeners = [...s.listeners]
    s.listeners.clear()
    for (const l of listeners) {
      for (const ev of b.events) l.onEvent(ev)
      l.onEnd?.()
    }
    this.emit(s, e => e.sessionUpdated(s.info))
    return s.info
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
    if (s.working) {
      // Queued (QueueChanged); NOW stops the turn so the message starts the next one, like the client.
      s.queue.push({ uuid, text, queuedAt: this.now() })
      this.emit(s, b => b.queue(s.queue))
      if (delivery === SendUserMessageRequest_Delivery.NOW) {
        this.stopTurn(s)
        this.finishTurn(s)
      }
      return
    }
    this.startTurn(s, uuid, text)
  }

  async removeQueuedMessage(sessionId: string, uuid: string) {
    const s = this.get(sessionId)
    const before = s.queue.length
    s.queue = s.queue.filter(q => q.uuid !== uuid)
    if (s.queue.length === before) return false
    this.emit(s, b => b.queue(s.queue))
    return true
  }

  // Rewinds to the checkpoint of a user message: the message's text comes back for the composer.
  async rewind(sessionId: string, checkpointId: string, mode: RewindRequest_Mode) {
    const s = this.get(sessionId)
    if (s.working) throw new Error('[failed_precondition] a turn is in progress')
    const ev = s.history.find(e => e.event.case === 'userMessage' && e.event.value.uuid === checkpointId)
    if (!ev || ev.event.case !== 'userMessage') throw new Error(`[failed_precondition] unknown checkpoint ${checkpointId}`)
    const restoredPrompt = ev.event.value.content.map(b => b.block.case === 'text' ? b.block.value.text : '').join('')
    this.emit(s, b => b.rewound(checkpointId, mode, restoredPrompt))
    return { restoredPrompt, filesRestored: 0 }
  }

  private startTurn(s: MockSession, uuid: string, body: string) {
    s.working = true
    s.turn += 1
    const n = s.turn
    const startedAt = this.now()
    const msg = `live-${n}`
    this.emit(s, (b) => {
      b.checkpoint(uuid, body)
      b.user(uuid, body, [uuid])
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
    // The client labels Bash calls in the background: the title follows the call shortly.
    this.later(s, this.tick * 6, () => this.emit(s, b => b.toolTitle(tool, 'clean core build')))
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

  // Ends the running turn as interrupted (an open card is cancelled); the queue is kept.
  private stopTurn(s: MockSession) {
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
  }

  async interrupt(sessionId: string) {
    const s = this.get(sessionId)
    if (!s.working) return { idle: true }
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
    return { idle: false }
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
    // A compaction takes a while: the details pane shows it busy meanwhile.
    await new Promise(resolve => setTimeout(resolve, this.tick * 40))
    this.messageTokens.set(sessionId, 9_800)
    this.emit(s, b => b.compacted(182_000, 24_000))
  }

  async getContextUsage(sessionId: string) {
    this.get(sessionId)
    const window = 200_000
    const threshold = window * 0.92
    const parts: [ContextUsage_Category, number][] = [
      [ContextUsage_Category.MESSAGES, this.messageTokens.get(sessionId) ?? 98_400],
      [ContextUsage_Category.MCP_TOOLS, 6_300],
      [ContextUsage_Category.SYSTEM_TOOLS, 11_900],
      [ContextUsage_Category.SYSTEM_PROMPT, 3_600],
      [ContextUsage_Category.MCP_SERVER_INSTRUCTIONS, 0]
    ]
    const used = parts.reduce((n, [, t]) => n + t, 0)
    const tools = [
      { server: 'youtrack', tool: 'search_issues', description: 'Search YouTrack issues with a query', tokens: 1_400, deferred: false },
      { server: 'youtrack', tool: 'get_issue', description: 'Read an issue by its readable id (PRJ-123)', tokens: 900, deferred: false },
      { server: 'youtrack', tool: 'update_issue', description: 'Change the fields of an issue', tokens: 4_000, deferred: false },
      { server: 'figma', tool: 'get_design_context', description: 'Design context of a Figma node for implementing it in code', tokens: 2_600, deferred: true },
      { server: 'figma', tool: 'get_screenshot', description: 'Screenshot of a Figma node', tokens: 700, deferred: true }
    ]
    return contextUsageFromProto(create(ContextUsageSchema, {
      model: 'mock',
      contextWindow: BigInt(window),
      usedTokens: BigInt(used),
      categories: [
        ...parts.map(([category, tokens]) => ({ category, tokens: BigInt(tokens) })),
        { category: ContextUsage_Category.AUTOCOMPACT_BUFFER, tokens: BigInt(window - threshold) },
        { category: ContextUsage_Category.FREE_SPACE, tokens: BigInt(threshold - used) }
      ],
      deferredMcpToolsTokens: BigInt(tools.filter(t => t.deferred).reduce((n, t) => n + t.tokens, 0)),
      mcpTools: tools.map(t => ({ ...t, name: `mcp__${t.server}__${t.tool}`, tokens: BigInt(t.tokens) })),
      autocompactThreshold: BigInt(threshold),
      tokensUntilAutocompact: BigInt(threshold - used)
    }))
  }

  async getUserPreferences() {
    return { ...this.preferences }
  }

  async updateUserPreferences(patch: Partial<UserPreferences>) {
    this.preferences = { ...this.preferences, ...patch }
    return { ...this.preferences }
  }

  async listTasks() {
    return []
  }

  async stopTask(sessionId: string, taskId: string) {
    const s = this.get(sessionId)
    this.emit(s, b => b.taskFinished(taskId, 143, 0, true))
  }

  private filesApi = new MockFilesApi({ sessionCwd: id => this.sessions.get(id)?.info.cwd, blank: id => !!this.sessions.get(id)?.info.noFolder })
  files() {
    return this.filesApi
  }

  // User shell commands (runMockShell): the result streams after a short delay, `sleep N` runs N seconds
  // (Stop ends it), and cd moves the session's shell directory. Remote sessions refuse them, as the client does.
  private shellCwd = new Map<string, string>()
  private shells = new Map<string, () => void>()

  async runShellCommand(sessionId: string, uuid: string, command: string) {
    const s = this.get(sessionId)
    if (s.info.remote) throw new Error(`session ${sessionId} runs on ${s.info.deviceName || 'another machine'}: shell commands run only on the session's own machine`)
    const id = uuid || `sh-${Date.now()}`
    if (this.shells.has(id)) return id
    const cwd = this.shellCwd.get(sessionId) ?? s.info.cwd
    const r = runMockShell(command, cwd)
    const startedAt = this.now()
    this.emit(s, b => b.userShellStarted(id, command, cwd))
    const finish = (stopped: boolean) => {
      if (!this.shells.delete(id)) return
      if (!stopped) this.shellCwd.set(sessionId, r.cwd)
      const durationMs = stopped ? this.now() - startedAt : r.durationMs
      this.emit(s, b => b.userShellFinished(id, stopped
        ? { command, cwd, exitCode: -1, durationMs, stdout: r.stdout, reason: 'stopped' }
        : { command, cwd, exitCode: r.exitCode, durationMs, stdout: r.stdout, stderr: r.stderr }))
    }
    this.shells.set(id, () => finish(true))
    if (r.stdout && r.durationMs > 1000) this.later(s, this.tick * 4, () => this.shells.has(id) && this.emit(s, b => b.userShellOutput(id, r.stdout)))
    this.later(s, this.tick ? r.durationMs : 0, () => finish(false))
    return id
  }

  async stopShellCommand(_sessionId: string, commandId: string) {
    const stop = this.shells.get(commandId)
    if (!stop) throw new Error(`shell command ${commandId} is not running`)
    stop()
  }
}

// ---- Local LLM providers (./localModels.ts) ----
//
// The machine of the mock: Ollama runs with two models, LM Studio and the other default endpoints are not
// running, and one custom OpenAI-compatible endpoint (a vLLM box on the LAN, with a key) is reachable. A pull
// streams progress every few ticks and adds the model; a model name containing "missing" fails like Ollama does.

export interface MockLocalModelsOptions {
  // Delay between pull progress messages (ms); 0 runs pulls without waiting.
  tick?: number
  // The server's policy disallows local models.
  disabledByPolicy?: boolean
}

const mockLocalModel = (providerId: string, name: string, o: Partial<LocalModel> = {}): LocalModel => ({
  id: `local:${providerId}/${name}`,
  name,
  displayName: name,
  contextWindow: 32_768,
  maxContextWindow: 0,
  sizeBytes: 0,
  family: '',
  parameterSize: '',
  quantization: '',
  modifiedAt: '',
  ...o
})

const notRunning = (id: string, kind: LocalProviderKindName, name: string, baseUrl: string): LocalProvider => ({
  id, kind, name, baseUrl, enabled: true, builtin: true, reachable: false, version: '',
  error: 'not running (connection refused)', hasApiKey: false, models: [], checkedAt: new Date().toISOString(), contextLength: 0
})

export class MockLocalModelsApi implements LocalModelsApi {
  private tick: number
  private disabledByPolicy: boolean
  private providers: LocalProvider[]
  private seq = 0

  constructor(opts: MockLocalModelsOptions = {}) {
    this.tick = opts.tick ?? 120
    this.disabledByPolicy = opts.disabledByPolicy ?? false
    const checkedAt = new Date().toISOString()
    this.providers = [
      {
        id: 'ollama', kind: 'ollama', name: 'Ollama', baseUrl: 'http://127.0.0.1:11434', enabled: true, builtin: true,
        reachable: true, version: '0.12.3', error: '', hasApiKey: false, checkedAt, contextLength: 0,
        models: [
          mockLocalModel('ollama', 'llama3.2:3b', { maxContextWindow: 131_072, tools: true, vision: false, thinking: false, sizeBytes: 2_019_393_189, family: 'llama', parameterSize: '3.2B', quantization: 'Q4_K_M', modifiedAt: '2026-10-12T09:30:00Z' }),
          mockLocalModel('ollama', 'qwen3:8b', { maxContextWindow: 40_960, tools: true, vision: false, thinking: true, sizeBytes: 5_225_387_923, family: 'qwen3', parameterSize: '8.2B', quantization: 'Q4_K_M', modifiedAt: '2026-10-28T17:02:00Z' })
        ]
      },
      notRunning('lmstudio', 'lmstudio', 'LM Studio', 'http://127.0.0.1:1234/v1'),
      notRunning('llamacpp', 'llamacpp', 'llama.cpp', 'http://127.0.0.1:8080/v1'),
      notRunning('mlx', 'mlx', 'MLX', 'http://127.0.0.1:8080/v1'),
      notRunning('vllm', 'vllm', 'vLLM', 'http://127.0.0.1:8000/v1'),
      notRunning('jan', 'jan', 'Jan', 'http://127.0.0.1:1337/v1'),
      {
        id: 'custom-a1b2c3', kind: 'custom', name: 'Workstation', baseUrl: 'http://192.168.1.20:8000/v1', enabled: true, builtin: false,
        reachable: true, version: '', error: '', hasApiKey: true, checkedAt, contextLength: 0,
        models: [mockLocalModel('custom-a1b2c3', 'Qwen/Qwen3-Coder-30B-A3B-Instruct', { contextWindow: 65_536, maxContextWindow: 65_536 })]
      }
    ]
  }

  private state(): LocalProvidersState {
    const providers = structuredClone(this.providers)
    if (this.disabledByPolicy) for (const p of providers) p.models = []
    return { providers, disabledByPolicy: this.disabledByPolicy }
  }

  private find(id: string) {
    const p = this.providers.find(p => p.id === id)
    if (!p) throw new Error(`no local provider "${id}"`)
    return p
  }

  private ollama(id = 'ollama') {
    if (this.disabledByPolicy) throw new Error('local models are disabled by the server\'s policy')
    const p = this.find(id)
    if (p.kind !== 'ollama') throw new Error(`${p.name} is not an Ollama server`)
    if (!p.reachable) throw new Error(`${p.name} is not reachable`)
    return p
  }

  // The model picker's local entries (listModels).
  pickerModels(): ModelInfo[] {
    if (this.disabledByPolicy) return []
    return this.providers.filter(p => p.enabled && p.reachable).flatMap(p => p.models.map(m => ({
      id: m.id, displayName: m.displayName, provider: p.name, contextWindow: m.contextWindow, price: '', local: true, thinking: m.thinking
    })))
  }

  async list() {
    return this.state()
  }

  async add(input: AddLocalProviderInput) {
    let url: URL
    try {
      url = new URL(input.baseUrl)
    } catch {
      throw new Error('base_url must be an http(s) URL')
    }
    if (url.protocol !== 'http:' && url.protocol !== 'https:') throw new Error('base_url must be an http(s) URL')
    const kind = input.kind ?? 'custom'
    const id = `${kind}-mock${++this.seq}`
    // Only the LAN box answers in the mock; anything else is not running.
    const reachable = url.hostname.startsWith('192.168.')
    const p: LocalProvider = {
      id, kind, name: input.name || 'OpenAI-compatible', baseUrl: input.baseUrl.replace(/\/+$/, ''), enabled: true, builtin: false,
      reachable, version: '', error: reachable ? '' : 'not running (no answer in time)', hasApiKey: !!input.apiKey,
      models: reachable ? [mockLocalModel(id, 'default')] : [], checkedAt: new Date().toISOString(), contextLength: input.contextLength ?? 0
    }
    this.providers.push(p)
    return structuredClone(p)
  }

  async update(id: string, patch: LocalProviderPatch) {
    const p = this.find(id)
    if (p.builtin && (patch.name !== undefined || patch.baseUrl !== undefined || patch.apiKey !== undefined)) {
      throw new Error('built-in providers can only be enabled, disabled or given a context length')
    }
    if (patch.name) p.name = patch.name
    if (patch.baseUrl) p.baseUrl = patch.baseUrl
    if (patch.apiKey !== undefined) p.hasApiKey = patch.apiKey !== ''
    if (patch.enabled !== undefined) p.enabled = patch.enabled
    if (patch.contextLength !== undefined) {
      p.contextLength = patch.contextLength
      if (p.kind === 'ollama') {
        for (const m of p.models) m.contextWindow = Math.min(patch.contextLength || 32_768, m.maxContextWindow || Infinity)
      }
    }
    return structuredClone(p)
  }

  async remove(id: string) {
    const p = this.find(id)
    if (p.builtin) throw new Error('built-in providers cannot be removed; disable them instead')
    this.providers = this.providers.filter(x => x.id !== id)
  }

  async refresh(id?: string) {
    const now = new Date().toISOString()
    for (const p of this.providers) if (p.enabled && (!id || p.id === id)) p.checkedAt = now
    return this.state()
  }

  pullOllamaModel(model: string, handlers: PullHandlers, providerId = 'ollama'): PullHandle {
    let cancel = () => {}
    const done = new Promise<void>((resolve, reject) => {
      let p: LocalProvider
      try {
        p = this.ollama(providerId)
      } catch (e) {
        reject(e)
        return
      }
      const total = 4_920_000_000
      const steps: PullProgress[] = [
        { status: 'pulling manifest', digest: '', total: 0, completed: 0, done: false },
        ...[0, 0.18, 0.41, 0.67, 0.9, 1].map(f => ({ status: 'downloading', digest: 'sha256:6e4c38e1172f', total, completed: Math.round(total * f), done: false })),
        { status: 'verifying sha256 digest', digest: '', total: 0, completed: 0, done: false },
        { status: 'writing manifest', digest: '', total: 0, completed: 0, done: false },
        { status: 'success', digest: '', total: 0, completed: 0, done: true }
      ]
      let i = 0
      let timer: ReturnType<typeof setTimeout> | undefined
      cancel = () => {
        clearTimeout(timer)
        reject(new Error('pull cancelled'))
      }
      const step = () => {
        if (model.includes('missing') && i === 1) {
          reject(new Error('pull model manifest: file does not exist'))
          return
        }
        const s = steps[i++]!
        handlers.onProgress?.(s)
        if (s.done) {
          if (!p.models.some(m => m.name === model)) {
            p.models.push(mockLocalModel(p.id, model, { tools: true, sizeBytes: total, modifiedAt: new Date().toISOString() }))
            p.models.sort((a, b) => a.name.localeCompare(b.name))
          }
          resolve()
          return
        }
        timer = setTimeout(step, this.tick)
      }
      step()
    })
    return { done, cancel: () => cancel() }
  }

  async deleteOllamaModel(model: string, providerId = 'ollama') {
    const p = this.ollama(providerId)
    if (!p.models.some(m => m.name === model)) throw new Error(`model '${model}' not found`)
    p.models = p.models.filter(m => m.name !== model)
  }
}

// The store keeps the transport instance across hot updates, so a changed transport would keep running the
// old code (missing methods). Reload the page instead of hot-swapping this module.
if (import.meta.hot) import.meta.hot.accept(() => window.location.reload())
