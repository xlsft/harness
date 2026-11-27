// Cloud sessions (docs/design/cloud-sessions.md) as the screens see them: the container's state on a session, the runner
// settings form's units, and how the work of a cloud session reads while it waits.
import { describe, expect, it } from 'vitest'
import { CloudEnvironmentState } from '~/gen/harness/agent/v1/agent_pb'
import { sessionMeta } from '~/lib/session/reducer'
import { sessionInfo } from '~/lib/transport/events'
import { CLOUD_HOST, emptyRunnerConfig, runnerConfigFromForm, runnerForm, workStateText, type HostWorkView } from '~/lib/hosts/types'
import { MockHostsApi } from '~/lib/hosts/mock'

describe('cloud sessions', () => {
  it('a session carries its container state, and other sessions none', () => {
    expect(sessionMeta(sessionInfo({ id: 'c1', cloudEnvironment: CloudEnvironmentState.STARTING })).cloudEnvironment).toBe('starting')
    expect(sessionMeta(sessionInfo({ id: 'c2', cloudEnvironment: CloudEnvironmentState.REMOVED })).cloudEnvironment).toBe('removed')
    expect(sessionMeta(sessionInfo({ id: 'l1' })).cloudEnvironment).toBe('')
  })

  it('the runner form speaks CPUs, GiB, minutes and hours, and empty keeps the defaults', () => {
    const empty = runnerConfigFromForm(runnerForm(null))
    expect(empty).toEqual(emptyRunnerConfig())
    const f = runnerForm(null)
    Object.assign(f, { cpus: '1.5', memoryGiB: '2', idleMinutes: '30', lifetimeHours: '12', runtime: 'runsc', allowedImages: 'a:1\n\n b:2 ' })
    const c = runnerConfigFromForm(f)
    expect(c).toMatchObject({ cpuMillis: 1500, memoryBytes: 2 * 1024 ** 3, idleTimeoutSeconds: 1800, maxLifetimeSeconds: 43_200, runtime: 'runsc', allowedImages: ['a:1', 'b:2'] })
    expect(runnerForm(c)).toMatchObject({ cpus: '1.5', memoryGiB: '2', idleMinutes: '30', lifetimeHours: '12' })
  })

  it('a cloud session waiting for its container says so', async () => {
    const api = new MockHostsApi()
    const w: HostWorkView = await api.start({ hostId: CLOUD_HOST, repositoryKey: 'github.com/a/b', baseRef: '', prompt: 'x', model: '', landing: 'branch', freshClone: false, idempotencyKey: 'k' })
    expect(w.cloud).toBe(true)
    expect(w.hostName).toBe('runner-1')
    expect(workStateText(w)).toBe('Starting the cloud machine…')
    expect(workStateText({ ...w, cloud: false })).toBe('Waiting for the machine')
  })
})
