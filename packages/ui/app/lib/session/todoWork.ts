// The work under a plan item. The client links subagents and background tasks to the item (Todo.agent_ids, task_ids);
// what still runs is read here from the session's live lists, so work that ends updates the line by itself.
import type { SubagentVM, TaskVM, TodoVM } from './types'

export interface TodoWork {
  // Linked agents that are running or waiting for a slot, as the Agents pane counts them.
  agents: number
  tasks: number
}

export function todoWork(todo: TodoVM, subagents: Record<string, SubagentVM>, tasks: Record<string, TaskVM>): TodoWork {
  if (todo.status !== 'in_progress') return { agents: 0, tasks: 0 }
  return {
    agents: todo.agentIds.filter((id) => {
      const state = subagents[id]?.state
      return state === 'running' || state === 'queued'
    }).length,
    tasks: todo.taskIds.filter(id => tasks[id]?.state === 'running').length
  }
}

// Agents and background tasks count together: the line says how much runs for the item, the panes say what it is.
export function todoWorkLabel(work: TodoWork): string {
  const n = work.agents + work.tasks
  if (!n) return ''
  return n === 1 ? '1 running task' : `${n} running tasks`
}
