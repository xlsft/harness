// HostsApi over the server's HostsService: through the client, which forwards it (packages/client/server/coreproxy.go), or
// directly in the web console.

import { createClient, type Client, type Transport } from '@connectrpc/connect'
import { AgentService, PermissionMode, type AttemptCheck } from '~/gen/harness/agent/v1/agent_pb'
import { Effort } from '~/gen/harness/llm/v1/content_pb'
import { AgentHostIsolation, AgentHostScope, AttemptState as PbAttemptState, HostsService, HostWorkLanding, HostWorkState, LocalAttemptsService, type AgentHost, type AttemptGroup, type CloudRunnerConfig, type CloudSession, type HostWork } from '~/gen/harness/relay/v1/hosts_pb'
import { cloudEnvironmentName, permissionModeName } from '~/lib/session/reducer'
import type { AttemptCheckVM } from '~/lib/session/types'
import { CLOUD_HOST, type AttemptGroupView, type AttemptState, type CloudSessionView, type HostChanges, type HostsApi, type HostView, type HostWorkView, type MoveOptions, type NewHost, type RunnerConfigView, type StartAttempts, type StartWork, type WorkLanding, type WorkState } from './types'

export function runnerConfigView(c: CloudRunnerConfig): RunnerConfigView {
  return {
    image: c.image,
    allowedImages: c.allowedImages,
    runtime: c.runtime,
    cpuMillis: c.cpuMillis,
    memoryBytes: Number(c.memoryBytes),
    diskBytes: Number(c.diskBytes),
    pids: c.pids,
    idleTimeoutSeconds: c.idleTimeoutSeconds,
    maxLifetimeSeconds: c.maxLifetimeSeconds,
    cacheMaxBytes: Number(c.cacheMaxBytes),
    cacheScope: c.cacheScope === 'runner' ? 'runner' : 'user',
    storageOptSize: c.storageOptSize
  }
}

function runnerConfigProto(c: RunnerConfigView) {
  return {
    image: c.image.trim(),
    allowedImages: c.allowedImages.map(s => s.trim()).filter(Boolean),
    runtime: c.runtime.trim(),
    cpuMillis: c.cpuMillis,
    memoryBytes: BigInt(Math.round(c.memoryBytes)),
    diskBytes: BigInt(Math.round(c.diskBytes)),
    pids: c.pids,
    idleTimeoutSeconds: c.idleTimeoutSeconds,
    maxLifetimeSeconds: c.maxLifetimeSeconds,
    cacheMaxBytes: BigInt(Math.round(c.cacheMaxBytes)),
    cacheScope: c.cacheScope,
    storageOptSize: c.storageOptSize
  }
}

export function cloudSessionView(c: CloudSession): CloudSessionView {
  return {
    id: c.id,
    hostName: c.hostName,
    userName: c.userName,
    sessionId: c.sessionId,
    state: cloudEnvironmentName(c.state),
    image: c.image,
    repositoryKey: c.repositoryKey,
    error: c.error,
    diskBytes: Number(c.diskBytes),
    starts: c.starts,
    startedAt: c.startedAt,
    lastActiveAt: c.lastActiveAt
  }
}

function scopeOf(s: AgentHostScope): HostView['scope'] {
  return s === AgentHostScope.TEAM ? 'team' : s === AgentHostScope.CLOUD ? 'cloud' : 'user'
}

function isolationOf(i: AgentHostIsolation): HostView['isolation'] {
  switch (i) {
    case AgentHostIsolation.UID: return 'uid'
    case AgentHostIsolation.NONE: return 'none'
    case AgentHostIsolation.CONTAINER: return 'container'
  }
  return ''
}

export function hostView(h: AgentHost): HostView {
  return {
    id: h.id,
    name: h.name,
    scope: scopeOf(h.scope),
    ownerName: h.ownerName,
    groupId: h.groupId,
    groupName: h.groupName,
    online: h.online,
    draining: h.draining,
    unavailableReason: h.unavailableReason,
    maxSessions: h.maxSessions,
    maxSessionsPerUser: h.maxSessionsPerUser,
    workingSessions: h.workingSessions,
    queuedWork: h.queuedWork,
    isolation: isolationOf(h.isolation),
    sandboxDriver: h.sandboxDriver,
    os: h.os,
    arch: h.arch,
    clientVersion: h.clientVersion,
    cpuCount: h.cpuCount,
    loadAverage: h.loadAverage,
    diskFreeBytes: Number(h.diskFreeBytes),
    allowedRepositories: h.allowedRepositories,
    awayHost: h.awayHost,
    lastSeenAt: h.lastSeenAt,
    workers: h.workers.map(w => ({ userId: w.userId, userName: w.userName, online: w.online, workingSessions: w.workingSessions })),
    cloud: h.cloud ? runnerConfigView(h.cloud) : null,
    cloudStats: h.cloudStats ? { starting: h.cloudStats.starting, running: h.cloudStats.running, idle: h.cloudStats.idle, removed: h.cloudStats.removed, failed: h.cloudStats.failed } : null
  }
}

