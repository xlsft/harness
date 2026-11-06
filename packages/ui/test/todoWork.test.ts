import { describe, expect, it } from 'vitest'
import { createSessionState, reduce } from '../app/lib/session/reducer'
import { todoWork, todoWorkLabel } from '../app/lib/session/todoWork'
import { MAIN_SESSION, mainHistory } from '../app/lib/transport/scenario'
import { EventBuilder } from '../app/lib/transport/events'
import { SubagentState } from '../app/gen/harness/agent/v1/subagents_pb'
import type { SessionState, TodoVM } from '../app/lib/session/types'

function setup() {
  const b = new EventBuilder('s', 1000)
  const s = createSessionState('s')
  return { b, s }
}

const agent = (b: EventBuilder, id: string, state: SubagentState) => b.subagent(id, 't-' + id, 'help', 'Explore', 'm', state)
const line = (s: SessionState, i = 0) => todoWorkLabel(todoWork(s.todos[i]!, s.subagents, s.tasks))
const item = (status: TodoVM['status'], agentIds: string[] = [], taskIds: string[] = []) => ({ content: 'Work', activeForm: 'Working', status, agentIds, taskIds })

describe('the running work under a plan item', () => {
  it('says one or many, counting agents and background tasks together', () => {
    expect(todoWorkLabel({ agents: 0, tasks: 0 })).toBe('')
    expect(todoWorkLabel({ agents: 1, tasks: 0 })).toBe('1 running task')
    expect(todoWorkLabel({ agents: 0, tasks: 1 })).toBe('1 running task')
    expect(todoWorkLabel({ agents: 3, tasks: 2 })).toBe('5 running tasks')
  })

  it('follows the live lists with no new list of todos', () => {
    const { b, s } = setup()
    reduce(s, agent(b, 'a1', SubagentState.RUNNING))
    reduce(s, agent(b, 'a2', SubagentState.QUEUED))
    reduce(s, b.taskStarted('k1', 't1', 'pnpm dev', 'dev'))
    reduce(s, b.todos([item('in_progress', ['a1', 'a2'], ['k1'])]))
    expect(line(s)).toBe('3 running tasks')

    reduce(s, agent(b, 'a1', SubagentState.FINISHED))
    expect(line(s)).toBe('2 running tasks')
    reduce(s, b.taskFinished('k1', 0, 100))
    expect(line(s)).toBe('1 running task')
    reduce(s, agent(b, 'a2', SubagentState.FAILED))
    expect(s.todos[0]!.status).toBe('in_progress')
    expect(line(s)).toBe('')
  })

  it('shows nothing for work that is not linked or an item that is not in progress', () => {
    const { b, s } = setup()
    reduce(s, agent(b, 'a1', SubagentState.RUNNING))
    reduce(s, b.todos([item('in_progress'), item('completed', ['a1']), item('pending', ['a1'])]))
    expect(s.todos[1]!.agentIds).toEqual(['a1'])
    expect([line(s, 0), line(s, 1), line(s, 2)]).toEqual(['', '', ''])
  })

  it('ignores ids of work this frontend has not heard of, as after a resume', () => {
    const { b, s } = setup()
    reduce(s, b.todos([item('in_progress', ['gone'], ['gone-too'])]))
    expect(line(s)).toBe('')
  })

  it('shows in the mock scenario: the in-progress item, and only it', () => {
    const s = createSessionState(MAIN_SESSION)
    for (const e of mainHistory(Date.now())) reduce(s, e)
    expect(s.todos.map((_, i) => line(s, i))).toEqual(['', '', '3 running tasks', ''])
  })
})
