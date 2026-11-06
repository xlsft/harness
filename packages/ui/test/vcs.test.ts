import { create } from '@bufbuild/protobuf'
import { createRouterTransport } from '@connectrpc/connect'
import { describe, expect, it } from 'vitest'
import { AgentService } from '../app/gen/harness/agent/v1/agent_pb'
import { GetUserAuthStatusResponse_State, ProviderType, UserConnectionSchema, VcsService } from '../app/gen/harness/vcs/v1/vcs_pb'
import { ConnectVcsApi, authState } from '../app/lib/vcs/connect'
import { MockVcsApi } from '../app/lib/vcs/mock'
import { MockTransport } from '../app/lib/transport/mock'

describe('Create PR on the mock client', () => {
  const pick = async (t: MockTransport) => (await t.listSessions()).find(s => !s.pullRequest && !s.remote && s.cwd)!

  it('drafts, then opens the PR, which shows on the session', async () => {
    const t = new MockTransport({ tick: 0 })
    const s = await pick(t)
    const draft = await t.draftPullRequest(s.id)
    expect(draft.title).toBeTruthy()
    expect(draft.provider).toBe('gh')
    expect(draft.drafted).toBe(true)
    const pr = await t.createPullRequest(s.id, { title: '  Keep this ', body: 'b', baseBranch: 'main', draft: true, monitor: true })
    expect(pr.existed).toBe(false)
    const info = (await t.listSessions()).find(x => x.id === s.id)!
    expect(info.pullRequest).toMatchObject({ number: pr.number, url: pr.url, title: 'Keep this', monitoring: true })
    // A second click finds the open one.
    expect((await t.createPullRequest(s.id, { title: 'x', body: '', baseBranch: 'main', draft: false, monitor: false })).existed).toBe(true)
  })

  it('needs a title, like the client', async () => {
    const t = new MockTransport({ tick: 0 })
    const s = await pick(t)
    await expect(t.createPullRequest(s.id, { title: '  ', body: '', baseBranch: 'main', draft: false, monitor: false })).rejects.toThrow(/failed_precondition/)
  })
})

describe('code hosts on the mock client', () => {
  it('connects and disconnects an account, and keeps connections to admins', async () => {
    const api = new MockVcsApi()
    const [a] = await api.userConnections()
    expect(a!.state).toBe('not_connected')
    const flow = await api.startAuth(a!.id)
    expect((await api.authStatus(a!.id, flow.flowId)).state).toBe('connected')
    expect((await api.userConnections())[0]).toMatchObject({ state: 'connected', canWrite: true, login: 'demo' })
    await api.revokeAuth(a!.id)
    expect((await api.userConnections())[0]!.state).toBe('not_connected')

    const lab = await api.putGitLab({ name: 'Lab', baseUrl: 'https://gitlab.example.com', allowPrivateNetwork: false })
    expect((await api.connections()).map(c => c.id)).toContain(lab.id)
    await api.deleteConnection(lab.id)
    expect((await api.connections()).map(c => c.id)).not.toContain(lab.id)

    api.admin = false
    await expect(api.connections()).rejects.toThrow(/permission_denied/)
    await expect(api.putGitLab({ name: '', baseUrl: 'x', allowPrivateNetwork: false })).rejects.toThrow(/permission_denied/)
  })
})

describe('VcsApi over Connect', () => {
  const transport = () => createRouterTransport(({ service }) => {
    service(AgentService, {
      getVcsStatus: () => ({ clis: [{ name: 'gh', installed: true, version: 'gh 2', hosts: [{ host: 'github.com', login: 'me' }], loginCommand: 'gh auth login' }] })
    })
    service(VcsService, {
      listUserConnections: () => ({
        connections: [create(UserConnectionSchema, { connectionId: 'vcs_1', type: ProviderType.GITLAB, displayName: 'Lab', baseUrl: 'https://l', host: 'l', state: GetUserAuthStatusResponse_State.CONNECTED, login: 'me', canWrite: true })]
      }),
      getGitHubAppManifest: req => ({ manifestJson: `{"name":"${req.appName}"}`, postUrl: 'https://g/settings/apps/new?state=s', state: 's' }),
      putConnection: req => ({ connection: { id: 'vcs_2', type: req.type, displayName: req.displayName, baseUrl: req.baseUrl, healthy: true } })
    })
  })

  it('maps the messages to the views', async () => {
    const api = new ConnectVcsApi(transport())
    expect((await api.clis())[0]).toMatchObject({ name: 'gh', installed: true, hosts: [{ host: 'github.com', login: 'me' }] })
    expect((await api.userConnections())[0]).toMatchObject({ id: 'vcs_1', kind: 'gitlab', state: 'connected', login: 'me', canWrite: true })
    expect(await api.gitHubManifest({ baseUrl: '', organization: '', appName: 'bot' })).toEqual({ postUrl: 'https://g/settings/apps/new?state=s', manifest: '{"name":"bot"}' })
    expect(await api.putGitLab({ name: 'Lab', baseUrl: 'https://l', accessToken: 'write-only', allowPrivateNetwork: false })).toMatchObject({ id: 'vcs_2', kind: 'gitlab', healthy: true })
  })

  it('reads every auth state', () => {
    expect(authState(GetUserAuthStatusResponse_State.PENDING)).toBe('pending')
    expect(authState(GetUserAuthStatusResponse_State.ERROR)).toBe('error')
    expect(authState(GetUserAuthStatusResponse_State.NOT_CONNECTED)).toBe('not_connected')
    expect(authState(GetUserAuthStatusResponse_State.UNSPECIFIED)).toBe('not_connected')
  })
})
