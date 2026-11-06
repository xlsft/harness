// McpApi: what Settings > MCP and the composer need about MCP servers. The client keeps the logic (who manages what,
// approvals, the trust rule); the xlsft`s harness server keeps the registry, OAuth and the audit log. Two implementations:
// connect.ts (AgentService, plus the McpService calls the client forwards) and mock.ts (in memory).

import { tt } from '~/utils/i18n'
import { create, type MessageInitShape } from '@bufbuild/protobuf'
import type {
  InspectMcpServerResponse,
  ListMcpServersResponse,
  McpCommand,
  McpResourceMention,
  McpServerEntry
} from '~/gen/harness/agent/v1/mcp_pb'
import { McpServerSource, McpServerState } from '~/gen/harness/agent/v1/mcp_pb'
import { ApprovalState, GetOAuthFlowResponse_State, ServerSchema, Transport, type AuditEntry, type Server } from '~/gen/harness/mcp/v1/mcp_pb'

export { ApprovalState, GetOAuthFlowResponse_State, McpServerSource, McpServerState, Transport }
export type { AuditEntry, InspectMcpServerResponse, ListMcpServersResponse, McpCommand, McpResourceMention, McpServerEntry, Server }

export interface McpSecretChanges {
  set: Record<string, string>
  remove: string[]
}

export interface McpOAuthStart {
  flowId: string
  authorizationUrl: string
}

export interface McpOAuthFlow {
  state: GetOAuthFlowResponse_State
  errorMessage: string
}

export interface McpAuditQuery {
  serverId?: string
  action?: string
  limit?: number
  cursor?: string
}

export type McpRule = '' | 'allow' | 'ask' | 'deny'

export interface McpApi {
  list(folder?: string): Promise<ListMcpServersResponse>
  inspect(serverId: string, folder?: string): Promise<InspectMcpServerResponse>
  create(source: McpServerSource, server: MessageInitShape<typeof ServerSchema>, secrets: McpSecretChanges): Promise<McpServerEntry>
  // A conflict comes back as such: someone else changed the server, the form shows the current one.
  update(server: MessageInitShape<typeof ServerSchema>, secrets: McpSecretChanges, baseVersion: bigint): Promise<{ entry?: McpServerEntry, conflict?: McpServerEntry }>
  remove(serverId: string): Promise<void>
  setApproval(serverId: string, folder: string, approved: boolean): Promise<McpServerEntry>
  setTrust(folder: string, trusted: boolean): Promise<void>
  setToolRule(server: string, tool: string, rule: McpRule, deviceLocal: boolean): Promise<void>
  startOAuth(serverId: string): Promise<McpOAuthStart>
  oauthFlow(flowId: string): Promise<McpOAuthFlow>
  revokeAuth(serverId: string): Promise<void>
  audit(q: McpAuditQuery): Promise<{ entries: AuditEntry[], nextCursor: string }>
  // A session's prompts as slash commands, for a command menu to merge into its own list.
  commands(sessionId: string): Promise<McpCommand[]>
  // A session's resources, for @-mentions.
  resources(sessionId: string, query: string, limit?: number): Promise<McpResourceMention[]>
}

// The form's fields. Lists are one entry per line ("KEY=value" for maps), as people paste them.
export interface McpDraft {
  name: string
  transport: 'stdio' | 'http' | 'sse' | 'ws'
  enabled: boolean
  alwaysLoad: boolean
  command: string
  args: string
  env: string
  cwd: string
  url: string
  headers: string
  headersHelper: string
  requiresInteraction: boolean
  oauthClientId: string
  oauthScopes: string
  // Seconds; '' for the default.
  toolTimeout: string
  maxOutputChars: string
  allowedTools: string
  deniedTools: string
  // Secret values to set, "NAME=value" per line. Values are write-only: never filled in from the server.
  secrets: string
  // Stored secret names to remove.
  removeSecrets: string[]
}

export function emptyDraft(): McpDraft {
  return {
    name: '', transport: 'stdio', enabled: true, alwaysLoad: false, command: '', args: '', env: '', cwd: '', url: '', headers: '', headersHelper: '',
    requiresInteraction: false, oauthClientId: '', oauthScopes: '', toolTimeout: '', maxOutputChars: '', allowedTools: '', deniedTools: '', secrets: '', removeSecrets: []
  }
}

