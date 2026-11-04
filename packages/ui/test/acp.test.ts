import { describe, expect, it } from 'vitest'
import type { SessionEvent } from '~/gen/harness/agent/v1/agent_pb'
import { RespondToPermissionRequest_Decision, SendUserMessageRequest_Delivery, PermissionMode, RewindRequest_Mode } from '~/gen/harness/agent/v1/agent_pb'
import { AcpAuthMethod_Kind, AcpAuthState, AcpModelsStatus, InstallAcpAgentResponse_Phase, SessionFeature, acpAgentOf, acpEngine } from '~/lib/acp/types'
import { firstPickable, fitAcpChoice, modelKey, modelSections, newSessionChoice, type ModelScope } from '~/lib/acp/modelChoices'
import { MockTransport } from '~/lib/transport/mock'
import type { ModelInfo } from '~/lib/transport/types'

const CWD = '/Users/xlsft/Projects/xlsft/harness'

function collect(t: MockTransport, id: string) {
  const events: SessionEvent[] = []
  t.subscribe(id, 0, { onEvent: e => events.push(e) })
  return events
}

const flush = () => new Promise(r => setTimeout(r, 0))

describe('ACP agents in the mock transport', () => {
  it('lists registry agents with install, runtime, sign-in and limits', async () => {
    const t = new MockTransport({ tick: 0 })
    const { agents, runtimes } = await t.acp().listAgents()
    const by = Object.fromEntries(agents.map(a => [a.id, a]))
    expect(Object.keys(by)).toEqual(expect.arrayContaining(['claude-acp', 'codex-acp', 'gemini', 'goose', 'fast-agent']))
    expect(runtimes).toEqual(['npx'])
    expect(by['claude-acp']!.authState).toBe(AcpAuthState.REQUIRED)
    expect(by['claude-acp']!.updateAvailable).toBe(true)
    expect(by['fast-agent']!.available).toBe(false)
    const limits = by['codex-acp']!.limits!
    expect(limits.windows.map(w => [w.label, w.usedPercent])).toEqual([['5h', 42], ['weekly', 18]])
    expect(acpAgentOf(by['codex-acp']!.engine)).toBe('codex-acp')
  })

  it('installs a binary agent with progress, updates and uninstalls', async () => {
    const t = new MockTransport({ tick: 0 })
    const phases: InstallAcpAgentResponse_Phase[] = []
    const goose = await t.acp().installAgent('goose', p => phases.push(p.phase))
    expect(goose.installed).toBe(true)
    expect(phases).toContain(InstallAcpAgentResponse_Phase.DOWNLOADING)
    expect(phases.at(-1)).toBe(InstallAcpAgentResponse_Phase.DONE)
    await expect(t.acp().installAgent('fast-agent')).rejects.toThrow(/uvx/)
    const claude = await t.acp().updateAgent('claude-acp')
    expect(claude.installedVersion).toBe(claude.latestVersion)
    expect((await t.acp().uninstallAgent('goose')).installed).toBe(false)
  })

  it('runs a session on an agent: streamed reply, tool call behind a permission, no harness-only features', async () => {
    const t = new MockTransport({ tick: 0 })
    const info = await t.createSession({ cwd: CWD, engine: acpEngine('codex-acp') })
    expect(info.engine).toBe('acp:codex-acp')
    expect(info.unsupportedFeatures).toContain(SessionFeature.REWIND)
    expect(info.acp?.limits?.windows).toHaveLength(2)
    const events = collect(t, info.id)
    await flush()
    await t.sendUserMessage(info.id, 'u1', 'make the backoff exponential', SendUserMessageRequest_Delivery.QUEUE)
    const perm = events.find(e => e.event.case === 'permissionRequest')
    expect(perm?.event.case === 'permissionRequest' && perm.event.value.toolName).toBe('Edit')
    expect(events.some(e => e.event.case === 'assistantDelta')).toBe(true)
    await t.respondToPermission(info.id, perm!.event.case === 'permissionRequest' ? perm!.event.value.requestId : '', RespondToPermissionRequest_Decision.ALLOW)
    await new Promise(r => setTimeout(r, 350))
    const finished = events.filter(e => e.event.case === 'turnFinished')
    expect(finished).toHaveLength(1)
    expect(events.some(e => e.event.case === 'toolCallFinished')).toBe(true)
    expect(events.some(e => e.event.case === 'todosUpdated')).toBe(true)
    expect(events.some(e => e.event.case === 'checkpointCreated')).toBe(false)
    await expect(t.rewind(info.id, 'u1', RewindRequest_Mode.CODE)).rejects.toThrow(/failed_precondition/)
    await expect(t.compact(info.id)).rejects.toThrow(/failed_precondition/)
    await expect(t.createSession({ cwd: CWD, resumeSessionId: info.id, fork: true })).rejects.toThrow(/failed_precondition/)
  })

  it('waits for a sign-in, then continues; terminal logins verify afterwards; modes switch', async () => {
    const t = new MockTransport({ tick: 0 })
    const info = await t.createSession({ cwd: CWD, engine: 'acp:claude-acp' })
    expect(info.acp?.needsAuth).toBe(true)
    await expect(t.sendUserMessage(info.id, 'u1', 'hi', SendUserMessageRequest_Delivery.QUEUE)).rejects.toThrow(/sign in/)
    const term = info.acp!.authMethods.find(m => m.kind === AcpAuthMethod_Kind.TERMINAL)!
    const r = await t.acp().authenticate({ agentId: 'claude-acp', methodId: term.id, sessionId: info.id })
    expect(r.authenticated).toBe(false)
    expect(r.terminal?.shellLine).toMatch(/login/)
    const v = await t.acp().authenticate({ agentId: 'claude-acp', sessionId: info.id, verify: true })
    expect(v.authenticated).toBe(true)
    const [now] = (await t.listSessions()).filter(s => s.id === info.id)
    expect(now!.acp?.needsAuth).toBe(false)
    await t.acp().setMode(info.id, 'plan')
    const [planned] = (await t.listSessions()).filter(s => s.id === info.id)
    expect(planned!.permissionMode).toBe(PermissionMode.PLAN)
    expect(planned!.acp?.currentModeId).toBe('plan')
    await expect(t.createSession({ cwd: CWD, engine: 'acp:gemini' })).rejects.toThrow(/not installed/)
  })
})

