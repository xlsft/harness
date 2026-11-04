import { describe, expect, it } from 'vitest'
import { createSessionState, reduce } from '../app/lib/session/reducer'
import { MockTransport } from '../app/lib/transport/mock'
import type { SessionEvent } from '../app/gen/harness/agent/v1/agent_pb'
import { SubagentState, SubagentStepStatus } from '../app/gen/harness/agent/v1/subagents_pb'
import { WorkflowState } from '../app/gen/harness/agent/v1/workflows_pb'

function agent(over: Record<string, unknown> = {}) {
  return {
    id: 'a1', parentId: '', toolUseId: 't1', name: 'find usages', type: 'Explore', model: 'm', state: SubagentState.RUNNING,
    currentTool: 'Grep', steps: [{ toolUseId: 's1', tool: 'Grep', target: 'foo', status: SubagentStepStatus.RUNNING }],
    toolCalls: 1, inputTokens: 100n, outputTokens: 20n, costUsd: 0.5, startedAt: '2026-11-04T00:00:00Z', finishedAt: '', report: '', error: '',
    worktreePath: '', worktreeBranch: '', ...over
  }
}

function event(n: number, a: unknown): SessionEvent {
  return { seq: BigInt(n), timestamp: '2026-11-04T00:00:01Z', event: { case: 'subagentUpdated', value: { agent: a } } } as unknown as SessionEvent
}

describe('workflows', () => {
  const run = (over: Record<string, unknown> = {}) => ({
    id: 'w1', toolUseId: 't9', name: 'review', description: 'Reviews', phases: ['One'], currentPhase: 'One', state: WorkflowState.RUNNING,
    agents: [{ agentId: 'a1', label: 'first', phase: 'One', state: SubagentState.RUNNING, cached: false, error: '' }], log: ['hello'],
    startedAt: '2026-11-04T00:00:00Z', finishedAt: '', error: '', result: '', inputTokens: 100n, outputTokens: 20n, costUsd: 0.5, ...over
  })
  const update = (n: number, w: unknown) => ({ seq: BigInt(n), timestamp: '2026-11-04T00:00:01Z', event: { case: 'workflowUpdated', value: { workflow: w } } }) as unknown as SessionEvent

  it('keeps runs in order, as they are now, without counting usage twice', () => {
    const s = createSessionState('s1')
    reduce(s, update(1, run()))
    reduce(s, update(2, run({ state: WorkflowState.FINISHED, result: 'done', finishedAt: '2026-11-04T00:01:00Z' })))
    reduce(s, update(3, run({ id: 'w2' })))
    expect(s.workflowOrder).toEqual(['w1', 'w2'])
    const w = s.workflows.w1!
    expect(w.state).toBe('finished')
    expect(w.result).toBe('done')
    expect(w.tokens).toBe(120)
    expect(w.agents[0]).toMatchObject({ agentId: 'a1', label: 'first', state: 'running' })
    expect(s.costUsd).toBe(0)
    expect(s.usage.inputTokens).toBe(0)
  })
})

describe('subagent limits', () => {
  it('are validated preferences with the defaults', async () => {
    const t = new MockTransport()
    expect(await t.getUserPreferences()).toMatchObject({ subagentMaxDepth: 2, subagentMaxRunning: 8 })
    expect(await t.updateUserPreferences({ subagentMaxDepth: 3, subagentMaxRunning: 16 })).toMatchObject({ subagentMaxDepth: 3, subagentMaxRunning: 16 })
    for (const bad of [0, 6, 1.5]) await expect(t.updateUserPreferences({ subagentMaxDepth: bad })).rejects.toThrow(/invalid_argument/)
    for (const bad of [0, 33]) await expect(t.updateUserPreferences({ subagentMaxRunning: bad })).rejects.toThrow(/invalid_argument/)
  })
})

describe('subagent events', () => {
  it('keeps the agents, live', () => {
    const s = createSessionState('s')
    reduce(s, event(1, agent()))
    expect(s.subagentOrder).toEqual(['a1'])
    const a = s.subagents.a1!
    expect(a).toMatchObject({ name: 'find usages', state: 'running', currentTool: 'Grep', toolCalls: 1, tokens: 120 })
    expect(a.steps).toEqual([{ id: 's1', tool: 'Grep', target: 'foo', status: 'running' }])

    reduce(s, event(2, agent({ state: SubagentState.FINISHED, currentTool: '', report: 'done', finishedAt: '2026-11-04T00:01:00Z', steps: [] })))
    expect(s.subagentOrder).toEqual(['a1'])
    expect(s.subagents.a1).toMatchObject({ state: 'finished', report: 'done' })
  })

  it('counts usage toward the session once', () => {
    const s = createSessionState('s')
    reduce(s, event(1, agent()))
    reduce(s, event(2, agent({ inputTokens: 300n, outputTokens: 50n, costUsd: 0.75 })))
    expect(s.costUsd).toBeCloseTo(0.75)
    expect(s.usage.inputTokens).toBe(300)
    expect(s.usage.outputTokens).toBe(50)
  })

  it('labels a prompt from an agent', () => {
    const s = createSessionState('s')
    reduce(s, { seq: 1n, timestamp: '', event: { case: 'permissionRequest', value: { requestId: 'p', toolUseId: 'x', toolName: 'Bash', inputJson: '{}', suggestedRules: [], reason: '', agentId: 'a1', agentName: 'find usages' } } } as unknown as SessionEvent)
    const item = s.items.find(i => i.kind === 'permission')
    expect(item).toMatchObject({ agentName: 'find usages' })
  })
})
