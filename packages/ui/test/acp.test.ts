import { describe, expect, it } from 'vitest'
import type { SessionEvent } from '~/gen/harness/agent/v1/agent_pb'
import { RespondToPermissionRequest_Decision, SendUserMessageRequest_Delivery, PermissionMode, RewindRequest_Mode } from '~/gen/harness/agent/v1/agent_pb'
import { AcpAuthMethod_Kind, AcpAuthState, InstallAcpAgentResponse_Phase, SessionFeature, acpAgentOf, acpEngine } from '~/lib/acp/types'
import { MockTransport } from '~/lib/transport/mock'

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