const SERVER: ModelInfo[] = [
  { id: 'claude-sonnet', displayName: 'Claude Sonnet', provider: 'Anthropic', contextWindow: 200_000, price: '$3 / $15' },
  // The same id as one of the mock Codex agent's.
  { id: 'gpt-5.2', provider: 'OpenAI', contextWindow: 400_000, price: '' },
  { id: 'qwen', provider: 'Local', contextWindow: 32_000, price: '', offline: true }
]

async function choices(scope: ModelScope, extra: { query?: string, favorites?: string[], models?: ModelInfo[] } = {}) {
  const t = new MockTransport({ tick: 0 })
  const { agents, sessionSwitchRefusal } = await t.acp().listAgents()
  return { t, agents, sections: modelSections({ models: SERVER, agents, refusal: sessionSwitchRefusal, scope, ...extra }) }
}

describe('the model list', () => {
  it('has a group per connected agent after the server providers on a new session', async () => {
    const { sections } = await choices({ kind: 'new' })
    expect(sections.map(s => s.title)).toEqual(['Anthropic', 'OpenAI', 'Local', 'Codex'])
    const codex = sections.at(-1)!
    expect(codex.rows.map(r => r.name)).toEqual(['GPT-5.2 Codex', 'GPT-5.2', 'GPT-5.2 Mini'])
    expect(codex.rows.every(r => r.engine === 'acp:codex-acp' && !r.disabledReason)).toBe(true)
    expect(codex.note).toBe('')
  })

  it('leaves out agents that are not installed, signed out or unavailable, and looks as before without any', async () => {
    const { agents } = await choices({ kind: 'new' })
    expect(agents.filter(a => a.selectable).map(a => a.id)).toEqual(['codex-acp'])
    const plain = modelSections({ models: SERVER, agents: [], refusal: '', scope: { kind: 'new' } })
    expect(plain.map(s => s.title)).toEqual(['Anthropic', 'OpenAI', 'Local'])
    expect(plain.flatMap(s => s.rows).map(r => [r.key, r.name, r.detail])).toEqual([
      ['claude-sonnet', 'Claude Sonnet', 'claude-sonnet'], ['gpt-5.2', 'gpt-5.2', ''], ['qwen', 'qwen', '']
    ])
  })

  it('shows agents disabled with the reason in a harness session', async () => {
    const { sections } = await choices({ kind: 'session', engine: 'harness', acp: null })
    const codex = sections.at(-1)!
    expect(codex.note).toBe('Start a new session to use this agent')
    expect(codex.rows.every(r => r.disabledReason === 'Start a new session to use this agent')).toBe(true)
    expect(sections[0]!.rows.every(r => !r.disabledReason)).toBe(true)
    // Enter never picks a disabled row.
    expect(firstPickable(modelSections({ models: [], agents: (await choices({ kind: 'new' })).agents, refusal: 'no', scope: { kind: 'session', engine: 'harness', acp: null } }))).toBeUndefined()
  })

  it('shows only its own agent in a session on an agent', async () => {
    const t = new MockTransport({ tick: 0 })
    const info = await t.createSession({ cwd: CWD, engine: 'acp:codex-acp' })
    const sections = modelSections({ models: SERVER, agents: (await t.acp().listAgents()).agents, refusal: 'no', scope: { kind: 'session', engine: info.engine, acp: info.acp ?? null } })
    expect(sections.map(s => s.title)).toEqual(['Codex'])
    expect(sections[0]!.rows.map(r => r.id)).toEqual(['gpt-5.2-codex', 'gpt-5.2', 'gpt-5.2-mini'])
    expect(sections[0]!.rows.every(r => !r.disabledReason)).toBe(true)
  })

  it('shows only server models where nothing else can run', async () => {
    const { sections } = await choices({ kind: 'server' })
    expect(sections.map(s => s.title)).toEqual(['Anthropic', 'OpenAI', 'Local'])
  })

  it('keeps equal model ids apart: keys, favorites, search', async () => {
    const key = modelKey('gpt-5.2', 'acp:codex-acp')
    const { sections } = await choices({ kind: 'new' }, { favorites: [key] })
    expect(sections[0]!.title).toBe('Favorites')
    expect(sections[0]!.rows.map(r => r.key)).toEqual([key])
    expect(sections.find(s => s.title === 'OpenAI')!.rows.map(r => r.key)).toEqual(['gpt-5.2'])
    const found = (await choices({ kind: 'new' }, { query: 'codex' })).sections
    // By the agent's name too, and the groups with no match go away.
    expect(found.map(s => s.title)).toEqual(['Codex'])
    expect(found[0]!.rows).toHaveLength(3)
    expect((await choices({ kind: 'new' }, { query: 'mini' })).sections.flatMap(s => s.rows.map(r => r.id))).toEqual(['gpt-5.2-mini'])
  })

  it('says how finding the models goes, and keeps the old list on a failure', async () => {
    const { agents } = await choices({ kind: 'new' })
    const codex = agents.find(a => a.id === 'codex-acp')!
    const section = (a: typeof codex) => modelSections({ models: [], agents: [a], refusal: '', scope: { kind: 'new' } })[0]
    expect(section({ ...codex, models: [], modelsStatus: AcpModelsStatus.PROBING })!.note).toBe('Finding models…')
    expect(section({ ...codex, modelsStatus: AcpModelsStatus.PROBING })!.note).toBe('')
    const failed = section({ ...codex, modelsStatus: AcpModelsStatus.FAILED, modelsError: 'exit status 1' })!
    expect(failed.note).toContain('exit status 1')
    expect(failed.failed).toBe(true)
    expect(failed.rows).toHaveLength(3)
    // Nothing to show and nothing going on: no empty group.
    expect(section({ ...codex, models: [], modelsStatus: AcpModelsStatus.OK })).toBeUndefined()
  })
})

