// In-memory McpApi for the mock transport. Follows the client's rules closely enough to exercise the screen: stdio
// goes on the device, an org server is read-only for non-admins, project servers need a trusted folder and an approval,
// secrets are write-only.

import { create } from '@bufbuild/protobuf'
import {
  InspectMcpServerResponseSchema,
  ListMcpServersResponseSchema,
  McpCommandSchema,
  McpResourceMentionSchema,
  McpServerEntrySchema
} from '~/gen/harness/agent/v1/mcp_pb'
import { AuditEntrySchema, AuthState, ServerSchema, SecretInfoSchema, type Server } from '~/gen/harness/mcp/v1/mcp_pb'
import { ApprovalState, GetOAuthFlowResponse_State, McpServerSource, McpServerState, Transport, type McpApi, type McpAuditQuery, type McpRule, type McpSecretChanges, type McpServerEntry } from './types'

export class MockMcpApi implements McpApi {
  private seq = 100
  private entries: McpServerEntry[]
  private secrets = new Map<string, Set<string>>()
  private flows = new Map<string, string>()
  private trusted = false
  rules: Record<string, McpRule> = {}
  admin = true

  constructor() {
    const server = (init: Parameters<typeof create<typeof ServerSchema>>[1]) => create(ServerSchema, { enabled: true, canEdit: true, version: 1n, configHash: 'h', ...init })
    this.entries = [
      this.entry(server({ id: 'mcl_files', name: 'files', transport: Transport.STDIO, stdio: { command: 'npx', args: ['-y', 'files-server', '.'] } }), McpServerSource.DEVICE, 'This device'),
      this.entry(server({ id: 'mcp_tracker', name: 'tracker', transport: Transport.STREAMABLE_HTTP, remote: { url: 'https://tracker.example/mcp' }, auth: { state: AuthState.CONNECTED, account: 'you@example.com' } }), McpServerSource.ACCOUNT, 'You'),
      this.entry(server({ id: 'mcp_docs', name: 'docs', transport: Transport.SSE, remote: { url: 'https://docs.example/sse' }, auth: { state: AuthState.NEEDS_AUTH, message: 'Sign in to use it' } }), McpServerSource.ACCOUNT, 'You', McpServerState.NEEDS_AUTH, 'Sign in to use it'),
      this.entry(server({ id: 'mcp_company', name: 'company', transport: Transport.WS, canEdit: true, remote: { url: 'wss://company.example/mcp' }, deniedTools: ['delete*'] }), McpServerSource.ORG, 'Admin'),
      this.entry(server({ id: 'mcp_lint', name: 'lint', transport: Transport.STDIO, canEdit: false, stdio: { command: 'node', args: ['tools/lint-server.js'] }, projectKey: 'github.com/example/repo' }), McpServerSource.PROJECT, 'Repository file', McpServerState.NEEDS_APPROVAL, 'Needs your approval on this device; a changed config asks again.')
    ]
  }

  private entry(s: Server, source: McpServerSource, managedBy: string, state = McpServerState.READY, detail = ''): McpServerEntry {
    const stdio = s.transport === Transport.STDIO
    return create(McpServerEntrySchema, {
      server: s, source, managedBy, canEdit: s.canEdit, canRemove: s.canEdit, state, stateDetail: detail,
      approval: stdio ? (state === McpServerState.NEEDS_APPROVAL ? ApprovalState.PENDING : ApprovalState.APPROVED) : ApprovalState.UNSPECIFIED,
      launchSummary: stdio ? `${s.stdio?.command} ${(s.stdio?.args ?? []).join(' ')}`.trim() : s.remote?.url ?? ''
    })
  }

  async list(folder = '') {
    const project = (e: McpServerEntry) => e.source === McpServerSource.PROJECT
    const servers = this.entries.filter(e => !project(e) || folder).map((e) => {
      if (!project(e) || this.trusted) return e
      return create(McpServerEntrySchema, { ...e, state: McpServerState.NEEDS_TRUST, stateDetail: 'Trust this folder to use the servers of its .mcp.json.' })
    })
    return create(ListMcpServersResponseSchema, { servers, canManageOrg: this.admin, projectRoot: folder, projectTrusted: this.trusted, projectHasFile: !!folder })
  }

