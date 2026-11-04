// ConnectTransport: AgentService over connect-web (server streaming for Subscribe, unary for commands).
// The base URL comes from runtime config (NUXT_PUBLIC_AGENT_URL): `harness-client serve --listen 127.0.0.1:<port>`
// serves it. Every call carries `Authorization: Bearer <token>` when a token is given (see token.ts).

import { ResolveSessionProposalRequest_Action } from '~/gen/harness/agent/v1/session_links_pb'
import type { ComputerUseStatus } from '~/gen/harness/agent/v1/computer_use_pb'
import { permissionVM, profileMessage, profileVM, tierName } from '~/lib/session/computer'
import type { ComputerProfileVM, EffortName, QuestionReply } from '~/lib/session/types'
import type { DeviceSettings, ComputerUseStatusVM, ModelInfo, NewSessionOptions, SessionPatch, SessionTransport, SubscribeHandlers, UserPreferences } from './types'
import { create } from '@bufbuild/protobuf'
import { createClient, type Client, type Interceptor, type Transport } from '@connectrpc/connect'
import { createConnectTransport } from '@connectrpc/connect-web'
import {
  AgentService,
  QuestionOutcome,
  type PermissionMode,
  type RespondToPermissionRequest_Decision,
  type RespondToPlanRequest_Decision,
  type RewindRequest_Mode,
  type SendUserMessageRequest_Delivery
} from '~/gen/harness/agent/v1/agent_pb'
import { ContentBlockSchema, Effort } from '~/gen/harness/llm/v1/content_pb'
import { contextUsageFromProto } from '~/lib/session/context'
import { backgroundReasonPb } from '~/lib/session/reducer'
import { ConnectProvidersApi } from '~/lib/providers/connect'
import { ConnectUsersApi } from '~/lib/users/connect'
import { ConnectAccountApi } from '~/lib/account/connect'
import { ConnectUsageApi } from '~/lib/usage/connect'
import { ConnectTerminalsApi } from '~/lib/terminal/connect'
import { ConnectSideChatsApi } from '~/lib/sidechat/connect'
import { ConnectEnvironmentApi } from '~/lib/environment/connect'
import { ConnectFilesApi, diffBaseMessage, diffBasesView, sessionDiffView, sessionFileDiffView } from '~/lib/files/connect'
import type { DiffBaseChoice } from '~/lib/files/sessionDiff'
import { ConnectSpeechApi } from '~/lib/speech/connect'
import { ConnectLocalModelsApi } from './localModels'
import { ConnectLimitsApi } from '~/lib/limits/connect'
import { ConnectAcpApi } from '~/lib/acp/connect'
import { ConnectBrowserApi } from '~/lib/browser/connect'

const EFFORT_NAME: Partial<Record<Effort, EffortName>> = {
  [Effort.LOW]: 'low',
  [Effort.MEDIUM]: 'medium',
  [Effort.HIGH]: 'high',
  [Effort.XHIGH]: 'xhigh',
  [Effort.MAX]: 'max'
}

function computerStatusVM(s: ComputerUseStatus | undefined): ComputerUseStatusVM {
  if (!s) throw new Error('the client returned no status')
  return { enabled: s.enabled, supported: s.supported, platform: s.platform, unavailableReason: s.unavailableReason, permissions: s.permissions.map(permissionVM),
    activeProfileId: s.activeProfileId, profiles: s.profiles.map(profileVM), blockEntries: s.blockEntries.map(e => ({ id: e.id, label: e.label, warning: e.warning })) }
}

export class ConnectTransport implements SessionTransport {
  readonly kind = 'connect' as const
  private client: Client<typeof AgentService>
  private transport: Transport

  private providersApi: ConnectProvidersApi
  private usersApi: ConnectUsersApi
  private accountApi: ConnectAccountApi
  private usageApi: ConnectUsageApi
  private terminalsApi: ConnectTerminalsApi

  constructor(baseUrl: string, token = '') {
    const interceptors: Interceptor[] = token ? [bearer(token)] : []
    const transport = createConnectTransport({ baseUrl, interceptors })
    this.transport = transport
    this.client = createClient(AgentService, transport)
    this.providersApi = new ConnectProvidersApi(transport)
    this.usersApi = new ConnectUsersApi(transport)
    this.accountApi = new ConnectAccountApi(transport)
    this.usageApi = new ConnectUsageApi(transport)
    this.terminalsApi = new ConnectTerminalsApi(transport, baseUrl)
    this.sideChatsApi = new ConnectSideChatsApi(transport)
    this.environmentApi = new ConnectEnvironmentApi(transport)
    this.filesApi = new ConnectFilesApi(transport)
    this.speechApi = new ConnectSpeechApi(transport)
    this.limitsApi = new ConnectLimitsApi(transport)
    this.acpApi = new ConnectAcpApi(transport)
    this.browserApi = new ConnectBrowserApi(transport, baseUrl)
  }

