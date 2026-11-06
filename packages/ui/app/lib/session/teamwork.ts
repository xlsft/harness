// Goal mode and the task graph, as the session's events carry them.
import { GoalState, TaskNodeStatus, type Goal, type TaskNode } from '~/gen/harness/agent/v1/teamwork_pb'

export type GoalStateName = 'active' | 'met' | 'cap_reached' | 'paused' | 'stopped'

export interface GoalVM {
  condition: string
  state: GoalStateName
  turns: number
  maxTurns: number
  reason: string
  startedAt: string
}

const GOAL_STATES: Partial<Record<GoalState, GoalStateName>> = {
  [GoalState.ACTIVE]: 'active',
  [GoalState.MET]: 'met',
  [GoalState.CAP_REACHED]: 'cap_reached',
  [GoalState.PAUSED]: 'paused',
  [GoalState.STOPPED]: 'stopped'
}

export function goalVM(g: Goal): GoalVM {
  return { condition: g.condition, state: GOAL_STATES[g.state] ?? 'stopped', turns: g.turns, maxTurns: g.maxTurns, reason: g.reason, startedAt: g.startedAt }
}

const GOAL_TEXT: Record<GoalStateName, string> = {
  active: 'Working towards a goal',
  met: 'Goal met',
  cap_reached: 'Goal not met: out of turns',
  paused: 'Goal paused',
  stopped: 'Goal stopped'
}

export const goalStateText = (g: GoalVM) => GOAL_TEXT[g.state]

export type TaskNodeStatusName = 'pending' | 'in_progress' | 'done' | 'failed' | 'cancelled'

export interface TaskNodeVM {
  id: string
  title: string
  description: string
  status: TaskNodeStatusName
  ownerName: string
  blockedBy: string[]
  waitingFor: string[]
  team: string
  note: string
}

const NODE_STATUS: Partial<Record<TaskNodeStatus, TaskNodeStatusName>> = {
  [TaskNodeStatus.PENDING]: 'pending',
  [TaskNodeStatus.IN_PROGRESS]: 'in_progress',
  [TaskNodeStatus.DONE]: 'done',
  [TaskNodeStatus.FAILED]: 'failed',
  [TaskNodeStatus.CANCELLED]: 'cancelled'
}

export function taskNodeVM(t: TaskNode): TaskNodeVM {
  return { id: t.id, title: t.title, description: t.description, status: NODE_STATUS[t.status] ?? 'pending', ownerName: t.ownerName, blockedBy: t.blockedBy, waitingFor: t.waitingFor, team: t.team, note: t.note }
}

// One line on where a task stands.
export function taskNodeLine(t: TaskNodeVM): string {
  switch (t.status) {
    case 'in_progress': return t.ownerName ? `${t.ownerName} is on it` : 'In progress'
    case 'done': return 'Done'
    case 'failed': return 'Failed'
    case 'cancelled': return 'Cancelled'
  }
  return t.waitingFor.length ? `Waits for ${t.waitingFor.join(', ')}` : 'Free'
}

export function taskGraphCounts(list: readonly TaskNodeVM[]) {
  return { done: list.filter(t => t.status === 'done').length, total: list.length }
}