const TRANSPORTS: Record<McpDraft['transport'], Transport> = { stdio: Transport.STDIO, http: Transport.STREAMABLE_HTTP, sse: Transport.SSE, ws: Transport.WS }

export function transportName(t: Transport): McpDraft['transport'] {
  return t === Transport.SSE ? 'sse' : t === Transport.WS ? 'ws' : t === Transport.STREAMABLE_HTTP ? 'http' : 'stdio'
}

export const TRANSPORT_LABELS: Record<McpDraft['transport'], string> = { stdio: 'Local command (stdio)', http: 'Streamable HTTP', sse: 'SSE', ws: 'WebSocket' }

const lines = (s: string) => s.split('\n').map(l => l.trim()).filter(Boolean)
const words = (s: string) => s.split(/[\s,]+/).map(w => w.trim()).filter(Boolean)

// "KEY=value" lines; `sep` is ':' as well for headers.
export function parseMap(text: string, what: string, headers = false): Record<string, string> {
  const out: Record<string, string> = {}
  for (const line of lines(text)) {
    const m = headers ? /^([^:=\s]+)\s*[:=]\s*(.*)$/.exec(line) : /^([^=\s]+)=(.*)$/.exec(line)
    if (!m) throw new Error(`${what}: "${line}" is not ${headers ? 'Name: value' : 'NAME=value'}`)
    out[m[1]!] = m[2]!
  }
  return out
}

const mapText = (m: Record<string, string> | undefined, sep = '=') => Object.entries(m ?? {}).map(([k, v]) => `${k}${sep}${v}`).join('\n')

// The form's view of a server. Secret values are not in it: they are write-only.
export function draftOf(s: Server): McpDraft {
  return {
    ...emptyDraft(),
    name: s.name,
    transport: transportName(s.transport),
    enabled: s.enabled,
    alwaysLoad: s.alwaysLoad,
    command: s.stdio?.command ?? '',
    args: (s.stdio?.args ?? []).join('\n'),
    env: mapText(s.stdio?.env),
    cwd: s.stdio?.cwd ?? '',
    url: s.remote?.url ?? '',
    headers: mapText(s.remote?.headers, ': '),
    headersHelper: s.remote?.headersHelper ?? '',
    requiresInteraction: s.remote?.requiresUserInteraction ?? false,
    oauthClientId: s.remote?.oauth?.clientId ?? '',
    oauthScopes: (s.remote?.oauth?.scopes ?? []).join(' '),
    toolTimeout: s.toolTimeoutMs ? String(Number(s.toolTimeoutMs) / 1000) : '',
    maxOutputChars: s.maxOutputChars ? String(s.maxOutputChars) : '',
    allowedTools: s.allowedTools.join(' '),
    deniedTools: s.deniedTools.join(' ')
  }
}

function positive(text: string, what: string): number {
  if (!text.trim()) return 0
  const n = Number(text)
  if (!Number.isFinite(n) || n < 0) throw new Error(`${what} must be a positive number`)
  return n
}

// The server message of a form. `base` keeps what the form does not show (id, scope, overrides, target roles).
export function serverOf(d: McpDraft, base?: Server): MessageInitShape<typeof ServerSchema> {
  const stdio = d.transport === 'stdio'
  const { $typeName: _type, $unknown: _unknown, ...kept } = (base ?? {}) as Partial<Server>
  return {
    ...kept,
    name: d.name.trim(),
    transport: TRANSPORTS[d.transport],
    enabled: d.enabled,
    alwaysLoad: d.alwaysLoad,
    stdio: stdio ? { command: d.command.trim(), args: lines(d.args), env: parseMap(d.env, 'Environment'), cwd: d.cwd.trim() } : undefined,
    remote: stdio
      ? undefined
      : {
          url: d.url.trim(),
          headers: parseMap(d.headers, 'Headers', true),
          headersHelper: d.headersHelper.trim(),
          requiresUserInteraction: d.requiresInteraction,
          oauth: d.oauthClientId.trim() || d.oauthScopes.trim() ? { clientId: d.oauthClientId.trim(), scopes: words(d.oauthScopes), authorizationServer: base?.remote?.oauth?.authorizationServer ?? '' } : undefined
        },
    toolTimeoutMs: BigInt(Math.round(positive(d.toolTimeout, 'The time limit') * 1000)),
    maxOutputChars: Math.round(positive(d.maxOutputChars, 'The output limit')),
    allowedTools: words(d.allowedTools),
    deniedTools: words(d.deniedTools)
  }
}

