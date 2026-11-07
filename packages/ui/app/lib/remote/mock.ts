// In-memory RemoteApi for the mock transport. Like the server, it keeps the apps' and bots' secrets write-only.

import type { ChatAppInput, ChatAppView, ChatBotInput, ChatBotView, ChatIdentityView, ChatKind, FolderView, MachineView, RemoteApi, RemoteStartInput } from './types'

const TELEGRAM_TOKEN = /^\d{3,20}:[\w-]{20,100}$/

function emptyApp(provider: ChatKind): ChatAppView {
  const url = provider === 'telegram' ? 'http://localhost:3000/api/chat/telegram/webhook/org' : 'http://localhost:3000/api/chat/slack/events'
  return { provider, configured: false, enabled: false, teamId: '', teamName: '', botUserId: '', requestUrl: url, matchByEmail: provider === 'slack', lastEventAt: '', lastError: '', disabledByPolicy: false, delivery: 'auto', activeDelivery: 'auto' }
}

export class MockRemoteApi implements RemoteApi {
  started: RemoteStartInput[] = []
  private hosts: MachineView[] = [
    { hostId: 'hst_laptop', deviceId: 'dev_laptop', name: 'Laptop', online: true, remoteStarts: true, daemon: false, lastSeenAt: new Date().toISOString() },
    { hostId: 'hst_desk', deviceId: 'dev_desk', name: 'Desktop', online: false, remoteStarts: true, daemon: false, lastSeenAt: new Date(Date.now() - 3_600_000).toISOString() }
  ]

  private folderList: FolderView[] = [
    { path: '/Users/demo/src/harness', name: 'harness', branch: 'main' },
    { path: '/Users/demo/src/site', name: 'site', branch: 'dev' }
  ]

  private apps: Record<ChatKind, ChatAppView> = { slack: emptyApp('slack'), telegram: emptyApp('telegram') }
  private ids: ChatIdentityView[] = []
  private bot: ChatBotView | undefined

  async machines() {
    return this.hosts
  }

  async folders(hostId: string) {
    const h = this.hosts.find(x => x.hostId === hostId)
    if (!h?.remoteStarts) throw new Error('This machine does not take remote starts')
    if (!h.online) throw new Error('The host is offline')
    return this.folderList
  }

  async start(input: RemoteStartInput) {
    if (!this.folderList.some(f => f.path === input.folder)) throw new Error(`${input.folder} is not one of this machine's recent folders`)
    this.started.push(input)
    return `mock-remote-${this.started.length}`
  }

  async chatApp(provider: ChatKind = 'slack') {
    return { ...this.apps[provider] }
  }

  async putChatApp(input: ChatAppInput) {
    const provider = input.provider ?? 'slack'
    const cur = this.apps[provider]
    if (provider === 'telegram') {
      if (!cur.configured && !input.botToken) throw new Error('A bot token is required')
      if (input.botToken && !TELEGRAM_TOKEN.test(input.botToken)) throw new Error('That is not a Telegram bot token')
      const delivery = input.delivery ?? 'auto'
      this.apps.telegram = { ...cur, configured: true, enabled: input.enabled, matchByEmail: false, teamId: '700000', teamName: 'demo_harness_bot', botUserId: '700000', delivery, activeDelivery: delivery === 'auto' ? 'polling' : delivery }
      return { ...this.apps.telegram }
    }
    if (!cur.configured && (!input.botToken || !input.signingSecret)) throw new Error('A bot token and a signing secret are required')
    this.apps.slack = { ...cur, configured: true, enabled: input.enabled, matchByEmail: input.matchByEmail, teamId: 'T0DEMO', teamName: 'Demo', botUserId: 'UBOT' }
    return { ...this.apps.slack }
  }

  async deleteChatApp(provider: ChatKind = 'slack') {
    this.apps[provider] = emptyApp(provider)
  }

  async identities() {
    const providers: ChatKind[] = []
    if (this.apps.slack.configured && this.apps.slack.enabled) providers.push('slack')
    if ((this.apps.telegram.configured && this.apps.telegram.enabled) || this.bot?.enabled) providers.push('telegram')
    return { identities: [...this.ids], available: providers.length > 0, providers, telegramBot: this.apps.telegram.configured ? this.apps.telegram.teamName : '' }
  }

  async startLink(provider: ChatKind = 'slack', personalBot = false) {
    const code = 'ABCD-2345'
    const bot = personalBot ? this.bot?.botUsername : this.apps.telegram.teamName
    return { code, expiresAt: new Date(Date.now() + 15 * 60_000).toISOString(), deepLink: provider === 'telegram' && bot ? `https://t.me/${bot}?start=${code}` : '' }
  }

  async unlink(id: ChatIdentityView) {
    this.ids = this.ids.filter(x => x.provider !== id.provider || x.externalUserId !== id.externalUserId || x.teamId !== id.teamId)
  }

  async myBot() {
    return { bot: this.bot ? { ...this.bot } : undefined, allowed: true }
  }

  async putMyBot(input: ChatBotInput) {
    if (!this.bot && !input.botToken) throw new Error('A bot token is required')
    if (input.botToken && !TELEGRAM_TOKEN.test(input.botToken)) throw new Error('That is not a Telegram bot token')
    const active = input.delivery === 'auto' ? 'polling' : input.delivery
    this.bot = { id: 'cbt_demo', botUserId: '700001', botUsername: 'my_demo_bot', requestUrl: 'http://localhost:3000/api/chat/telegram/webhook/cbt_demo', lastEventAt: '', lastError: '', ...this.bot, enabled: input.enabled, delivery: input.delivery, activeDelivery: active }
    return { ...this.bot }
  }

  async deleteMyBot() {
    if (!this.bot) throw new Error('You have no bot connected')
    this.ids = this.ids.filter(x => x.provider !== 'telegram' || x.teamId !== this.bot!.botUserId)
    this.bot = undefined
  }
}