  async inspect(serverId: string) {
    const e = this.must(serverId)
    if (e.state !== McpServerState.READY) return create(InspectMcpServerResponseSchema, {})
    const full = (t: string) => `mcp__${e.server!.name}__${t}`
    return create(InspectMcpServerResponseSchema, {
      connected: true, serverName: `${e.server!.name}-server`, serverVersion: '1.2.0', instructionsChars: 120n,
      tools: ['search', 'read', 'delete'].filter(t => !(e.server!.deniedTools.some(d => d.startsWith(t)))).map(t => ({ name: t, description: `${t} things`, fullName: full(t), rule: this.rules[full(t)] ?? '' })),
      prompts: [{ name: 'summarize', description: 'Summarize a thing', arguments: [{ name: 'topic', required: true }] }],
      resources: [{ uri: 'res://readme', name: 'readme', description: 'The readme', mimeType: 'text/markdown' }, { uriTemplate: 'res://items/{id}', name: 'item' }]
    })
  }

  private must(id: string) {
    const e = this.entries.find(x => x.server?.id === id)
    if (!e) throw new Error('MCP server not found')
    return e
  }

  private validate(s: Server) {
    if (!/^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$/.test(s.name)) throw new Error('name must be 1-64 characters of [A-Za-z0-9_.-], starting with a letter or digit')
    if (s.transport === Transport.STDIO && !s.stdio?.command) throw new Error('a command is required')
    if (s.transport !== Transport.STDIO && !s.remote?.url) throw new Error('the URL is required')
  }

  private applySecrets(s: Server, ch: McpSecretChanges) {
    const names = this.secrets.get(s.id) ?? new Set<string>()
    for (const k of Object.keys(ch.set)) names.add(k)
    for (const k of ch.remove) names.delete(k)
    this.secrets.set(s.id, names)
    s.secrets = [...names].sort().map(name => create(SecretInfoSchema, { name, hasValue: true }))
  }

  async create(source: McpServerSource, init: Parameters<McpApi['create']>[1], secrets: McpSecretChanges) {
    if (source === McpServerSource.ORG && !this.admin) throw new Error('Only admins can manage org MCP servers')
    const s = create(ServerSchema, { ...init, canEdit: true, version: 1n, configHash: 'h' })
    this.validate(s)
    if (s.transport === Transport.STDIO && source === McpServerSource.ACCOUNT) throw new Error('a personal stdio server stays on this device: add it to this device instead')
    if (this.entries.some(e => e.server?.name === s.name && e.source === source)) throw new Error(`An MCP server named ${s.name} already exists`)
    s.id = `${source === McpServerSource.DEVICE ? 'mcl' : 'mcp'}_${++this.seq}`
    this.applySecrets(s, secrets)
    const e = this.entry(s, source, source === McpServerSource.DEVICE ? 'This device' : source === McpServerSource.ORG ? 'Admin' : 'You')
    this.entries.push(e)
    return e
  }

  async update(init: Parameters<McpApi['update']>[0], secrets: McpSecretChanges, baseVersion: bigint) {
    const s = create(ServerSchema, init)
    const cur = this.must(s.id)
    if (!cur.canEdit) throw new Error('Only admins can manage org MCP servers')
    if (cur.source !== McpServerSource.DEVICE && baseVersion !== cur.server!.version) return { conflict: cur }
    this.validate(s)
    s.version = cur.server!.version + 1n
    s.canEdit = true
    this.applySecrets(s, secrets)
    const e = this.entry(s, cur.source, cur.managedBy, s.enabled ? McpServerState.READY : McpServerState.DISABLED)
    this.entries = this.entries.map(x => (x === cur ? e : x))
    return { entry: e }
  }

