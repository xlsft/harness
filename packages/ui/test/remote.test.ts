import { createRouterTransport } from '@connectrpc/connect'
import { describe, expect, it } from 'vitest'
import { PermissionMode } from '../app/gen/harness/agent/v1/agent_pb'
import { ChatDelivery, ChatProvider, ChatService, type PutChatAppRequest, type PutMyChatBotRequest } from '../app/gen/harness/chat/v1/chat_pb'
import { RelayService, type SpawnSessionRequest } from '../app/gen/harness/relay/v1/relay_pb'
import { ConnectRemoteApi } from '../app/lib/remote/connect'
import { MockRemoteApi } from '../app/lib/remote/mock'
import { REMOTE_MODES } from '../app/lib/remote/types'
import { MockTransport } from '../app/lib/transport/mock'

describe('RemoteApi over Connect', () => {
  const spawns: SpawnSessionRequest[] = []
  const frontends: string[] = []
  const puts: PutChatAppRequest[] = []
  const botPuts: PutMyChatBotRequest[] = []
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
      getChatApp: req => req.provider === ChatProvider.TELEGRAM
        ? { app: { provider: ChatProvider.TELEGRAM, configured: true, enabled: true, teamId: '111', teamName: 'org_bot', botUserId: '111', requestUrl: 'https://h/api/chat/telegram/webhook/org', delivery: ChatDelivery.UNSPECIFIED, activeDelivery: ChatDelivery.WEBHOOK } }
        : { app: { provider: ChatProvider.SLACK, configured: true, enabled: true, teamId: 'T1', teamName: 'Team', requestUrl: 'https://h/api/chat/slack/events', matchByEmail: true } },
      putChatApp: (req) => {
        puts.push(req)
        return { app: { provider: req.provider, configured: true, enabled: req.enabled, delivery: req.delivery, activeDelivery: req.delivery } }
      },
      listMyChatIdentities: () => ({
        identities: [
          { provider: ChatProvider.SLACK, teamId: 'T1', externalUserId: 'U1', displayName: 'me', linkedVia: 'code', linkedAt: 'x' },
          { provider: ChatProvider.TELEGRAM, teamId: '111', externalUserId: '9001', displayName: 'Me', linkedVia: 'code', linkedAt: 'y' }
        ],
        available: true,
        availableProviders: [ChatProvider.SLACK, ChatProvider.TELEGRAM],
        telegramBotUsername: 'org_bot'
      }),
      startChatLink: req => ({ code: 'ABCD-EFGH', expiresAt: 'later', deepLink: req.provider === ChatProvider.TELEGRAM ? `https://t.me/${req.personalBot ? 'mine_bot' : 'org_bot'}?start=ABCD-EFGH` : '' }),
      getMyChatBot: () => ({ allowed: true }),
      putMyChatBot: (req) => {
        botPuts.push(req)
        return { bot: { provider: ChatProvider.TELEGRAM, id: 'cbt_1', enabled: req.enabled, botUserId: '222', botUsername: 'mine_bot', delivery: req.delivery, activeDelivery: ChatDelivery.POLLING } }
      }
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
    expect(await api.identities()).toMatchObject({ available: true, identities: [{ provider: 'slack', externalUserId: 'U1', linkedVia: 'code' }, { provider: 'telegram', externalUserId: '9001' }] })
    expect((await api.startLink()).code).toBe('ABCD-EFGH')
  })

  it('handles the Telegram bot, its deep links and a personal bot without ever reading a token back', async () => {
    expect(await api.chatApp('telegram')).toMatchObject({ provider: 'telegram', teamName: 'org_bot', delivery: 'auto', activeDelivery: 'webhook', matchByEmail: false })
    expect(await api.putChatApp({ provider: 'telegram', botToken: '1:x', enabled: true, matchByEmail: false, delivery: 'polling' })).toMatchObject({ provider: 'telegram', delivery: 'polling' })
    expect(puts.at(-1)).toMatchObject({ provider: ChatProvider.TELEGRAM, botToken: '1:x', delivery: ChatDelivery.POLLING })
    const ids = await api.identities()
    expect(ids.providers).toEqual(['slack', 'telegram'])
    expect(ids.telegramBot).toBe('org_bot')
    expect(ids.identities.map(i => i.provider)).toEqual(['slack', 'telegram'])
    expect((await api.startLink('telegram')).deepLink).toBe('https://t.me/org_bot?start=ABCD-EFGH')
    expect((await api.startLink('telegram', true)).deepLink).toBe('https://t.me/mine_bot?start=ABCD-EFGH')
    expect(await api.myBot()).toEqual({ bot: undefined, allowed: true })
    const bot = await api.putMyBot({ botToken: '2:y', enabled: true, delivery: 'auto' })
    expect(bot).toMatchObject({ botUsername: 'mine_bot', delivery: 'auto', activeDelivery: 'polling' })
    expect(botPuts.at(-1)).toMatchObject({ provider: ChatProvider.TELEGRAM, delivery: ChatDelivery.UNSPECIFIED })
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

describe('telegram on the mock client', () => {
  it('checks the token shape, keeps it write-only, and offers links to the bots', async () => {
    const api = new MockRemoteApi()
    await expect(api.putChatApp({ provider: 'telegram', enabled: true, matchByEmail: false })).rejects.toThrow(/required/)
    await expect(api.putChatApp({ provider: 'telegram', botToken: 'xoxb-1', enabled: true, matchByEmail: false })).rejects.toThrow(/not a Telegram bot token/)
    const app = await api.putChatApp({ provider: 'telegram', botToken: '123456:ABCDEFGHIJKLMNOPQRSTUVWXYZ', enabled: true, matchByEmail: false })
    expect(app).toMatchObject({ configured: true, activeDelivery: 'polling' })
    expect(JSON.stringify(app)).not.toContain('ABCDEFGHIJ')
    expect((await api.identities()).providers).toEqual(['telegram'])
    expect((await api.startLink('telegram')).deepLink).toMatch(/^https:\/\/t\.me\/demo_harness_bot\?start=/)
    const bot = await api.putMyBot({ botToken: '654321:ABCDEFGHIJKLMNOPQRSTUVWXYZ', enabled: true, delivery: 'webhook' })
    expect(JSON.stringify(bot)).not.toContain('ABCDEFGHIJ')
    expect((await api.myBot()).bot?.activeDelivery).toBe('webhook')
    await api.deleteMyBot()
    expect((await api.myBot()).bot).toBeUndefined()
  })
})
