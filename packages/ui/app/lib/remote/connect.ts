// RemoteApi over the RelayService and ChatService calls the client forwards to the server
// (packages/client/server/coreproxy.go), on the same connect transport (and agent token).

import { createClient, type Client, type Transport } from '@connectrpc/connect'
import { PermissionMode } from '~/gen/harness/agent/v1/agent_pb'
import { ChatDelivery, ChatProvider, ChatService, type ChatApp, type ChatBot } from '~/gen/harness/chat/v1/chat_pb'
import { RelayService } from '~/gen/harness/relay/v1/relay_pb'
import type { ChatAppInput, ChatAppView, ChatBotInput, ChatBotView, ChatIdentityView, ChatKind, DeliveryKind, RemoteApi, RemoteStartInput } from './types'

const MODES: Record<string, PermissionMode> = {
  default: PermissionMode.DEFAULT,
  acceptEdits: PermissionMode.ACCEPT_EDITS,
  plan: PermissionMode.PLAN,
  auto: PermissionMode.AUTO,
  dontAsk: PermissionMode.DONT_ASK
}

const frontend = { headers: { 'X-Harness-Frontend': 'mobile' } }

const PROVIDERS: Record<ChatKind, ChatProvider> = { slack: ChatProvider.SLACK, telegram: ChatProvider.TELEGRAM }
const kindOf = (p: ChatProvider): ChatKind => (p === ChatProvider.TELEGRAM ? 'telegram' : 'slack')

const DELIVERIES: Record<DeliveryKind, ChatDelivery> = { auto: ChatDelivery.UNSPECIFIED, webhook: ChatDelivery.WEBHOOK, polling: ChatDelivery.POLLING }
const deliveryOf = (d: ChatDelivery | undefined): DeliveryKind => (d === ChatDelivery.WEBHOOK ? 'webhook' : d === ChatDelivery.POLLING ? 'polling' : 'auto')

function appView(provider: ChatKind, a: ChatApp | undefined): ChatAppView {
  return {
    provider,
    configured: a?.configured ?? false,
    enabled: a?.enabled ?? false,
    teamId: a?.teamId ?? '',
    teamName: a?.teamName ?? '',
    botUserId: a?.botUserId ?? '',
    requestUrl: a?.requestUrl ?? '',
    matchByEmail: a?.matchByEmail ?? provider === 'slack',
    lastEventAt: a?.lastEventAt ?? '',
    lastError: a?.lastError ?? '',
    disabledByPolicy: a?.disabledByPolicy ?? false,
    delivery: deliveryOf(a?.delivery),
    activeDelivery: deliveryOf(a?.activeDelivery)
  }
}

function botView(b: ChatBot): ChatBotView {
  return {
    id: b.id,
    enabled: b.enabled,
    botUserId: b.botUserId,
    botUsername: b.botUsername,
    delivery: deliveryOf(b.delivery),
    activeDelivery: deliveryOf(b.activeDelivery),
    requestUrl: b.requestUrl,
    lastEventAt: b.lastEventAt,
    lastError: b.lastError
  }
}

export class ConnectRemoteApi implements RemoteApi {
  private relay: Client<typeof RelayService>
  private chat: Client<typeof ChatService>

  constructor(transport: Transport) {
    this.relay = createClient(RelayService, transport)
    this.chat = createClient(ChatService, transport)
  }

  async machines() {
    const res = await this.relay.listHosts({})
    return res.hosts.map(h => ({ hostId: h.hostId, deviceId: h.deviceId, name: h.deviceName, online: h.online, remoteStarts: h.remoteStarts, daemon: h.daemon, lastSeenAt: h.lastSeenAt }))
  }

  async folders(hostId: string) {
    const res = await this.relay.listHostFolders({ hostId, limit: 30 })
    return res.folders.filter(f => f.exists).map(f => ({ path: f.path, name: f.name, branch: f.gitBranch }))
  }

  async start(input: RemoteStartInput) {
    const text = input.message.trim()
    const res = await this.relay.spawnSession({
      hostId: input.hostId,
      spawn: {
        create: { cwd: input.folder, permissionMode: MODES[input.mode] ?? PermissionMode.DEFAULT },
        initialMessage: text ? { uuid: crypto.randomUUID(), content: [{ block: { case: 'text', value: { text } } }] } : undefined
      },
      options: { idempotencyKey: crypto.randomUUID(), frontend: 'mobile' }
    }, frontend)
    return res.session?.id ?? ''
  }

  async chatApp(provider: ChatKind = 'slack') {
    return appView(provider, (await this.chat.getChatApp({ provider: PROVIDERS[provider] })).app)
  }

  async putChatApp(input: ChatAppInput) {
    const provider = input.provider ?? 'slack'
    const res = await this.chat.putChatApp({
      provider: PROVIDERS[provider],
      botToken: input.botToken ?? '',
      signingSecret: input.signingSecret ?? '',
      enabled: input.enabled,
      matchByEmail: input.matchByEmail,
      delivery: DELIVERIES[input.delivery ?? 'auto']
    })
    return appView(provider, res.app)
  }

  async deleteChatApp(provider: ChatKind = 'slack') {
    await this.chat.deleteChatApp({ provider: PROVIDERS[provider] })
  }

  async identities() {
    const res = await this.chat.listMyChatIdentities({})
    return {
      identities: res.identities.map(i => ({ provider: kindOf(i.provider), teamId: i.teamId, externalUserId: i.externalUserId, displayName: i.displayName, linkedVia: i.linkedVia, linkedAt: i.linkedAt })),
      available: res.available,
      providers: res.availableProviders.length ? res.availableProviders.map(kindOf) : (res.available ? ['slack' as const] : []),
      telegramBot: res.telegramBotUsername
    }
  }

  async startLink(provider: ChatKind = 'slack', personalBot = false) {
    const res = await this.chat.startChatLink({ provider: PROVIDERS[provider], personalBot })
    return { code: res.code, expiresAt: res.expiresAt, deepLink: res.deepLink }
  }

  async unlink(id: ChatIdentityView) {
    await this.chat.unlinkChatIdentity({ provider: PROVIDERS[id.provider], teamId: id.teamId, externalUserId: id.externalUserId })
  }

  async myBot() {
    const res = await this.chat.getMyChatBot({ provider: ChatProvider.TELEGRAM })
    return { bot: res.bot ? botView(res.bot) : undefined, allowed: res.allowed }
  }

  async putMyBot(input: ChatBotInput) {
    const res = await this.chat.putMyChatBot({ provider: ChatProvider.TELEGRAM, botToken: input.botToken ?? '', enabled: input.enabled, delivery: DELIVERIES[input.delivery] })
    if (!res.bot) throw new Error('The server returned no bot')
    return botView(res.bot)
  }

  async deleteMyBot() {
    await this.chat.deleteMyChatBot({ provider: ChatProvider.TELEGRAM })
  }
}
