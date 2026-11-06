// McpApi over AgentService's MCP RPCs and the McpService calls the client forwards to the server (OAuth, the audit
// log; packages/client/server/coreproxy.go), on the same connect transport (and agent token).

import { createClient, type Client, type Transport } from '@connectrpc/connect'
import { AgentService } from '~/gen/harness/agent/v1/agent_pb'
import { McpService } from '~/gen/harness/mcp/v1/mcp_pb'
import type { McpApi, McpAuditQuery, McpRule, McpSecretChanges, McpServerSource } from './types'

export class ConnectMcpApi implements McpApi {
  private agent: Client<typeof AgentService>
  private core: Client<typeof McpService>

  constructor(transport: Transport) {
    this.agent = createClient(AgentService, transport)
    this.core = createClient(McpService, transport)
  }

  list(folder = '') {
    return this.agent.listMcpServers({ folder })
  }

  inspect(serverId: string, folder = '') {
    return this.agent.inspectMcpServer({ serverId, folder })
  }

  async create(source: McpServerSource, server: Parameters<McpApi['create']>[1], secrets: McpSecretChanges) {
    const res = await this.agent.createMcpServer({ source, server, secrets })
    if (!res.entry) throw new Error('the client returned no server')
    return res.entry
  }

  async update(server: Parameters<McpApi['update']>[0], secrets: McpSecretChanges, baseVersion: bigint) {
    const res = await this.agent.updateMcpServer({ server, secrets, baseVersion })
    return { entry: res.entry, conflict: res.conflict }
  }

  async remove(serverId: string) {
    await this.agent.deleteMcpServer({ serverId })
  }

  async setApproval(serverId: string, folder: string, approved: boolean) {
    const res = await this.agent.setMcpServerApproval({ serverId, folder, approved })
    if (!res.entry) throw new Error('the client returned no server')
    return res.entry
  }

  async setTrust(path: string, trusted: boolean) {
    await this.agent.setWorkspaceTrust({ path, trusted })
  }

  async setToolRule(server: string, tool: string, rule: McpRule, deviceLocal: boolean) {
    await this.agent.setMcpToolRule({ server, tool, rule, deviceLocal })
  }

  async startOAuth(serverId: string) {
    const res = await this.core.startOAuth({ serverId })
    return { flowId: res.flowId, authorizationUrl: res.authorizationUrl }
  }

  async oauthFlow(flowId: string) {
    const res = await this.core.getOAuthFlow({ flowId })
    return { state: res.state, errorMessage: res.errorMessage }
  }

  async revokeAuth(serverId: string) {
    await this.core.revokeAuth({ serverId })
  }

  async audit(q: McpAuditQuery) {
    const res = await this.core.listAudit({ serverId: q.serverId ?? '', action: q.action ?? '', limit: q.limit ?? 50, cursor: q.cursor ?? '' })
    return { entries: res.entries, nextCursor: res.nextCursor }
  }

  async commands(sessionId: string) {
    return (await this.agent.listMcpCommands({ sessionId })).commands
  }

  async resources(sessionId: string, query: string, limit = 8) {
    return (await this.agent.listMcpResources({ sessionId, query, limit })).resources
  }
}