describe('the new-session options', () => {
  it('starts on the picked agent, with its mode and without what it cannot do', async () => {
    const t = new MockTransport({ tick: 0 })
    const { agents } = await t.acp().listAgents()
    const agent = agents.find(a => a.id === 'codex-acp')!
    const choice = newSessionChoice({ engine: agent.engine, agent, acpMode: 'read-only', effort: 3, mode: 2, worktree: { baseRef: 'main' } })
    expect(choice).toEqual({ effort: undefined, mode: undefined, worktree: undefined, engine: 'acp:codex-acp', acpModeId: 'read-only' })
    const info = await t.createSession({ cwd: CWD, model: 'gpt-5.2-mini', ...choice })
    expect(info.engine).toBe('acp:codex-acp')
    expect(info.acp?.currentModelId).toBe('gpt-5.2-mini')
    expect(info.acp?.currentModeId).toBe('read-only')
  })

  it('leaves a harness session as it was', () => {
    expect(newSessionChoice({ engine: '', acpMode: 'plan', effort: 3, mode: 2, worktree: { baseRef: 'main' } }))
      .toEqual({ effort: 3, mode: 2, worktree: { baseRef: 'main' }, engine: undefined, acpModeId: undefined })
  })

  it('falls back when the remembered agent or model is gone', async () => {
    const t = new MockTransport({ tick: 0 })
    const { agents } = await t.acp().listAgents()
    const keep = { engine: 'acp:codex-acp', model: 'gpt-5.2' }
    expect(fitAcpChoice(keep, agents)).toEqual(keep)
    expect(fitAcpChoice({ engine: '', model: 'gpt-5.2' }, agents)).toEqual({ engine: '', model: 'gpt-5.2' })
    // Signed out, or not installed.
    expect(fitAcpChoice({ engine: 'acp:claude-acp', model: 'opus' }, agents)).toEqual({ engine: '', model: '' })
    expect(fitAcpChoice({ engine: 'acp:gone', model: 'x' }, agents)).toEqual({ engine: '', model: '' })
    // The agent no longer has the model: its own default.
    expect(fitAcpChoice({ engine: 'acp:codex-acp', model: 'gpt-4' }, agents)).toEqual({ engine: 'acp:codex-acp', model: 'gpt-5.2-codex' })
  })
})

