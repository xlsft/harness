// HostsApi for the mock transport: no server behind it, the same rules for what the screens can see.

import type { AttemptGroupView, HostChanges, HostsApi, HostView, HostWorkView, MoveOptions, NewHost, StartAttempts, StartWork } from './types'

function sampleHost(over: Partial<HostView>): HostView {
  return {
    id: 'ahs_box',
    name: 'build-box',
    scope: 'user',
    ownerName: 'You',
    groupId: '',
    groupName: '',
    online: true,
    draining: false,
    unavailableReason: '',
    maxSessions: 4,
    maxSessionsPerUser: 0,
    workingSessions: 1,
    queuedWork: 0,
    isolation: 'none',
    sandboxDriver: 'linux',
    os: 'linux',
    arch: 'amd64',
    clientVersion: 'dev',
    cpuCount: 8,
    loadAverage: 0.7,
    diskFreeBytes: 120 * 1024 ** 3,
    allowedRepositories: [],
    awayHost: true,
    lastSeenAt: new Date().toISOString(),
    workers: [],
    ...over
  }
}

export class MockHostsApi implements HostsApi {
  hosts: HostView[] = [
    sampleHost({}),
    sampleHost({ id: 'ahs_team', name: 'team-runner', scope: 'team', groupName: 'Platform', isolation: 'uid', maxSessions: 16, maxSessionsPerUser: 2, workingSessions: 5, awayHost: false }),
    sampleHost({ id: 'ahs_off', name: 'old-box', online: false, awayHost: false })
  ]

  work: HostWorkView[] = []

  async list() {
    return this.hosts
  }

  async create(h: NewHost) {
    const host = sampleHost({ id: `ahs_${this.hosts.length + 1}`, name: h.name, scope: h.team ? 'team' : 'user', groupId: h.groupId, online: false, workingSessions: 0, maxSessions: h.maxSessions || 4, maxSessionsPerUser: h.maxSessionsPerUser, allowedRepositories: h.allowedRepositories, isolation: '' })
    this.hosts = [...this.hosts, host]
    return { host, deviceToken: 'hdt_mock_token_shown_once' }
  }

  async update(id: string, c: HostChanges) {
    const i = this.hosts.findIndex(h => h.id === id)
    if (i < 0) throw new Error('not found')
    const next = { ...this.hosts[i]!, ...c }
    this.hosts = this.hosts.map(h => (h.id === id ? next : h))
    return next
  }

  async remove(id: string) {
    this.hosts = this.hosts.filter(h => h.id !== id)
  }

  async start(w: StartWork) {
    const row: HostWorkView = { id: `hwk_${this.work.length + 1}`, hostId: w.hostId, hostName: this.hosts.find(h => h.id === w.hostId)?.name ?? '', state: 'queued', repositoryKey: w.repositoryKey, branch: '', title: '', sessionId: '', pullRequestUrl: '', error: '', createdAt: new Date().toISOString() }
    this.work = [row, ...this.work]
    return row
  }

  async get(id: string) {
    const w = this.work.find(x => x.id === id)
    if (!w) throw new Error('not found')
    return w
  }

  async listWork(hostId = '') {
    return this.work.filter(w => !hostId || w.hostId === hostId)
  }

  async cancel(id: string) {
    const w = await this.get(id)
    w.state = 'cancelled'
    return w
  }

  async setAway(hostId: string) {
    this.hosts = this.hosts.map(h => ({ ...h, awayHost: h.id === hostId }))
  }

  groups: AttemptGroupView[] = []

  async checkMove(sessionId: string, hostId: string) {
    const host = this.hosts.find(h => h.id === hostId)
    const problems = host ? [] : [{ code: 'host', message: 'That host is not one of yours', paths: [] }]
    return { ok: !problems.length, problems, hostName: host?.name ?? '', repositoryKey: 'github.com/demo/project', branch: `harness/${sessionId}`, base: 'main', commits: 2, uncommittedFiles: 3, permissionMode: 'default', permissionNote: '', working: false }
  }

  async move(sessionId: string, hostId: string, _o: MoveOptions) {
    const host = this.hosts.find(h => h.id === hostId)
    if (!host) throw new Error('[failed_precondition] That host is not one of yours')
    const row = await this.start({ hostId, repositoryKey: 'github.com/demo/project', baseRef: 'main', prompt: '', model: '', landing: 'branch', freshClone: false, idempotencyKey: sessionId })
    return { hostId, hostName: host.name, workId: row.id, branch: `harness/${sessionId}`, movedAt: new Date().toISOString() }
  }

  async startAttempts(a: StartAttempts) {
    if (a.count < 2 || a.count > 5) throw new Error('[invalid_argument] A group has 2 to 5 attempts')
    const id = `atg_${this.groups.length + 1}`
    const g: AttemptGroupView = {
      id, prompt: a.prompt, title: a.title, hostId: a.hostId, hostName: this.hosts.find(h => h.id === a.hostId)?.name ?? '', cwd: a.cwd, repositoryKey: a.repositoryKey,
      testCommand: a.testCommand, parallel: a.parallel || a.count, budgetUsd: a.budgetUsd, keptIndex: -1, createdAt: new Date().toISOString(),
      attempts: Array.from({ length: a.count }, (_, i) => ({ index: i, state: 'pending' as const, workId: '', sessionId: '', branch: `harness/attempt-${id}-${i + 1}`, error: '', check: null, kept: false }))
    }
    this.groups = [g, ...this.groups]
    return g
  }

  async getGroup(id: string) {
    const g = this.groups.find(x => x.id === id)
    if (!g) throw new Error('[not_found] not found')
    return g
  }

  async listGroups() {
    return this.groups
  }

  async keepAttempt(groupId: string, index: number) {
    const g = await this.getGroup(groupId)
    g.keptIndex = index
    g.attempts = g.attempts.map(a => ({ ...a, kept: a.index === index }))
    return g
  }

  async discardAttempts(groupId: string) {
    const g = await this.getGroup(groupId)
    if (g.keptIndex < 0) throw new Error('[failed_precondition] Keep one attempt first')
    g.attempts = g.attempts.map(a => (a.index === g.keptIndex ? a : { ...a, state: 'discarded' as const }))
    return g
  }
}
