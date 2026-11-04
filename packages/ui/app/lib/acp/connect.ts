// AcpApi over AgentService's ACP RPCs, on the same connect transport (and agent token) as the rest of AgentService.

import { createClient, type Client, type Transport } from '@connectrpc/connect'
import { AgentService } from '~/gen/harness/agent/v1/agent_pb'
import type { AcpAgent } from '~/gen/harness/agent/v1/acp_pb'
import type { AcpApi, AcpAuthRequest, AcpAuthResult, AcpInstallProgress } from './types'

export class ConnectAcpApi implements AcpApi {
  private client: Client<typeof AgentService>

  constructor(transport: Transport) {
    this.client = createClient(AgentService, transport)
  }

  async listAgents(opts: { refresh?: boolean, cachedOnly?: boolean } = {}) {
    const res = await this.client.listAcpAgents({ refresh: opts.refresh ?? false, cachedOnly: opts.cachedOnly ?? false })
    return { agents: res.agents, registryFetchedAt: res.registryFetchedAt, registryError: res.registryError, runtimes: res.runtimes, sessionSwitchRefusal: res.sessionSwitchRefusal }
  }

  async installAgent(agentId: string, onProgress?: (p: AcpInstallProgress) => void) {
    return finish(this.client.installAcpAgent({ agentId }), p => p, onProgress)
  }

  async updateAgent(agentId: string, onProgress?: (p: AcpInstallProgress) => void) {
    return finish(this.client.updateAcpAgent({ agentId }), r => r.progress, onProgress)
  }

  async uninstallAgent(agentId: string) {
    const res = await this.client.uninstallAcpAgent({ agentId })
    if (!res.agent) throw new Error('the client returned no agent')
    return res.agent
  }

  async authenticate(req: AcpAuthRequest): Promise<AcpAuthResult> {
    const res = await this.client.authenticateAcpAgent({
      agentId: req.agentId,
      methodId: req.methodId ?? '',
      sessionId: req.sessionId ?? '',
      verify: req.verify ?? false,
      openTerminal: req.openTerminal ?? false
    })
    return {
      authenticated: res.authenticated,
      terminal: res.terminal,
      terminalInfo: res.terminalInfo,
      terminalTicket: res.terminalTicket || undefined,
      envVars: res.envVars,
      agent: res.agent
    }
  }

  async setMode(sessionId: string, modeId: string) {
    await this.client.setAcpMode({ sessionId, modeId })
  }
}

// Drains a progress stream; resolves with the agent of the final message.
async function finish<T>(stream: AsyncIterable<T>, pick: (m: T) => AcpInstallProgress | undefined, onProgress?: (p: AcpInstallProgress) => void): Promise<AcpAgent> {
  let agent: AcpAgent | undefined
  for await (const m of stream) {
    const p = pick(m)
    if (!p) continue
    onProgress?.(p)
    if (p.done) agent = p.agent
  }
  if (!agent) throw new Error('the client ended the install without a result')
  return agent
}