  private browserApi: ConnectBrowserApi
  browser() {
    return this.browserApi
  }

  private limitsApi: ConnectLimitsApi
  limits() {
    return this.limitsApi
  }

  private environmentApi: ConnectEnvironmentApi
  environment() {
    return this.environmentApi
  }

  providers() {
    return this.providersApi
  }

  users() {
    return this.usersApi
  }

  account() {
    return this.accountApi
  }

  usage() {
    return this.usageApi
  }

  terminals() {
    return this.terminalsApi
  }

  private sideChatsApi: ConnectSideChatsApi
  sideChats() {
    return this.sideChatsApi
  }

  async initialize() {
    return this.client.initialize({ frontend: 'web', frontendVersion: '0.1.0' })
  }

  async listSessions() {
    return (await this.client.listSessions({})).sessions
  }

  async createSession(opts: NewSessionOptions) {
    const res = await this.client.createSession({
      cwd: opts.cwd,
      model: opts.model ?? '',
      effort: opts.effort,
      permissionMode: opts.permissionMode,
      resumeSessionId: opts.resumeSessionId ?? '',
      fork: opts.fork ?? false,
      forkAtCheckpointId: opts.forkAtMessageId ?? '',
      worktree: opts.worktree ? { name: opts.worktree.name ?? '', branch: opts.worktree.branch ?? '', baseRef: opts.worktree.baseRef ?? '' } : undefined,
      engine: opts.engine ?? ''
    })
    if (!res.session) throw new Error('the client returned no session')
    return res.session
  }

  async listRecentFolders() {
    return (await this.client.listRecentFolders({})).folders
  }

  async listDirectory(path: string) {
    return this.client.listDirectory({ path })
  }

  async listBranches(cwd: string) {
    return this.client.listBranches({ cwd })
  }

  subscribe(sessionId: string, afterSeq: number, h: SubscribeHandlers) {
    const abort = new AbortController()
    void (async () => {
      try {
        for await (const res of this.client.subscribe({ sessionId, afterSeq: BigInt(afterSeq), control: true }, { signal: abort.signal })) {
          if (res.event) h.onEvent(res.event)
          else if (res.logId) h.onControl?.({ logId: res.logId, session: res.session })
        }
        h.onEnd?.()
      } catch (err) {
        if (!abort.signal.aborted) h.onError?.(err)
      }
    })()
    return () => abort.abort()
  }

  async sendUserMessage(sessionId: string, uuid: string, text: string, delivery: SendUserMessageRequest_Delivery) {
    const content = [create(ContentBlockSchema, { block: { case: 'text', value: { text } } })]
    await this.client.sendUserMessage({ sessionId, uuid, content, delivery })
  }

  async removeQueuedMessage(sessionId: string, uuid: string) {
    return (await this.client.removeQueuedMessage({ sessionId, uuid })).removed
  }

  async rewind(sessionId: string, checkpointId: string, mode: RewindRequest_Mode) {
    const res = await this.client.rewind({ sessionId, checkpointId, mode })
    return { restoredPrompt: res.result?.restoredPrompt ?? '', filesRestored: res.result?.filesRestored ?? 0 }
  }

  async closeSession(sessionId: string) {
    await this.client.closeSession({ sessionId })
  }

  async updateSession(sessionId: string, patch: SessionPatch) {
    const res = await this.client.updateSession({ sessionId, ...patch })
    if (!res.session) throw new Error('the client returned no session')
    return res.session
  }

  async deleteSession(sessionId: string) {
    await this.client.deleteSession({ sessionId })
  }

  async interrupt(sessionId: string) {
    const res = await this.client.interrupt({ sessionId })
    return { idle: res.idle }
  }

  async respondToPermission(sessionId: string, requestId: string, decision: RespondToPermissionRequest_Decision, rule = '', comment = '') {
    await this.client.respondToPermission({ sessionId, requestId, decision, rule, comment })
  }

  async answerQuestion(sessionId: string, requestId: string, reply: QuestionReply) {
    switch (reply.outcome) {
      case 'answered':
        await this.client.answerQuestion({ sessionId, requestId, outcome: QuestionOutcome.ANSWERED, answers: reply.answers })
        break
      case 'skipped':
        await this.client.answerQuestion({ sessionId, requestId, outcome: QuestionOutcome.SKIPPED })
        break
      case 'message':
        await this.client.answerQuestion({ sessionId, requestId, outcome: QuestionOutcome.MESSAGE, messageUuids: reply.messageUuids ?? [] })
    }
  }