describe('sessions keep to their engine in the mock transport', () => {
  it('refuses crossing and takes the agent\'s own models', async () => {
    const t = new MockTransport({ tick: 0 })
    const harness = await t.createSession({ cwd: CWD })
    await expect(t.setModel(harness.id, 'gpt-5.2', 'acp:codex-acp')).rejects.toThrow(/failed_precondition.*start a new session/)
    const info = await t.createSession({ cwd: CWD, engine: 'acp:codex-acp' })
    await expect(t.setModel(info.id, 'claude-sonnet')).rejects.toThrow(/failed_precondition/)
    await expect(t.setModel(info.id, 'gpt-5.2', 'acp:claude-acp')).rejects.toThrow(/failed_precondition/)
    await t.setModel(info.id, 'gpt-5.2')
    const [now] = (await t.listSessions()).filter(s => s.id === info.id)
    expect(now!.acp?.currentModelId).toBe('gpt-5.2')
    expect(now!.model).toBe('gpt-5.2')
  })

  it('makes a signed-in agent connected, with its models', async () => {
    const t = new MockTransport({ tick: 0 })
    const r = await t.acp().authenticate({ agentId: 'claude-acp', methodId: 'claude-login' })
    expect(r.agent?.selectable).toBe(true)
    expect(r.agent?.models.map(m => m.id)).toEqual(['default', 'opus', 'sonnet'])
    expect((await t.acp().uninstallAgent('claude-acp')).selectable).toBe(false)
  })
})
