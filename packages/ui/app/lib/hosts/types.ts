// Always-on hosts (docs/design/hosts.md), as the screens see them. What a host may do is the server's to say; here are
// only the words for it.

export interface HostWorkerView {
  userId: string
  userName: string
  online: boolean
  workingSessions: number
}

export interface HostView {
  id: string
  name: string
  scope: 'user' | 'team'
  ownerName: string
  groupId: string
  groupName: string
  online: boolean
  draining: boolean
  // Why it can't take the caller's work; empty when it can.
  unavailableReason: string
  maxSessions: number
  maxSessionsPerUser: number
  workingSessions: number
  queuedWork: number
  isolation: 'none' | 'uid' | ''
  sandboxDriver: string
  os: string
  arch: string
  clientVersion: string
  cpuCount: number
  loadAverage: number
  diskFreeBytes: number
  allowedRepositories: string[]
  awayHost: boolean
  lastSeenAt: string
  workers: HostWorkerView[]
}

export type WorkState = 'queued' | 'preparing' | 'running' | 'done' | 'failed' | 'cancelled' | 'expired'
export type WorkLanding = 'pull_request' | 'branch' | 'none'

export interface HostWorkView {
  id: string
  hostId: string
  hostName: string
  state: WorkState
  repositoryKey: string
  branch: string
  title: string
  sessionId: string
  pullRequestUrl: string
  error: string
  createdAt: string
}

export interface NewHost {
  name: string
  team: boolean
  groupId: string
  maxSessions: number
  maxSessionsPerUser: number
  allowedRepositories: string[]
}

export interface HostChanges {
  name?: string
  draining?: boolean
  maxSessions?: number
  maxSessionsPerUser?: number
  allowedRepositories?: string[]
}

export interface StartWork {
  hostId: string
  repositoryKey: string
  baseRef: string
  prompt: string
  model: string
  effort?: string
  permissionMode?: string
  landing: WorkLanding
  freshClone: boolean
  idempotencyKey: string
}

export interface HostsApi {
  list(all?: boolean): Promise<HostView[]>
  // The token comes once.
  create(host: NewHost): Promise<{ host: HostView, deviceToken: string }>
  update(id: string, changes: HostChanges): Promise<HostView>
  remove(id: string): Promise<void>
  start(work: StartWork): Promise<HostWorkView>
  get(id: string): Promise<HostWorkView>
  listWork(hostId?: string): Promise<HostWorkView[]>
  cancel(id: string): Promise<HostWorkView>
  setAway(hostId: string): Promise<void>
}

export function formatBytes(n: number): string {
  if (n <= 0) return ''
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let v = n
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v >= 10 || i === 0 ? Math.round(v) : v.toFixed(1)} ${units[i]}`
}

// The row in the environment menu.
export function hostLoadText(h: HostView): string {
  if (!h.online) return h.queuedWork ? `offline, ${h.queuedWork} waiting` : 'offline'
  const load = `${h.workingSessions}/${h.maxSessions}`
  return h.queuedWork ? `${load}, ${h.queuedWork} waiting` : load
}

export function isolationText(h: HostView): string {
  if (h.scope === 'user') return 'One user'
  if (h.isolation === 'uid') return 'A Unix user for each member'
  return h.isolation === 'none' ? 'Cannot separate users: takes no work' : 'Not started yet'
}

export function workStateText(w: HostWorkView): string {
  switch (w.state) {
    case 'queued': return 'Waiting for the host'
    case 'preparing': return 'Making the checkout'
    case 'running': return 'Running'
    case 'done': return 'Done'
    case 'cancelled': return 'Cancelled'
    case 'expired': return 'Waited too long'
    case 'failed': return w.error ? `Failed: ${w.error}` : 'Failed'
  }
}

// host/owner/name, with what people paste in front of it taken off. The server checks it again.
export function repositoryKeyOf(input: string): string {
  return input.trim().replace(/^[a-z+]+:\/\//i, '').replace(/^git@([^:]+):/, '$1/').replace(/\.git$/, '').replace(/\/+$/, '').toLowerCase()
}