  async remove(serverId: string) {
    const e = this.must(serverId)
    if (!e.canRemove) throw new Error('This server cannot be removed here')
    this.entries = this.entries.filter(x => x !== e)
  }

  async setApproval(serverId: string, _folder: string, approved: boolean) {
    const e = this.must(serverId)
    if (e.source === McpServerSource.PROJECT && !this.trusted) throw new Error('trust this folder first: the servers of a .mcp.json only run in a trusted folder')
    e.approval = approved ? ApprovalState.APPROVED : ApprovalState.DENIED
    e.state = approved ? McpServerState.READY : McpServerState.DENIED
    e.stateDetail = approved ? '' : 'Not allowed on this device.'
    return e
  }

  async setTrust(_folder: string, trusted: boolean) {
    this.trusted = trusted
  }

  async setToolRule(server: string, tool: string, rule: McpRule) {
    this.rules[tool ? `mcp__${server}__${tool}` : `mcp__${server}`] = rule
  }

  async startOAuth(serverId: string) {
    this.must(serverId)
    const flowId = `mfl_${++this.seq}`
    this.flows.set(flowId, serverId)
    return { flowId, authorizationUrl: `https://auth.example/authorize?flow=${flowId}` }
  }

  async oauthFlow(flowId: string) {
    const id = this.flows.get(flowId)
    if (!id) return { state: GetOAuthFlowResponse_State.EXPIRED, errorMessage: 'The sign-in expired' }
    const e = this.must(id)
    e.state = McpServerState.READY
    e.stateDetail = ''
    if (e.server) e.server.auth = { $typeName: 'harness.mcp.v1.AuthStatus', state: AuthState.CONNECTED, message: '', grantedScopes: [], account: 'you@example.com', expiresAt: '' }
    return { state: GetOAuthFlowResponse_State.COMPLETED, errorMessage: '' }
  }

  async revokeAuth(serverId: string) {
    const e = this.must(serverId)
    e.state = McpServerState.NEEDS_AUTH
    e.stateDetail = 'Sign in to use it'
    if (e.server) e.server.auth = { $typeName: 'harness.mcp.v1.AuthStatus', state: AuthState.NEEDS_AUTH, message: 'Sign in to use it', grantedScopes: [], account: '', expiresAt: '' }
  }

  async audit(q: McpAuditQuery) {
    if (!this.admin) throw new Error('Only admins can read the MCP audit log')
    const at = (m: number) => new Date(Date.now() - m * 60_000).toISOString()
    const all = [
      create(AuditEntrySchema, { id: 'mau_1', createdAt: at(2), userName: 'Ada Lovelace', userId: 'usr_ada', serverId: 'mcp_tracker', serverName: 'tracker', action: 'call_tool', tool: 'search', durationMs: 240n }),
      create(AuditEntrySchema, { id: 'mau_2', createdAt: at(9), userName: 'You', userId: 'usr_me', serverId: 'mcp_tracker', serverName: 'tracker', action: 'call_tool', tool: 'create_issue', isError: true, durationMs: 80n, errorMessage: 'Upstream said no' }),
      create(AuditEntrySchema, { id: 'mau_3', createdAt: at(60), userName: 'You', userId: 'usr_me', serverId: 'mcp_docs', serverName: 'docs', action: 'oauth_completed' })
    ]
    return { entries: all.filter(e => !q.action || e.action === q.action).filter(e => !q.serverId || e.serverId === q.serverId), nextCursor: '' }
  }

  async commands() {
    return [create(McpCommandSchema, { command: '/mcp__tracker__summarize', server: 'tracker', prompt: 'summarize', description: 'Summarize a thing', arguments: [{ name: 'topic', required: true }] })]
  }

  async resources(_sessionId: string, query: string) {
    const all = [create(McpResourceMentionSchema, { mention: '@tracker:res://readme', server: 'tracker', uri: 'res://readme', name: 'readme', description: 'The readme' })]
    return all.filter(r => !query || r.mention.includes(query) || r.name.includes(query))
  }
}
