// AcpApi: ACP agents (Agent Client Protocol) as session engines (proto/harness/agent/v1/acp.proto).
// A session can run on an external agent CLI (Claude Code via claude-acp, Codex, Gemini CLI, goose, ...) that uses
// the user's own subscription: create it with NewSessionOptions.engine = AcpAgent.engine ("acp:<id>"). Such a
// session lists what it cannot do in SessionInfo.unsupportedFeatures and carries the agent's modes, commands,
// sign-in state and subscription limits in SessionInfo.acp.
//
// Everything here is local to the client's machine (installs, sign-in, limits) and never synced. Two
// implementations: connect.ts (a real client) and mock.ts (scripted agents, for the mock transport).

import type {
  AcpAgent,
  AcpTerminalCommand,
  InstallAcpAgentResponse
} from '~/gen/harness/agent/v1/acp_pb'
import type { TerminalInfo } from '~/gen/harness/agent/v1/terminal_pb'

export type { AcpAgent, AcpAgentLimits, AcpAuthMethod, AcpSessionState, AcpTerminalCommand, InstallAcpAgentResponse } from '~/gen/harness/agent/v1/acp_pb'
export { AcpAuthMethod_Kind, AcpAuthState, AcpDistribution, InstallAcpAgentResponse_Phase, SessionFeature } from '~/gen/harness/agent/v1/acp_pb'

export interface AcpAgentList {
  agents: AcpAgent[]
  // RFC 3339; empty when the registry was never fetched.
  registryFetchedAt: string
  // The registry could not be fetched: the agents come from the client's cache.
  registryError: string
  // Runtimes found on the client's PATH ("npx", "uvx").
  runtimes: string[]
}

// Install / update progress; the last one has done = true and the agent's new state.
export type AcpInstallProgress = InstallAcpAgentResponse

export interface AcpAuthRequest {
  agentId: string
  // An id of AcpAgent.authMethods (or SessionInfo.acp.authMethods); omit with verify.
  methodId?: string
  // A session waiting for sign-in: it continues once signed in.
  sessionId?: string
  // The terminal login finished (or credentials were set): check again.
  verify?: boolean
  // Terminal methods: also start the login command in a client terminal.
  openTerminal?: boolean
}

export interface AcpAuthResult {
  authenticated: boolean
  // Terminal methods: run this (or attach terminalInfo with terminalTicket), then call again with verify.
  terminal?: AcpTerminalCommand
  terminalInfo?: TerminalInfo
  terminalTicket?: string
  // Env-var methods: the variables to set in the session's environment, then verify.
  envVars: string[]
  agent?: AcpAgent
}

export interface AcpApi {
  // The registry's agents merged with the client's installs, runtimes, sign-in and limits.
  listAgents(opts?: { refresh?: boolean }): Promise<AcpAgentList>
  // Install / update stream progress to onProgress and resolve with the agent once done.
  installAgent(agentId: string, onProgress?: (p: AcpInstallProgress) => void): Promise<AcpAgent>
  updateAgent(agentId: string, onProgress?: (p: AcpInstallProgress) => void): Promise<AcpAgent>
  uninstallAgent(agentId: string): Promise<AcpAgent>
  authenticate(req: AcpAuthRequest): Promise<AcpAuthResult>
  // Switches an ACP session to one of the agent's own modes (SessionInfo.acp.modes).
  setMode(sessionId: string, modeId: string): Promise<void>
}

// The engine id of an ACP agent, and the agent id of an engine ("" for the harness engine).
export const acpEngine = (agentId: string) => `acp:${agentId}`
export const acpAgentOf = (engine: string | undefined) => (engine?.startsWith('acp:') ? engine.slice(4) : '')
