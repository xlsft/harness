import { createRouterTransport } from '@connectrpc/connect'
import { describe, expect, it } from 'vitest'
import { PermissionMode } from '../app/gen/harness/agent/v1/agent_pb'
import { ChatProvider, ChatService } from '../app/gen/harness/chat/v1/chat_pb'
import { RelayService, type SpawnSessionRequest } from '../app/gen/harness/relay/v1/relay_pb'
import { ConnectRemoteApi } from '../app/lib/remote/connect'
import { MockRemoteApi } from '../app/lib/remote/mock'
import { REMOTE_MODES } from '../app/lib/remote/types'
import { MockTransport } from '../app/lib/transport/mock'

describe('RemoteApi over Connect', () => {
  const spawns: SpawnSessionRequest[] = []
  const frontends: string[] = []
  const transport = createRouterTransport(({ service }) => {
    service(RelayService, {
      listHosts: () => ({ hosts: [{ hostId: 'h1', deviceId: 'd1', deviceName: 'Laptop', online: true, remoteStarts: true, lastSeenAt: '2026-11-07T10:00:00Z' }, { hostId: 'h2', deviceId: 'd2', deviceName: 'Desk', online: false }] }),
      listHostFolders: req => ({ folders: req.hostId === 'h1' ? [{ path: '/w/a', name: 'a', gitBranch: 'main', exists: true }, { path: '/w/gone', name: 'gone', exists: false }] : [] }),
      spawnSession: (req, ctx) => {
        spawns.push(req)
        frontends.push(ctx.requestHeader.get('x-harness-frontend') ?? '')
        return { session: { id: 'ses_1', cwd: req.spawn?.create?.cwd } }
      }
    })
    service(ChatService, {
      getChatApp: () => ({ app: { provider: ChatProvider.SLACK, configured: true, enabled: true, teamId: 'T1', teamName: 'Team', requestUrl: 'https://h/api/chat/slack/events' } }),
      listMyChatIdentities: () => ({ identities: [{ provider: ChatProvider.SLACK, teamId: 'T1', externalUserId: 'U1', displayName: 'me', linkedVia: 'code', linkedAt: 'x' }], available: true }),
      startChatLink: () => ({ code: 'ABCD-EFGH', expiresAt: 'later' })
    })
  })
  const api = new ConnectRemoteApi(transport)

  it('lists machines and only the folders that exist', async () => {
    expect((await api.machines()).map(m => [m.name, m.online, m.remoteStarts])).toEqual([['Laptop', true, true], ['Desk', false, false]])
    expect((await api.folders('h1')).map(f => f.path)).toEqual(['/w/a'])
  })

  it('starts a session with the folder, mode and first message, labelled as the phone', async () => {
    expect(await api.start({ hostId: 'h1', folder: '/w/a', mode: 'acceptEdits', message: '  fix it  ' })).toBe('ses_1')
    const s = spawns.at(-1)!
    expect(s.hostId).toBe('h1')
    expect(s.spawn?.create?.cwd).toBe('/w/a')
    expect(s.spawn?.create?.permissionMode).toBe(PermissionMode.ACCEPT_EDITS)
    expect(s.spawn?.initialMessage?.content[0]?.block).toMatchObject({ case: 'text', value: { text: 'fix it' } })
    expect(s.options?.idempotencyKey).toBeTruthy()
    expect(frontends.at(-1)).toBe('mobile')
    // An unknown mode (bypassPermissions included) falls back to the default; the server and the host refuse bypass anyway.
    await api.start({ hostId: 'h1', folder: '/w/a', mode: 'bypassPermissions', message: 'x' })
    expect(spawns.at(-1)!.spawn?.create?.permissionMode).toBe(PermissionMode.DEFAULT)
    expect(REMOTE_MODES).not.toContain('bypassPermissions')
  })

  it('reads the chat app without secrets and the user\'s own links', async () => {
    expect(await api.chatApp()).toMatchObject({ configured: true, teamName: 'Team', requestUrl: 'https://h/api/chat/slack/events' })
    expect(await api.identities()).toMatchObject({ available: true, identities: [{ externalUserId: 'U1', linkedVia: 'code' }] })
    expect((await api.startLink()).code).toBe('ABCD-EFGH')
  })
})

describe('remote starts on the mock client', () => {
  it('keeps the device switch off by default and starts only in recent folders', async () => {
    const t = new MockTransport({ tick: 0 })
    expect((await t.getDeviceSettings()).allowRemoteStarts).toBe(false)
    expect((await t.updateDeviceSettings({ allowRemoteStarts: true })).allowRemoteStarts).toBe(true)
    const api = t.remote() as MockRemoteApi
    const [laptop, desk] = await api.machines()
    await expect(api.folders(desk!.hostId)).rejects.toThrow(/offline/)
    const [f] = await api.folders(laptop!.hostId)
    await expect(api.start({ hostId: laptop!.hostId, folder: '/etc', mode: 'default', message: 'x' })).rejects.toThrow(/recent folders/)
    expect(await api.start({ hostId: laptop!.hostId, folder: f!.path, mode: 'default', message: 'x' })).toMatch(/^mock-remote-/)
  })

  it('needs both secrets to connect a chat app', async () => {
    const api = new MockRemoteApi()
    await expect(api.putChatApp({ enabled: true, matchByEmail: true })).rejects.toThrow(/required/)
    const app = await api.putChatApp({ botToken: 'xoxb-1', signingSecret: 's', enabled: true, matchByEmail: true })
    expect(app.configured).toBe(true)
    expect(JSON.stringify(app)).not.toContain('xoxb')
  })
})
