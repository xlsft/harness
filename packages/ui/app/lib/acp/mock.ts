// MockAcpApi: a few registry agents with install and sign-in states, for the mock transport.
//
// - claude-acp: installed (npx), needs a sign-in (an agent method and a terminal login);
// - codex-acp: installed (npx), signed in, reports subscription limits (5h window at 42%, weekly at 18%);
// - gemini: not installed (npx); goose: not installed (binary, downloads with progress);
// - fast-agent: unavailable (uv is not on PATH).
// Installs stream progress on a timer; an agent sign-in succeeds at once, a terminal login returns the command
// and succeeds on verify.

import { create, type MessageInitShape } from '@bufbuild/protobuf'
import {
  AcpAgentLimitsSchema,
  AcpAgentSchema,
  AcpAuthMethod_Kind,
  AcpAuthState,
  AcpDistribution,
  AcpSessionStateSchema,
  AcpTerminalCommandSchema,
  InstallAcpAgentResponseSchema,
  InstallAcpAgentResponse_Phase,
  type AcpAgent,
  type AcpSessionState
} from '~/gen/harness/agent/v1/acp_pb'
import type { AcpApi, AcpAuthRequest, AcpAuthResult, AcpInstallProgress } from './types'

const ICON = (letter: string) =>
  `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16"><rect width="16" height="16" rx="4" fill="currentColor" opacity=".15"/><text x="8" y="11.5" font-size="9" text-anchor="middle" fill="currentColor">${letter}</text></svg>`

const CDN = 'https://cdn.agentclientprotocol.com/registry/v1/latest'

export interface MockAcpHooks {
  now?: () => number
  // Delay between progress steps (ms); 0 is synchronous (tests).
  tick?: number
  // A session's agent signed in: the mock transport lets it continue.
  onSessionAuthenticated?: (sessionId: string) => void
  // Switches a mock session's mode.
  setSessionMode?: (sessionId: string, modeId: string) => void
}

function limits(now: number) {
  return create(AcpAgentLimitsSchema, {
    planName: 'plus',
    updatedAt: new Date(now - 4 * 60_000).toISOString(),
    windows: [
      { label: '5h', usedPercent: 42, windowMinutes: 300n, resetsAt: new Date(now + 2.5 * 3_600_000).toISOString() },
      { label: 'weekly', usedPercent: 18, windowMinutes: 10080n, resetsAt: new Date(now + 4 * 86_400_000).toISOString() }
    ]
  })
}

export class MockAcpApi implements AcpApi {
  private agents = new Map<string, AcpAgent>()
  private now: () => number
  private tick: number

