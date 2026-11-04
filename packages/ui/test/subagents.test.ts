import { describe, expect, it } from 'vitest'
import { createSessionState, reduce } from '../app/lib/session/reducer'
import type { SessionEvent } from '../app/gen/harness/agent/v1/agent_pb'
import { SubagentState, SubagentStepStatus } from '../app/gen/harness/agent/v1/subagents_pb'

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