const STATE: Partial<Record<HostWorkState, WorkState>> = {
  [HostWorkState.QUEUED]: 'queued',
  [HostWorkState.CLAIMED]: 'preparing',
  [HostWorkState.RUNNING]: 'running',
  [HostWorkState.DONE]: 'done',
  [HostWorkState.FAILED]: 'failed',
  [HostWorkState.CANCELLED]: 'cancelled',
  [HostWorkState.EXPIRED]: 'expired'
}

export function workView(w: HostWork | undefined): HostWorkView {
  return {
    id: w?.id ?? '',
    hostId: w?.hostId ?? '',
    hostName: w?.hostName ?? '',
    state: (w && STATE[w.state]) ?? 'queued',
    repositoryKey: w?.repositoryKey ?? '',
    branch: w?.branch ?? '',
    title: w?.title ?? '',
    sessionId: w?.sessionId ?? '',
    pullRequestUrl: w?.pullRequestUrl ?? '',
    error: w?.error ?? '',
    createdAt: w?.createdAt ?? '',
    cloud: w?.cloud ?? false,
    cloudState: w ? cloudEnvironmentName(w.cloudState) : ''
  }
}

const LANDING: Record<WorkLanding, HostWorkLanding> = {
  pull_request: HostWorkLanding.PULL_REQUEST,
  branch: HostWorkLanding.BRANCH,
  none: HostWorkLanding.NONE
}

const MODE: Record<string, PermissionMode> = {
  default: PermissionMode.DEFAULT,
  acceptEdits: PermissionMode.ACCEPT_EDITS,
  plan: PermissionMode.PLAN,
  auto: PermissionMode.AUTO
}

const EFFORT: Record<string, Effort> = { low: Effort.LOW, medium: Effort.MEDIUM, high: Effort.HIGH, xhigh: Effort.XHIGH, max: Effort.MAX }

const ATTEMPT_STATE: Partial<Record<PbAttemptState, AttemptState>> = {
  [PbAttemptState.PENDING]: 'pending',
  [PbAttemptState.RUNNING]: 'running',
  [PbAttemptState.IDLE]: 'idle',
  [PbAttemptState.FAILED]: 'failed',
  [PbAttemptState.DISCARDING]: 'discarding',
  [PbAttemptState.DISCARDED]: 'discarded'
}

function checkView(c: AttemptCheck | undefined): AttemptCheckVM | null {
  return c ? { command: c.command, exitCode: c.exitCode, output: c.output, refused: c.refused, timedOut: c.timedOut, head: c.head, checkedAt: c.checkedAt } : null
}

export function groupView(g: AttemptGroup | undefined): AttemptGroupView {
  return {
    id: g?.id ?? '',
    prompt: g?.prompt ?? '',
    title: g?.title ?? '',
    hostId: g?.hostId ?? '',
    hostName: g?.hostName ?? '',
    cwd: g?.cwd ?? '',
    repositoryKey: g?.repositoryKey ?? '',
    testCommand: g?.testCommand ?? '',
    parallel: g?.parallel ?? 0,
    budgetUsd: g?.budgetUsd ?? 0,
    keptIndex: g?.keptIndex ?? -1,
    attempts: (g?.attempts ?? []).map(a => ({ index: a.index, state: ATTEMPT_STATE[a.state] ?? 'pending', workId: a.workId, sessionId: a.sessionId, branch: a.branch, error: a.error, check: checkView(a.check), kept: a.kept })),
    createdAt: g?.createdAt ?? ''
  }
}

export class ConnectHostsApi implements HostsApi {
  private rpc: Client<typeof HostsService>
  private agent: Client<typeof AgentService>
  private local: Client<typeof LocalAttemptsService>

  constructor(transport: Transport) {
    this.rpc = createClient(HostsService, transport)
    this.agent = createClient(AgentService, transport)
    this.local = createClient(LocalAttemptsService, transport)
  }

  async checkMove(sessionId: string, hostId: string) {
    const c = (await this.agent.checkSessionMove({ sessionId, hostId })).check
    return {
      ok: c?.ok ?? false,
      problems: (c?.problems ?? []).map(p => ({ code: p.code, message: p.message, paths: p.paths })),
      hostName: c?.hostName ?? '',
      repositoryKey: c?.repositoryKey ?? '',
      branch: c?.branch ?? '',
      base: c?.base ?? '',
      commits: c?.commits ?? 0,
      uncommittedFiles: c?.uncommittedFiles ?? 0,
      permissionMode: permissionModeName(c?.permissionMode ?? PermissionMode.UNSPECIFIED),
      permissionNote: c?.permissionNote ?? '',
      working: c?.working ?? false
    }
  }

