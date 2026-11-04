import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { PullRequest_State, SessionInfoSchema } from '../app/gen/harness/agent/v1/agent_pb'
import { sessionMeta } from '../app/lib/session/reducer'
import { MockTransport } from '../app/lib/transport/mock'
import { prChip, prDetails } from '../app/utils/pr'

const base = {
  number: 12, url: 'https://github.com/o/r/pull/12', state: 'open' as const, title: 'Fix', checksState: 'passing', checksTotal: 2, checksPassed: 2, checksFailed: 0,
  mergeable: true, hasConflicts: false, reviewDecision: 'none', unresolvedComments: 0, monitoring: false, autoMerge: false, problem: ''
}

describe('pull request state', () => {
  it('maps SessionInfo.pull_request into the session meta', () => {
    const meta = sessionMeta(create(SessionInfoSchema, {
      id: 's',
      pullRequest: { number: 12, url: 'u', state: PullRequest_State.DRAFT, checksState: 'failing', checksFailed: 1, unresolvedComments: 3, monitoring: true, problem: 'gh is not logged in' }
    }))
    expect(meta.pullRequest).toMatchObject({ number: 12, state: 'draft', checksState: 'failing', checksFailed: 1, unresolvedComments: 3, monitoring: true, problem: 'gh is not logged in' })
    expect(sessionMeta(create(SessionInfoSchema, { id: 's' })).pullRequest).toBeNull()
  })

  it('the chip says the most pressing thing', () => {
    expect(prChip(base).text).toBe('passing')
    expect(prChip({ ...base, checksState: 'failing', checksFailed: 2 }).text).toBe('2 failing')
    expect(prChip({ ...base, hasConflicts: true, checksState: 'failing' }).text).toBe('conflicts')
    expect(prChip({ ...base, state: 'merged', hasConflicts: true }).text).toBe('merged')
    expect(prChip({ ...base, problem: 'no gh' }).text).toBe('unavailable')
    expect(prChip({ ...base, unresolvedComments: 4 }).text).toBe('4 unresolved')
    expect(prDetails({ ...base, autoMerge: true })).toContain('Auto-merge is on')
  })
})

describe('auto-fix switch', () => {
  it('the mock client follows the intent and refuses without a PR', async () => {
    const t = new MockTransport({ tick: 0 })
    const list = await t.listSessions()
    const withPr = list.find(s => s.pullRequest)!
    const without = list.find(s => !s.pullRequest && !s.remote)!
    await t.setPrMonitor(withPr.id, true)
    expect((await t.listSessions()).find(s => s.id === withPr.id)!.pullRequest!.monitoring).toBe(true)
    await expect(t.setPrMonitor(without.id, true)).rejects.toThrow(/failed_precondition/)
  })

  it('the wake cap is a validated preference', async () => {
    const t = new MockTransport({ tick: 0 })
    expect((await t.getUserPreferences()).prMonitorWakesPerHour).toBe(30)
    expect((await t.updateUserPreferences({ prMonitorWakesPerHour: 60 })).prMonitorWakesPerHour).toBe(60)
    for (const bad of [0, -1, 241, 1.5]) await expect(t.updateUserPreferences({ prMonitorWakesPerHour: bad })).rejects.toThrow(/invalid_argument/)
    expect((await t.getUserPreferences()).prMonitorWakesPerHour).toBe(60)
  })
})