  async respondToPlan(sessionId: string, requestId: string, decision: RespondToPlanRequest_Decision, feedback = '') {
    await this.client.respondToPlan({ sessionId, requestId, decision, feedback })
  }

  async setModel(sessionId: string, model: string) {
    await this.client.setModel({ sessionId, model })
  }

  async setEffort(sessionId: string, effort: Effort) {
    await this.client.setEffort({ sessionId, effort })
  }

  async setPermissionMode(sessionId: string, mode: PermissionMode) {
    await this.client.setPermissionMode({ sessionId, mode })
  }

  async getComputerUseStatus() {
    return computerStatusVM((await this.client.getComputerUseStatus({})).status)
  }

  async setComputerUseEnabled(enabled: boolean) {
    return computerStatusVM((await this.client.setComputerUseEnabled({ enabled })).status)
  }

  async setComputerUseProfile(activeProfileId: string, custom?: ComputerProfileVM) {
    return computerStatusVM((await this.client.setComputerUseProfile({ activeProfileId, custom: custom && profileMessage(custom) })).status)
  }

  async listComputerUseApps() {
    return (await this.client.listComputerUseApps({})).apps.map(a => ({ id: a.id, name: a.name, tier: tierName(a.tier) }))
  }

  async respondToComputerAccess(sessionId: string, requestId: string, approvedAppIds: string[]) {
    await this.client.respondToComputerAccess({ sessionId, requestId, approvedAppIds })
  }

  async stopComputerUse(sessionId: string) {
    await this.client.stopComputerUse({ sessionId })
  }

  async setAutoContinue(sessionId: string, enabled: boolean) {
    await this.client.setAutoContinue({ sessionId, enabled })
  }

  async resolveSessionProposal(sessionId: string, proposalId: string, action: 'start' | 'dismiss') {
    await this.client.resolveSessionProposal({ sessionId, proposalId, action: action === 'start' ? ResolveSessionProposalRequest_Action.START : ResolveSessionProposalRequest_Action.DISMISS })
  }

  async setPrMonitor(sessionId: string, enabled: boolean) {
    await this.client.setPrMonitor({ sessionId, enabled })
  }

  async setKeepAwake(sessionId: string, enabled: boolean) {
    await this.client.setKeepAwake({ sessionId, enabled })
  }

  async getDeviceSettings(): Promise<DeviceSettings> {
    const res = await this.client.getDeviceSettings({})
    return { theme: res.settings?.theme ?? '', notifications: res.settings?.notifications ?? true }
  }

  async updateDeviceSettings(patch: Partial<DeviceSettings>): Promise<DeviceSettings> {
    const res = await this.client.updateDeviceSettings({ theme: patch.theme, notifications: patch.notifications })
    return { theme: res.settings?.theme ?? '', notifications: res.settings?.notifications ?? true }
  }

  async setSessionRelay(sessionId: string, enabled: boolean) {
    const res = await this.client.setSessionRelay({ sessionId, enabled })
    if (!res.session) throw new Error('the client returned no session')
    return res.session
  }

  async suggestFiles(sessionId: string, query: string, limit: number) {
    return (await this.client.suggestFiles({ sessionId, query, limit })).paths
  }

  async compact(sessionId: string, instructions = '') {
    await this.client.compact({ sessionId, instructions })
  }

  async getContextUsage(sessionId: string) {
    const res = await this.client.getContextUsage({ sessionId })
    if (!res.usage) throw new Error('the client returned no context usage')
    return contextUsageFromProto(res.usage)
  }

  async getUserPreferences(): Promise<UserPreferences> {
    const res = await this.client.getUserPreferences({})
    return { compactIdleSessions: res.preferences?.compactIdleSessions ?? true, prMonitorWakesPerHour: res.preferences?.prMonitorWakesPerHour || 30, subagentMaxDepth: res.preferences?.subagentMaxDepth || 2, subagentMaxRunning: res.preferences?.subagentMaxRunning || 8, workflowMaxAgents: res.preferences?.workflowMaxAgents || 30 }
  }