export function secretChangesOf(d: McpDraft): McpSecretChanges {
  return { set: parseMap(d.secrets, 'Secrets'), remove: d.removeSecrets }
}

// The groups of the list, in order, with what they mean for syncing.
export const SOURCE_GROUPS: { source: McpServerSource, title: string, note: string }[] = [
  { source: McpServerSource.DEVICE, title: tt('On this device'), note: 'Kept on this computer and never sent to the xlsft`s harness server. Other devices do not see them.' },
  { source: McpServerSource.ACCOUNT, title: tt('Your account'), note: 'Registered on the xlsft`s harness server: they follow you to every device, and only you see them.' },
  { source: McpServerSource.ORG, title: tt('From your admin'), note: 'Managed by an admin for everyone or for some roles. Sign-ins are still your own.' },
  { source: McpServerSource.PROJECT, title: tt('From this repository'), note: 'Listed in the repository\'s .mcp.json. They only load in a trusted folder and after you approve each one here.' }
]

export interface StateView {
  label: string
  // A theme color token: ok, bad, muted.
  tone: 'ok' | 'bad' | 'muted'
}

export function stateView(e: McpServerEntry): StateView {
  switch (e.state) {
    case McpServerState.READY: return { label: tt('Ready'), tone: 'ok' }
    case McpServerState.DISABLED: return { label: tt('Off'), tone: 'muted' }
    case McpServerState.NEEDS_TRUST: return { label: tt('Folder not trusted'), tone: 'muted' }
    case McpServerState.NEEDS_APPROVAL: return { label: tt('Needs your approval'), tone: 'bad' }
    case McpServerState.DENIED: return { label: tt('Not allowed here'), tone: 'muted' }
    case McpServerState.NEEDS_AUTH: return { label: tt('Needs sign-in'), tone: 'bad' }
    case McpServerState.UNAVAILABLE: return { label: tt('Unavailable'), tone: 'bad' }
    default: return { label: tt('Unknown'), tone: 'muted' }
  }
}

// What the row offers.
export function canSignIn(e: McpServerEntry): boolean {
  const remote = e.source !== McpServerSource.DEVICE && e.server?.transport !== Transport.STDIO
  return remote && !!e.server?.id && e.state !== McpServerState.DISABLED && e.state !== McpServerState.NEEDS_TRUST
}

export function needsDecision(e: McpServerEntry): boolean {
  return e.state === McpServerState.NEEDS_APPROVAL || e.state === McpServerState.DENIED
}

// Where a new server can go. A personal stdio server always stays on the device; a remote one defaults to the account
// (the server keeps its sign-in), unless it is only reachable from here.
export function sourcesFor(transport: McpDraft['transport'], canManageOrg: boolean): { value: McpServerSource, label: string }[] {
  const out: { value: McpServerSource, label: string }[] = []
  if (transport !== 'stdio' && transport !== 'ws') out.push({ value: McpServerSource.ACCOUNT, label: tt('My account (every device)') })
  if (transport === 'ws') out.push({ value: McpServerSource.ACCOUNT, label: tt('My account (every device)') })
  if (transport !== 'ws') out.push({ value: McpServerSource.DEVICE, label: tt('This device only') })
  if (canManageOrg) out.push({ value: McpServerSource.ORG, label: tt('Everyone (organization)') })
  return out
}

export const AUDIT_ACTIONS: Record<string, string> = {
  call_tool: 'Tool call',
  stdio_launch: 'Started a local server',
  oauth_completed: 'Signed in',
  oauth_revoked: 'Signed out'
}

export function serverFrom(init: MessageInitShape<typeof ServerSchema>): Server {
  return create(ServerSchema, init)
}
