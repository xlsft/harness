// How a session's pull request reads in its chip. The state comes from the client (SessionInfo.pull_request).
import type { SessionMeta } from '~/lib/session/types'

type PR = NonNullable<SessionMeta['pullRequest']>

export function prChip(pr: PR): { text: string, cls: string } {
  if (pr.state === 'merged') return { text: 'merged', cls: 'text-ok' }
  if (pr.state === 'closed') return { text: 'closed', cls: 'text-muted' }
  if (pr.problem) return { text: 'unavailable', cls: 'text-bad' }
  if (pr.hasConflicts) return { text: 'conflicts', cls: 'text-bad' }
  if (pr.checksState === 'failing') return { text: `${pr.checksFailed} failing`, cls: 'text-bad' }
  if (pr.unresolvedComments > 0) return { text: `${pr.unresolvedComments} unresolved`, cls: 'text-muted' }
  if (pr.checksState === 'running') return { text: 'checks running', cls: 'text-muted' }
  if (pr.state === 'draft') return { text: 'draft', cls: 'text-muted' }
  if (pr.reviewDecision === 'changes_requested') return { text: 'changes requested', cls: 'text-bad' }
  if (pr.checksState === 'passing' || pr.reviewDecision === 'approved') return { text: 'passing', cls: 'text-ok' }
  return { text: 'open', cls: 'text-muted' }
}

// The whole state, for the chip's tooltip.
export function prDetails(pr: PR): string {
  const lines = [`#${pr.number}${pr.title ? ` ${pr.title}` : ''}`, `State: ${pr.state}`]
  if (pr.checksState) lines.push(`Checks: ${pr.checksState} (${pr.checksPassed}/${pr.checksTotal} passed)`)
  lines.push(`Mergeable: ${pr.hasConflicts ? 'conflicts' : pr.mergeable ? 'yes' : 'not now'}`)
  if (pr.reviewDecision) lines.push(`Review: ${pr.reviewDecision.replace(/_/g, ' ')}`)
  lines.push(`Unresolved comments: ${pr.unresolvedComments}`)
  if (pr.autoMerge) lines.push('Auto-merge is on')
  if (pr.problem) lines.push(pr.problem)
  return lines.join('\n')
}
