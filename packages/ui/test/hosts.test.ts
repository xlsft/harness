import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { AgentHostIsolation, AgentHostSchema, AgentHostScope, HostWorkSchema, HostWorkState } from '~/gen/harness/relay/v1/hosts_pb'
import { hostView, workView } from '~/lib/hosts/connect'
import { MockHostsApi } from '~/lib/hosts/mock'
import { formatBytes, hostLoadText, isolationText, repositoryKeyOf, workStateText } from '~/lib/hosts/types'

describe('hostView / workView', () => {
  it('maps scope, isolation, load and workers', () => {
    const h = hostView(create(AgentHostSchema, {
      id: 'ahs_1', name: 'runner', scope: AgentHostScope.TEAM, groupName: 'Platform', online: true, isolation: AgentHostIsolation.UID, maxSessions: 8, workingSessions: 3, queuedWork: 2, diskFreeBytes: 2048n,
      workers: [{ userId: 'usr_1', userName: 'Ada', online: true, workingSessions: 1 }]
    }))
    expect(h).toMatchObject({ scope: 'team', isolation: 'uid', diskFreeBytes: 2048, workers: [{ userName: 'Ada', workingSessions: 1 }] })
    expect(hostLoadText(h)).toBe('3/8, 2 waiting')
    expect(isolationText(h)).toBe('A Unix user for each member')
    expect(isolationText({ ...h, isolation: 'none' })).toContain('takes no work')
    expect(hostLoadText({ ...h, online: false, queuedWork: 0 })).toBe('offline')
  })

  it('says what became of work', () => {
    const w = workView(create(HostWorkSchema, { id: 'hwk_1', state: HostWorkState.FAILED, error: 'connect your account' }))
    expect(w.state).toBe('failed')
    expect(workStateText(w)).toBe('Failed: connect your account')
    expect(workView(create(HostWorkSchema, { state: HostWorkState.CLAIMED })).state).toBe('preparing')
    expect(workView(undefined).state).toBe('queued')
  })
})

describe('repositoryKeyOf', () => {
  it('takes what people paste', () => {
    expect(repositoryKeyOf('github.com/Acme/Widgets')).toBe('github.com/acme/widgets')
    expect(repositoryKeyOf('https://github.com/acme/widgets.git')).toBe('github.com/acme/widgets')
    expect(repositoryKeyOf('git@gitlab.example.com:group/sub/repo.git')).toBe('gitlab.example.com/group/sub/repo')
    expect(repositoryKeyOf(' ssh://git@github.com/acme/widgets/ ')).toBe('git@github.com/acme/widgets')
  })
})

describe('formatBytes', () => {
  it('reads', () => {
    expect(formatBytes(0)).toBe('')
    expect(formatBytes(1536)).toBe('1.5 KB')
    expect(formatBytes(120 * 1024 ** 3)).toBe('120 GB')
  })
})

describe('MockHostsApi', () => {
  it('hands out a token once and queues work', async () => {
    const api = new MockHostsApi()
    const made = await api.create({ name: 'n', team: false, groupId: '', maxSessions: 2, maxSessionsPerUser: 0, allowedRepositories: [] })
    expect(made.deviceToken).toMatch(/^hdt_/)
    expect((await api.list()).map(h => h.name)).toContain('n')
    const w = await api.start({ hostId: made.host.id, repositoryKey: 'h/o/r', baseRef: '', prompt: 'p', model: '', landing: 'pull_request', freshClone: false, idempotencyKey: 'k' })
    expect((await api.get(w.id)).state).toBe('queued')
    expect((await api.cancel(w.id)).state).toBe('cancelled')
    await api.setAway(made.host.id)
    expect((await api.list()).filter(h => h.awayHost).map(h => h.id)).toEqual([made.host.id])
    await api.remove(made.host.id)
    expect((await api.list()).map(h => h.id)).not.toContain(made.host.id)
  })
})
