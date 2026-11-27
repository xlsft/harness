// Always-on hosts (docs/design/hosts.md), as the screens see them. What a host may do is the server's to say; here are
// only the words for it.

import { tt } from '~/utils/i18n'
import type { AttemptCheckVM, SessionMovedVM } from '~/lib/session/types'

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

// Moving a live session to a host (decisions 21 to 27): what the client found, for the confirmation.
export interface MoveProblem {
  code: string
  message: string
  paths: string[]
}

export interface MoveCheck {
  ok: boolean
  problems: MoveProblem[]
  hostName: string
  repositoryKey: string
  branch: string
  base: string
  commits: number
  uncommittedFiles: number
  // The mode the host session gets, and why it is not the session's when it is not.
  permissionMode: string
  permissionNote: string
  working: boolean
}

export interface MoveOptions {
  stopTurn: boolean
  message: string
  openPullRequest: boolean
}

// Best-of-N (decisions 28 to 33).
export type AttemptState = 'pending' | 'running' | 'idle' | 'failed' | 'discarding' | 'discarded'

export interface AttemptView {
  index: number
  state: AttemptState
  workId: string
  sessionId: string
  branch: string
  error: string
  check: AttemptCheckVM | null
  kept: boolean
}

export interface AttemptGroupView {
  id: string
  prompt: string
  title: string
  // Empty: worktrees of the client in cwd.
  hostId: string
  hostName: string
  cwd: string
  repositoryKey: string
  testCommand: string
  parallel: number
  budgetUsd: number
  keptIndex: number
  attempts: AttemptView[]
  createdAt: string
}

export interface StartAttempts {
  hostId: string
  count: number
  prompt: string
  title: string
  repositoryKey: string
  baseRef: string
  model: string
  effort?: string
  permissionMode?: string
  testCommand: string
  // 0: the user's settings.
  parallel: number
  budgetUsd: number
  cwd: string
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
  // The client of this window checks and moves its own session.
  checkMove(sessionId: string, hostId: string): Promise<MoveCheck>
  move(sessionId: string, hostId: string, o: MoveOptions): Promise<SessionMovedVM>
  startAttempts(s: StartAttempts): Promise<AttemptGroupView>
  getGroup(id: string): Promise<AttemptGroupView>
  listGroups(): Promise<AttemptGroupView[]>
  keepAttempt(groupId: string, index: number): Promise<AttemptGroupView>
  // After the user confirmed: the attempts other than the kept one go, with their checkouts and branches.
  discardAttempts(groupId: string): Promise<AttemptGroupView>
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
  if (!h.online) return h.queuedWork ? tt('offline, {0} waiting', [h.queuedWork]) : tt('offline')
  const load = `${h.workingSessions}/${h.maxSessions}`
  return h.queuedWork ? tt('{0}, {1} waiting', [load, h.queuedWork]) : load
}

export function isolationText(h: HostView): string {
  if (h.scope === 'user') return tt('One user')
  if (h.isolation === 'uid') return tt('A Unix user for each member')
  return h.isolation === 'none' ? tt('Cannot separate users: takes no work') : tt('Not started yet')
}

export function workStateText(w: HostWorkView): string {
  switch (w.state) {
    case 'queued': return tt('Waiting for the host')
    case 'preparing': return tt('Making the checkout')
    case 'running': return tt('Running')
    case 'done': return tt('Done')
    case 'cancelled': return tt('Cancelled')
    case 'expired': return tt('Waited too long')
    case 'failed': return w.error ? tt('Failed: {0}', [w.error]) : tt('Failed')
  }
}

// host/owner/name, with what people paste in front of it taken off. The server checks it again.
export function repositoryKeyOf(input: string): string {
  return input.trim().replace(/^[a-z+]+:\/\//i, '').replace(/^git@([^:]+):/, '$1/').replace(/\.git$/, '').replace(/\/+$/, '').toLowerCase()
}

export function attemptStateText(a: AttemptView): string {
  switch (a.state) {
    case 'pending': return tt('Waiting for its turn')
    case 'running': return tt('Working')
    case 'idle': return tt('Finished its turn')
    case 'failed': return a.error ? tt('Failed: {0}', [a.error]) : tt('Failed')
    case 'discarding': return tt('Being removed')
    case 'discarded': return tt('Discarded')
  }
}

// One line about a test run: passed, failed with its code, refused or out of time.
export function attemptCheckText(c: AttemptCheckVM | null): string {
  if (!c) return ''
  if (c.refused) return tt('Tests not run: a deny rule covers the command')
  if (c.timedOut) return tt('Tests ran out of time')
  return c.exitCode === 0 ? tt('Tests passed') : tt('Tests failed (exit {0})', [c.exitCode])
}
