// HostsApi for the mock transport: no server behind it, the same rules for what the screens can see.

import { CLOUD_HOST, emptyRunnerConfig, type CloudSessionView, type HostChanges, type HostsApi, type HostView, type HostWorkView, type NewHost, type StartWork } from './types'

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
    cloud: null,
    cloudStats: null,
    ...over
  }
}

export class MockHostsApi implements HostsApi {
  hosts: HostView[] = [
    sampleHost({}),
    sampleHost({ id: 'ahs_team', name: 'team-runner', scope: 'team', groupName: 'Platform', isolation: 'uid', maxSessions: 16, maxSessionsPerUser: 2, workingSessions: 5, awayHost: false }),
    sampleHost({ id: 'ahs_off', name: 'old-box', online: false, awayHost: false }),
    sampleHost({ id: 'ahs_cloud', name: 'runner-1', scope: 'cloud', isolation: 'container', sandboxDriver: 'container', maxSessions: 20, workingSessions: 3, awayHost: false, cloud: { ...emptyRunnerConfig(), image: 'harness-agent', runtime: 'runsc' }, cloudStats: { starting: 0, running: 1, idle: 2, removed: 4, failed: 0 } })
  ]

  cloudSessions: CloudSessionView[] = [
    { id: 'cls_1', hostName: 'runner-1', userName: 'You', sessionId: '', state: 'idle', image: 'harness-agent', repositoryKey: 'github.com/acme/widgets', error: '', diskBytes: 340 * 1024 ** 2, starts: 2, startedAt: new Date().toISOString(), lastActiveAt: new Date().toISOString() }
  ]

  work: HostWorkView[] = []

  async list() {
    return this.hosts
  }

  async create(h: NewHost) {
    const host = sampleHost({ id: `ahs_${this.hosts.length + 1}`, name: h.name, scope: h.runner ? 'cloud' : h.team ? 'team' : 'user', cloud: h.runner ?? null, groupId: h.groupId, online: false, workingSessions: 0, maxSessions: h.maxSessions || 4, maxSessionsPerUser: h.maxSessionsPerUser, allowedRepositories: h.allowedRepositories, isolation: '' })
    this.hosts = [...this.hosts, host]
    return { host, deviceToken: 'hdt_mock_token_shown_once' }
  }

  async update(id: string, c: HostChanges) {
    const i = this.hosts.findIndex(h => h.id === id)
    if (i < 0) throw new Error('not found')
    const { runner, ...rest } = c
    const next = { ...this.hosts[i]!, ...rest, ...(runner ? { cloud: runner } : {}) }
    this.hosts = this.hosts.map(h => (h.id === id ? next : h))
    return next
  }

  async remove(id: string) {
    this.hosts = this.hosts.filter(h => h.id !== id)
  }

  async start(w: StartWork) {
    const cloud = w.hostId === CLOUD_HOST
    const host = cloud ? this.hosts.find(h => h.scope === 'cloud') : this.hosts.find(h => h.id === w.hostId)
    const row: HostWorkView = { id: `hwk_${this.work.length + 1}`, hostId: host?.id ?? w.hostId, hostName: host?.name ?? '', state: 'queued', repositoryKey: w.repositoryKey, branch: '', title: '', sessionId: '', pullRequestUrl: '', error: '', createdAt: new Date().toISOString(), cloud, cloudState: cloud ? 'starting' : '' }
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

  async listCloudSessions() {
    return this.cloudSessions
  }

  async stopCloudSession(id: string, end: boolean) {
    const c = this.cloudSessions.find(x => x.id === id)
    if (!c) throw new Error('not found')
    c.state = end ? 'ended' : 'removed'
    return c
  }
}
