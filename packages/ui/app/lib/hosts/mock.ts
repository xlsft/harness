// HostsApi for the mock transport: no server behind it, the same rules for what the screens can see.

import type { HostChanges, HostsApi, HostView, HostWorkView, NewHost, StartWork } from './types'

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
}