  async updateUserPreferences(patch: Partial<UserPreferences>): Promise<UserPreferences> {
    const res = await this.client.updateUserPreferences({ compactIdleSessions: patch.compactIdleSessions, prMonitorWakesPerHour: patch.prMonitorWakesPerHour, subagentMaxDepth: patch.subagentMaxDepth, subagentMaxRunning: patch.subagentMaxRunning, workflowMaxAgents: patch.workflowMaxAgents })
    return { compactIdleSessions: res.preferences?.compactIdleSessions ?? true, prMonitorWakesPerHour: res.preferences?.prMonitorWakesPerHour || 30, subagentMaxDepth: res.preferences?.subagentMaxDepth || 2, subagentMaxRunning: res.preferences?.subagentMaxRunning || 8, workflowMaxAgents: res.preferences?.workflowMaxAgents || 30 }
  }

  async listTasks(sessionId: string) {
    return (await this.client.listTasks({ sessionId })).tasks
  }

  async stopTask(sessionId: string, taskId: string) {
    await this.client.stopTask({ sessionId, taskId })
  }

  async stopSubagent(sessionId: string, agentId: string) {
    await this.client.stopSubagent({ sessionId, agentId })
  }

  async stopWorkflow(sessionId: string, workflowId: string) {
    await this.client.stopWorkflow({ sessionId, workflowId })
  }

  async subagentTranscript(sessionId: string, agentId: string) {
    return (await this.client.getSubagentTranscript({ sessionId, agentId })).items
  }

  async backgroundToolCall(sessionId: string, toolUseId: string, reason: 'user' | 'queued_message') {
    return (await this.client.backgroundToolCall({ sessionId, toolUseId, reason: backgroundReasonPb(reason) })).backgrounded
  }

  async runShellCommand(sessionId: string, uuid: string, command: string) {
    return (await this.client.runShellCommand({ sessionId, uuid, command })).commandId
  }

  async stopShellCommand(sessionId: string, commandId: string) {
    await this.client.stopShellCommand({ sessionId, commandId })
  }

  async listModels(): Promise<ModelInfo[]> {
    const init = await this.initialize()
    // An older client sends ids only.
    if (!init.models.length) return init.modelIds.map(id => ({ id, provider: '', contextWindow: 0, price: '' }))
    const fmt = (n: number) => `$${Number(n.toFixed(2))}`
    return init.models.map(m => ({
      id: m.id,
      provider: m.provider,
      displayName: m.displayName,
      contextWindow: Number(m.contextWindow),
      price: m.inputPrice === undefined || m.outputPrice === undefined ? '' : m.inputPrice === 0 && m.outputPrice === 0 ? 'Free' : `${fmt(m.inputPrice)} / ${fmt(m.outputPrice)}`,
      efforts: m.efforts.map(e => EFFORT_NAME[e]).filter((e): e is EffortName => !!e),
      thinking: m.thinking,
      ...(m.local ? { local: true, price: '' } : {})
    }))
  }

  private filesApi!: ConnectFilesApi
  files() {
    return this.filesApi
  }

  async sessionDiff(sessionId: string, opts: { ifNoneMatch?: string, base?: DiffBaseChoice } = {}) {
    return sessionDiffView(await this.client.getSessionDiff({ sessionId, ifNoneMatch: opts.ifNoneMatch ?? '', base: diffBaseMessage(opts.base) }))
  }

  async sessionFileDiff(sessionId: string, path: string, base?: DiffBaseChoice) {
    const r = await this.client.getSessionFileDiff({ sessionId, path, base: diffBaseMessage(base) })
    if (!r.file) throw new Error(`[not_found] ${path}`)
    return sessionFileDiffView(r.file)
  }

  async listDiffBases(sessionId: string) {
    return diffBasesView(await this.client.listDiffBases({ sessionId }))
  }

  async updateViewerPresence(sessionId: string, viewerId: string, focused: boolean) {
    await this.client.updateViewerPresence({ sessionId, viewerId, focused })
  }

  async readSentFile(sessionId: string, path: string) {
    const r = await this.client.readSentFile({ sessionId, path })
    return { bytes: r.content as Uint8Array<ArrayBuffer>, mime: r.mime, sha256: r.sha256 }
  }

  private speechApi!: ConnectSpeechApi
  speech() {
    return this.speechApi
  }

  private localModelsApi?: ConnectLocalModelsApi
  localModels() {
    return (this.localModelsApi ??= new ConnectLocalModelsApi(this.transport))
  }

  private acpApi!: ConnectAcpApi
  acp() {
    return this.acpApi
  }
}

/** Adds the client's bearer token to every request (unary and streaming). */
export function bearer(token: string): Interceptor {
  return next => (req) => {
    req.header.set('Authorization', `Bearer ${token}`)
    return next(req)
  }
}

// The store keeps the transport instance across hot updates, so a changed transport would keep running the
// old code (missing methods). Reload the page instead of hot-swapping this module.
if (import.meta.hot) import.meta.hot.accept(() => window.location.reload())