  async move(sessionId: string, hostId: string, o: MoveOptions) {
    const m = (await this.agent.moveSession({ sessionId, hostId, stopTurn: o.stopTurn, message: o.message, openPullRequest: o.openPullRequest })).moved
    return { hostId: m?.hostId ?? hostId, hostName: m?.hostName ?? '', workId: m?.workId ?? '', branch: m?.branch ?? '', movedAt: m?.movedAt ?? '' }
  }

  // Through the client: it runs a local group's attempts, and passes a host group to the server.
  async startAttempts(a: StartAttempts) {
    const res = await this.local.startLocalAttempts({
      request: {
        hostId: a.hostId,
        count: a.count,
        prompt: a.prompt,
        title: a.title,
        repositoryKey: a.repositoryKey,
        baseRef: a.baseRef,
        model: a.model,
        effort: a.effort ? EFFORT[a.effort] : undefined,
        permissionMode: a.permissionMode ? MODE[a.permissionMode] : undefined,
        testCommand: a.testCommand,
        parallel: a.parallel,
        budgetUsd: a.budgetUsd,
        cwd: a.cwd,
        idempotencyKey: a.idempotencyKey
      }
    })
    return groupView(res.group)
  }

  async getGroup(id: string) {
    return groupView((await this.rpc.getAttemptGroup({ id })).group)
  }

  async listGroups() {
    return (await this.rpc.listAttemptGroups({ limit: 20 })).groups.map(groupView)
  }

  async keepAttempt(groupId: string, index: number) {
    return groupView((await this.rpc.keepAttempt({ groupId, index })).group)
  }

  async discardAttempts(groupId: string) {
    return groupView((await this.local.discardLocalAttempts({ groupId })).group)
  }

  async list(all = false) {
    return (await this.rpc.listAgentHosts({ all })).hosts.map(hostView)
  }

  async create(h: NewHost) {
    const res = await this.rpc.createAgentHost({
      name: h.name,
      scope: h.runner ? AgentHostScope.CLOUD : h.team ? AgentHostScope.TEAM : AgentHostScope.USER,
      cloud: h.runner ? runnerConfigProto(h.runner) : undefined,
      groupId: h.groupId,
      maxSessions: h.maxSessions,
      maxSessionsPerUser: h.maxSessionsPerUser,
      allowedRepositories: h.allowedRepositories
    })
    return { host: hostView(res.host!), deviceToken: res.deviceToken }
  }

  async update(id: string, c: HostChanges) {
    const res = await this.rpc.updateAgentHost({
      id,
      name: c.name,
      draining: c.draining,
      maxSessions: c.maxSessions,
      maxSessionsPerUser: c.maxSessionsPerUser,
      setAllowedRepositories: c.allowedRepositories !== undefined,
      allowedRepositories: c.allowedRepositories ?? [],
      groupId: c.groupId,
      cloud: c.runner ? runnerConfigProto(c.runner) : undefined
    })
    return hostView(res.host!)
  }

  async remove(id: string) {
    await this.rpc.deleteAgentHost({ id })
  }

  async start(w: StartWork) {
    const cloud = w.hostId === CLOUD_HOST
    const res = await this.rpc.startHostWork({
      hostId: cloud ? '' : w.hostId,
      cloud,
      repositoryKey: w.repositoryKey,
      baseRef: w.baseRef,
      prompt: w.prompt,
      model: w.model,
      effort: w.effort ? EFFORT[w.effort] : undefined,
      permissionMode: w.permissionMode ? MODE[w.permissionMode] : undefined,
      landing: LANDING[w.landing],
      freshClone: w.freshClone,
      idempotencyKey: w.idempotencyKey
    })
    return workView(res.work)
  }

  async get(id: string) {
    return workView((await this.rpc.getHostWork({ id })).work)
  }

  async listWork(hostId = '') {
    return (await this.rpc.listHostWork({ hostId })).work.map(workView)
  }

  async cancel(id: string) {
    return workView((await this.rpc.cancelHostWork({ id })).work)
  }

  async setAway(hostId: string) {
    await this.rpc.setAwayHost({ hostId })
  }

  async listCloudSessions(hostId = '') {
    return (await this.rpc.listCloudSessions({ hostId })).sessions.map(cloudSessionView)
  }

  async stopCloudSession(id: string, end: boolean) {
    return cloudSessionView((await this.rpc.stopCloudSession({ id, end })).session!)
  }
}
