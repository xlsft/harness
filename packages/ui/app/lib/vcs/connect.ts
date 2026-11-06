// VcsApi over AgentService and the VcsService calls the client forwards to the server
// (packages/client/server/coreproxy.go), on the same connect transport (and agent token).

import { createClient, type Client, type Transport } from '@connectrpc/connect'
import { AgentService } from '~/gen/harness/agent/v1/agent_pb'
import { GetUserAuthStatusResponse_State, ProviderType, VcsService, type Connection } from '~/gen/harness/vcs/v1/vcs_pb'
import type { AuthStatus, CodeHostKind, ConnectionView, GitLabConnectionInput, UserAuthState, VcsApi } from './types'

const kindOf = (t: ProviderType): CodeHostKind => t === ProviderType.GITLAB ? 'gitlab' : 'github'

export function authState(s: GetUserAuthStatusResponse_State): UserAuthState {
  switch (s) {
    case GetUserAuthStatusResponse_State.PENDING: return 'pending'
    case GetUserAuthStatusResponse_State.CONNECTED: return 'connected'
    case GetUserAuthStatusResponse_State.ERROR: return 'error'
    default: return 'not_connected'
  }
}

function connectionView(c: Connection): ConnectionView {
  return { id: c.id, kind: kindOf(c.type), name: c.displayName, baseUrl: c.baseUrl, healthy: c.healthy, lastError: c.lastError, installUrl: c.installUrl, webhooksOk: c.webhooksOk }
}

export class ConnectVcsApi implements VcsApi {
  private agent: Client<typeof AgentService>
  private core: Client<typeof VcsService>

  constructor(transport: Transport) {
    this.agent = createClient(AgentService, transport)
    this.core = createClient(VcsService, transport)
  }

  async clis() {
    const res = await this.agent.getVcsStatus({})
    return res.clis.map(c => ({ name: c.name, installed: c.installed, version: c.version, hosts: c.hosts.map(h => ({ host: h.host, login: h.login })), loginCommand: c.loginCommand, problem: c.problem }))
  }

  async userConnections() {
    const res = await this.core.listUserConnections({})
    return res.connections.map(c => ({ id: c.connectionId, kind: kindOf(c.type), name: c.displayName, baseUrl: c.baseUrl, host: c.host, installUrl: c.installUrl, state: authState(c.state), login: c.login, canWrite: c.canWrite }))
  }

  async startAuth(connectionId: string) {
    const res = await this.core.startUserAuth({ connectionId })
    return { authorizationUrl: res.authorizationUrl, flowId: res.flowId }
  }

  async authStatus(connectionId: string, flowId: string): Promise<AuthStatus> {
    const res = await this.core.getUserAuthStatus({ connectionId, flowId })
    return { state: authState(res.state), login: res.login, error: res.error }
  }

  async revokeAuth(connectionId: string) {
    await this.core.revokeUserAuth({ connectionId })
  }

  async connections() {
    return (await this.core.listConnections({})).connections.map(connectionView)
  }

  async gitHubManifest(input: { baseUrl: string, organization: string, appName: string }) {
    const res = await this.core.getGitHubAppManifest(input)
    return { postUrl: res.postUrl, manifest: res.manifestJson }
  }

  async putGitLab(input: GitLabConnectionInput) {
    const res = await this.core.putConnection({
      type: ProviderType.GITLAB,
      displayName: input.name,
      baseUrl: input.baseUrl,
      clientId: input.clientId,
      clientSecret: input.clientSecret,
      webhookSecret: input.webhookSecret,
      accessToken: input.accessToken,
      allowPrivateNetwork: input.allowPrivateNetwork
    })
    if (!res.connection) throw new Error('the server returned no connection')
    return connectionView(res.connection)
  }

  async deleteConnection(id: string) {
    await this.core.deleteConnection({ id })
  }
}