  constructor(private hooks: MockAcpHooks = {}) {
    this.now = hooks.now ?? (() => Date.now())
    this.tick = hooks.tick ?? 120
    const now = this.now()
    const agent = (a: MessageInitShape<typeof AcpAgentSchema> & { id: string, name: string }) => create(AcpAgentSchema, {
      engine: `acp:${a.id}`, iconUrl: `${CDN}/${a.id}.svg`, iconSvg: ICON(a.name[0]!), available: true, authState: AcpAuthState.UNKNOWN, ...a
    })
    for (const a of [
      agent({
        id: 'claude-acp', name: 'Claude Agent', description: 'ACP wrapper for Anthropic\'s Claude', latestVersion: '0.100.0',
        repository: 'https://github.com/agentclientprotocol/claude-agent-acp', license: 'proprietary', authors: ['Anthropic', 'Zed Industries'],
        distribution: AcpDistribution.NPX, runtime: 'npx', installed: true, installedVersion: '0.99.2', updateAvailable: true,
        authState: AcpAuthState.REQUIRED,
        authMethods: [
          { id: 'claude-login', name: 'Log in with Claude', description: 'Opens claude.ai in the browser', kind: AcpAuthMethod_Kind.AGENT },
          { id: 'claude-terminal', name: 'Log in in a terminal', description: 'Runs the interactive login', kind: AcpAuthMethod_Kind.TERMINAL }
        ]
      }),
      agent({
        id: 'codex-acp', name: 'Codex', description: 'ACP adapter for OpenAI\'s coding assistant', latestVersion: '2.5.0',
        repository: 'https://github.com/agentclientprotocol/codex-acp', license: 'Apache-2.0', authors: ['OpenAI', 'Zed Industries'],
        distribution: AcpDistribution.NPX, runtime: 'npx', installed: true, installedVersion: '2.5.0', authState: AcpAuthState.AUTHENTICATED,
        authMethods: [
          { id: 'chatgpt', name: 'Sign in with ChatGPT', kind: AcpAuthMethod_Kind.AGENT },
          { id: 'openai-api-key', name: 'Use an API key', kind: AcpAuthMethod_Kind.ENV_VAR, envVars: ['OPENAI_API_KEY'], link: 'https://platform.openai.com/api-keys' }
        ],
        limits: limits(now)
      }),
      agent({
        id: 'gemini', name: 'Gemini CLI', description: 'Google\'s official CLI for Gemini', latestVersion: '0.66.0',
        repository: 'https://github.com/google-gemini/gemini-cli', website: 'https://geminicli.com', license: 'Apache-2.0', authors: ['Google'],
        distribution: AcpDistribution.NPX, runtime: 'npx'
      }),
      agent({
        id: 'goose', name: 'goose', description: 'A local, extensible, open source AI agent that automates engineering tasks', latestVersion: '1.57.0',
        repository: 'https://github.com/block/goose', license: 'Apache-2.0', authors: ['Block'], distribution: AcpDistribution.BINARY
      }),
      agent({
        id: 'fast-agent', name: 'fast-agent', description: 'Code and build agents with comprehensive multi-provider support', latestVersion: '0.10.1',
        repository: 'https://github.com/evalstate/fast-agent', license: 'Apache 2.0', distribution: AcpDistribution.UVX, runtime: 'uvx',
        available: false, unavailableReason: 'uv (uvx) is not on PATH'
      })
    ]) this.agents.set(a.id, a)
  }

  agent(id: string) {
    return this.agents.get(id)
  }

  // The ACP state of a new session on the agent (SessionInfo.acp).
  sessionState(agentId: string): AcpSessionState {
    const a = this.agents.get(agentId)
    if (!a) throw new Error(`unknown ACP agent ${agentId}`)
    const claude = agentId === 'claude-acp'
    return create(AcpSessionStateSchema, {
      agentId,
      agentName: a.name,
      agentVersion: a.installedVersion,
      modes: claude
        ? [{ id: 'default', name: 'Default' }, { id: 'acceptEdits', name: 'Accept Edits' }, { id: 'plan', name: 'Plan Mode' }, { id: 'bypassPermissions', name: 'Bypass Permissions' }]
        : [{ id: 'read-only', name: 'Read Only' }, { id: 'auto', name: 'Auto' }, { id: 'full-access', name: 'Full Access' }],
      currentModeId: claude ? 'default' : 'auto',
      commands: claude
        ? [{ name: 'review', description: 'Review the current changes' }, { name: 'init', description: 'Write a CLAUDE.md for this repository' }]
        : [{ name: 'review', description: 'Review my current changes and find issues' }, { name: 'compact', description: 'Summarize the conversation' }],
      needsAuth: a.authState === AcpAuthState.REQUIRED,
      authMethods: a.authMethods,
      contextWindowTokens: claude ? 200_000n : 272_000n,
      models: claude ? [{ id: 'default', name: 'Default (recommended)' }, { id: 'opus', name: 'Opus' }, { id: 'sonnet', name: 'Sonnet' }] : [],
      currentModelId: claude ? 'default' : '',
      limits: a.limits
    })
  }

  async listAgents() {
    return { agents: [...this.agents.values()], registryFetchedAt: new Date(this.now() - 20 * 60_000).toISOString(), registryError: '', runtimes: ['npx'] }
  }

  private get(id: string) {
    const a = this.agents.get(id)
    if (!a) throw new Error(`[not_found] the ACP registry has no agent "${id}"`)
    return a
  }

