// The mock client's inbox and project runs: enough for the screens to be used without a client behind them. The groups
// and limits here are a stand-in for what the client decides.
import { clone, create, type MessageInitShape } from '@bufbuild/protobuf'
import {
  InboxCountSchema,
  InboxItemSchema,
  ListInboxResponseSchema,
  type InboxChange,
  type InboxCount,
  type InboxItem
} from '~/gen/harness/agent/v1/inbox_pb'
import { ProjectRunSchema, type CreateProjectRunRequestSchema } from '~/gen/harness/agent/v1/project_runs_pb'
import { SubagentState } from '~/gen/harness/agent/v1/subagents_pb'
import {
  ControlProjectRunRequest_Action,
  INBOX_GROUPS,
  InboxGroup,
  InboxRequest_Kind,
  ProjectRunState,
  ProjectWorkerState,
  type InboxApi,
  type ProjectRun,
  type ProjectRunLimits,
  type ProjectRunsApi
} from './types'

type Listener = (c: InboxChange, counts: InboxCount[]) => void

export class MockInboxApi implements InboxApi {
  private items = new Map<string, InboxItem>()
  private listeners = new Set<Listener>()

  constructor() {
    const now = Date.now()
    const at = (s: number) => new Date(now - s * 1000).toISOString()
    const add = (i: MessageInitShape<typeof InboxItemSchema>) => this.items.set(i.sessionId!, create(InboxItemSchema, { open: true, deviceOnline: true, ...i }))
    add({ sessionId: 'mock-needs', title: 'Fix the flaky upload test', cwd: '~/Projects/app', group: InboxGroup.NEEDS_INPUT, unread: true, working: true, attentionAt: at(30),
      requests: [{ kind: InboxRequest_Kind.PERMISSION, requestId: 'perm-1', toolName: 'Bash', summary: 'npm run test -- upload', suggestedRules: ['Bash(npm run test *)'] }] })
    add({ sessionId: 'mock-question', title: 'Pick a date library', remote: true, deviceName: 'studio', group: InboxGroup.NEEDS_INPUT, unread: true, attentionAt: at(120),
      requests: [{ kind: InboxRequest_Kind.QUESTION, requestId: 'q-1', questions: [{ question: 'Which library should we use?', header: 'Library', options: ['date-fns', 'dayjs'] }] }] })
    add({ sessionId: 'mock-failed', title: 'Migrate the config loader', group: InboxGroup.FAILED, unread: true, error: 'The provider returned 529 three times', attentionAt: at(900) })
    add({ sessionId: 'mock-done', title: 'Write release notes', group: InboxGroup.FINISHED, report: 'Release notes are in docs/release.md, grouped by area.', attentionAt: at(3600) })
    add({ sessionId: 'mock-running', title: 'Index the monorepo', remote: true, deviceName: 'build host', group: InboxGroup.RUNNING, working: true, attentionAt: '',
      agents: [{ id: 'a1', name: 'Explore packages', state: SubagentState.RUNNING, currentTool: 'Grep' }] })
    for (let i = 0; i < 120; i++) {
      add({ sessionId: `mock-old-${i}`, title: `Earlier task ${i + 1}`, group: InboxGroup.FINISHED, report: 'Done.', attentionAt: at(7200 + i * 60) })
    }
  }

  private counts(): InboxCount[] {
    return INBOX_GROUPS.map((g) => {
      const items = [...this.items.values()].filter(i => i.group === g)
      return create(InboxCountSchema, { group: g, total: items.length, unread: items.filter(i => i.unread).length })
    })
  }

  private emit(c: InboxChange) {
    const counts = this.counts()
    for (const l of this.listeners) l(c, counts)
  }

  async list(group: InboxGroup, pageToken = '', pageSize = 50) {
    const all = [...this.items.values()].filter(i => i.group === group)
      .sort((a, b) => (a.unread !== b.unread ? (a.unread ? -1 : 1) : b.attentionAt.localeCompare(a.attentionAt)))
    const off = Number(pageToken || 0)
    const page = all.slice(off, off + pageSize).map(i => clone(InboxItemSchema, i))
    return create(ListInboxResponseSchema, { items: page, nextPageToken: off + pageSize < all.length ? String(off + pageSize) : '', counts: this.counts() })
  }

  async watch(h: { onChange: Listener, onResync: () => void }, signal: AbortSignal) {
    this.listeners.add(h.onChange)
    await new Promise<void>(resolve => signal.addEventListener('abort', () => resolve(), { once: true }))
    this.listeners.delete(h.onChange)
  }

  async markRead(ids: string[], read: boolean) {
    for (const id of ids) {
      const it = this.items.get(id)
      if (!it) continue
      it.unread = !read
      this.emit({ $typeName: 'harness.agent.v1.InboxChange', item: clone(InboxItemSchema, it), removedSessionId: '' })
    }
  }