  private progress(id: string, onProgress?: (p: AcpInstallProgress) => void): Promise<AcpAgent> {
    const a = this.get(id)
    if (!a.available) return Promise.reject(new Error(`[failed_precondition] the agent cannot run on this machine: ${a.unavailableReason}`))
    const total = a.distribution === AcpDistribution.BINARY ? 48_000_000 : 0
    const steps: AcpInstallProgress[] = [create(InstallAcpAgentResponseSchema, { phase: InstallAcpAgentResponse_Phase.RESOLVING, message: `Resolving ${a.name}` })]
    if (total) {
      for (let i = 1; i <= 4; i++) steps.push(create(InstallAcpAgentResponseSchema, { phase: InstallAcpAgentResponse_Phase.DOWNLOADING, bytesDone: BigInt(total * i / 4), bytesTotal: BigInt(total), message: 'Downloading' }))
      steps.push(create(InstallAcpAgentResponseSchema, { phase: InstallAcpAgentResponse_Phase.VERIFYING, message: 'Verifying' }))
      steps.push(create(InstallAcpAgentResponseSchema, { phase: InstallAcpAgentResponse_Phase.EXTRACTING, message: 'Extracting' }))
    }
    return new Promise((resolve) => {
      let i = 0
      const step = (): void => {
        if (i < steps.length) {
          const p = steps[i++]!
          onProgress?.(p)
          if (this.tick) setTimeout(step, this.tick)
          else step()
          return
        }
        const next = create(AcpAgentSchema, { ...a, installed: true, installedVersion: a.latestVersion, updateAvailable: false })
        this.agents.set(id, next)
        onProgress?.(create(InstallAcpAgentResponseSchema, { phase: InstallAcpAgentResponse_Phase.DONE, done: true, message: `${a.name} ${a.latestVersion} installed`, agent: next }))
        resolve(next)
      }
      step()
    })
  }

  installAgent(agentId: string, onProgress?: (p: AcpInstallProgress) => void) {
    return this.progress(agentId, onProgress)
  }

  async updateAgent(agentId: string, onProgress?: (p: AcpInstallProgress) => void) {
    const a = this.get(agentId)
    if (!a.installed) throw new Error(`[failed_precondition] ACP agent ${agentId} is not installed`)
    if (!a.updateAvailable) {
      const done = create(InstallAcpAgentResponseSchema, { phase: InstallAcpAgentResponse_Phase.DONE, done: true, message: `${a.name} is up to date`, agent: a })
      onProgress?.(done)
      return a
    }
    return this.progress(agentId, onProgress)
  }

  async uninstallAgent(agentId: string) {
    const a = this.get(agentId)
    if (!a.installed) throw new Error(`[not_found] ACP agent ${agentId} is not installed`)
    const next = create(AcpAgentSchema, { ...a, installed: false, installedVersion: '', updateAvailable: false })
    this.agents.set(agentId, next)
    return next
  }

  async authenticate(req: AcpAuthRequest): Promise<AcpAuthResult> {
    const a = this.get(req.agentId)
    const signedIn = () => {
      const next = create(AcpAgentSchema, { ...a, authState: AcpAuthState.AUTHENTICATED })
      this.agents.set(a.id, next)
      if (req.sessionId) this.hooks.onSessionAuthenticated?.(req.sessionId)
      return { authenticated: true, envVars: [], agent: next }
    }
    if (req.verify) return a.authState === AcpAuthState.REQUIRED && !this.terminalDone.has(a.id) ? { authenticated: false, envVars: [], agent: a } : signedIn()
    const method = a.authMethods.find(m => m.id === req.methodId)
    if (!method) throw new Error(`[invalid_argument] ${a.id} has no auth method "${req.methodId ?? ''}"`)
    switch (method.kind) {
      case AcpAuthMethod_Kind.ENV_VAR:
        return { authenticated: false, envVars: method.envVars, agent: a }
      case AcpAuthMethod_Kind.TERMINAL: {
        // The login "runs" in the user's terminal; verify succeeds afterwards.
        this.terminalDone.add(a.id)
        const command = '/Users/xlsft/.npm/_npx/bin/npx'
        const args = ['--yes', `@agentclientprotocol/claude-agent-acp@${a.installedVersion}`, '--cli', 'login']
        return {
          authenticated: false,
          envVars: [],
          agent: a,
          terminal: create(AcpTerminalCommandSchema, { command, args, cwd: '/Users/xlsft', shellLine: [command, ...args].join(' ') })
        }
      }
      default:
        return signedIn()
    }
  }

  private terminalDone = new Set<string>()

  async setMode(sessionId: string, modeId: string) {
    if (!this.hooks.setSessionMode) throw new Error('[failed_precondition] no session')
    this.hooks.setSessionMode(sessionId, modeId)
  }
}