  // An answer from the inbox: the request goes, the session works again.
  resolve(sessionId: string, requestId: string) {
    const it = this.items.get(sessionId)
    if (!it) return
    it.requests = it.requests.filter(r => r.requestId !== requestId)
    if (!it.requests.length) {
      it.group = InboxGroup.RUNNING
      it.working = true
    }
    this.emit({ $typeName: 'harness.agent.v1.InboxChange', item: clone(InboxItemSchema, it), removedSessionId: '' })
  }

  stop(sessionId: string) {
    const it = this.items.get(sessionId)
    if (!it) return
    this.items.delete(sessionId)
    this.emit({ $typeName: 'harness.agent.v1.InboxChange', removedSessionId: sessionId })
  }
}

export class MockProjectRunsApi implements ProjectRunsApi {
  private runs = new Map<string, ProjectRun>()
  private n = 0

  constructor() {
    const now = new Date().toISOString()
    const run = create(ProjectRunSchema, {
      id: 'prun_demo', goal: 'Add dark mode to the settings screens', title: 'Dark mode for settings', cwd: '~/Projects/app', coordinatorSessionId: 'mock-coordinator',
      state: ProjectRunState.RUNNING, limits: { maxWorkers: 3, budgetUsd: 10 }, usage: { inputTokens: 210_000n, outputTokens: 18_000n, costUsd: 2.4 },
      permissionMode: 'acceptEdits', deviceName: 'this machine', createdAt: now, updatedAt: now,
      workers: [
        { sessionId: 'mock-w1', name: 'w-1-a1b2', taskId: 't1', title: 'Theme tokens', state: ProjectWorkerState.FINISHED, target: 'worktree', branch: 'harness/w-1', report: 'Tokens added for both themes.', usage: { costUsd: 0.9 } },
        { sessionId: 'mock-w2', name: 'w-2-c3d4', taskId: 't2', title: 'Settings pages', state: ProjectWorkerState.RUNNING, target: 'worktree', branch: 'harness/w-2', usage: { costUsd: 0.7 } }
      ],
      tasks: [
        { id: 't1', title: 'Theme tokens', status: 3 },
        { id: 't2', title: 'Settings pages', status: 2, owner: 'w-2-c3d4' },
        { id: 't3', title: 'Screenshots in docs', status: 1, blockedBy: ['t2'], waitingFor: ['t2'] }
      ],
      targets: [{ id: 'worktree', label: 'a new git worktree on this machine', available: true }, { id: 'here', label: 'the coordinator\'s folder', available: true }]
    })
    this.runs.set(run.id, run)
  }

  async list() {
    return [...this.runs.values()].map(r => clone(ProjectRunSchema, r)).sort((a, b) => b.createdAt.localeCompare(a.createdAt))
  }

  async get(id: string) {
    const r = this.runs.get(id)
    if (!r) throw new Error(`[not_found] project run ${id} not found on this machine`)
    return clone(ProjectRunSchema, r)
  }

  async create(req: MessageInitShape<typeof CreateProjectRunRequestSchema>) {
    if (!req.goal?.trim()) throw new Error('[invalid_argument] goal is required')
    if (!req.cwd?.startsWith('/') && !req.cwd?.startsWith('~')) throw new Error('[invalid_argument] cwd must be an absolute folder')
    const now = new Date().toISOString()
    const run = create(ProjectRunSchema, {
      id: `prun_mock-${++this.n}`, goal: req.goal, title: req.title || req.goal.slice(0, 80), cwd: req.cwd, coordinatorSessionId: `mock-coord-${this.n}`,
      state: ProjectRunState.PLANNING, limits: { maxWorkers: Math.min(32, req.limits?.maxWorkers || 4), budgetUsd: req.limits?.budgetUsd ?? 0, budgetTokens: req.limits?.budgetTokens ?? 0n },
      usage: {}, permissionMode: req.permissionMode || 'default', createdAt: now, updatedAt: now
    })
    this.runs.set(run.id, run)
    return clone(ProjectRunSchema, run)
  }

  async control(id: string, action: ControlProjectRunRequest_Action, limits?: Partial<ProjectRunLimits>) {
    const r = this.runs.get(id)
    if (!r) throw new Error(`[not_found] project run ${id} not found`)
    if (limits) r.limits = { ...r.limits!, ...limits, maxWorkers: Math.min(32, limits.maxWorkers || r.limits?.maxWorkers || 4) } as ProjectRun['limits']
    if (action === ControlProjectRunRequest_Action.PAUSE) [r.state, r.reason] = [ProjectRunState.PAUSED, 'Paused by you']
    if (action === ControlProjectRunRequest_Action.RESUME) [r.state, r.reason] = [ProjectRunState.RUNNING, '']
    if (action === ControlProjectRunRequest_Action.STOP) [r.state, r.reason] = [ProjectRunState.STOPPED, 'Stopped by you']
    return clone(ProjectRunSchema, r)
  }

  async remove(id: string) {
    const r = this.runs.get(id)
    if (r && ![ProjectRunState.DONE, ProjectRunState.FAILED, ProjectRunState.STOPPED, ProjectRunState.PAUSED].includes(r.state)) throw new Error('[failed_precondition] stop the run first')
    this.runs.delete(id)
  }
}
