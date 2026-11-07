// RemoteApi over the RelayService and ChatService calls the client forwards to the server
// (packages/client/server/coreproxy.go), on the same connect transport (and agent token).

import { createClient, type Client, type Transport } from '@connectrpc/connect'
import { PermissionMode } from '~/gen/harness/agent/v1/agent_pb'
import { ChatProvider, ChatService, type ChatApp } from '~/gen/harness/chat/v1/chat_pb'
import { RelayService } from '~/gen/harness/relay/v1/relay_pb'
import type { ChatAppInput, ChatAppView, ChatIdentityView, RemoteApi, RemoteStartInput } from './types'

const MODES: Record<string, PermissionMode> = {
  default: PermissionMode.DEFAULT,
  acceptEdits: PermissionMode.ACCEPT_EDITS,
  plan: PermissionMode.PLAN,
  auto: PermissionMode.AUTO,
  dontAsk: PermissionMode.DONT_ASK
}

const frontend = { headers: { 'X-Harness-Frontend': 'mobile' } }

function appView(a: ChatApp | undefined): ChatAppView {
  return {
    configured: a?.configured ?? false,
    enabled: a?.enabled ?? false,
    teamId: a?.teamId ?? '',
    teamName: a?.teamName ?? '',
    botUserId: a?.botUserId ?? '',
    requestUrl: a?.requestUrl ?? '',
    matchByEmail: a?.matchByEmail ?? true,
    lastEventAt: a?.lastEventAt ?? '',
    lastError: a?.lastError ?? '',
    disabledByPolicy: a?.disabledByPolicy ?? false
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

  async chatApp() {
    return appView((await this.chat.getChatApp({ provider: ChatProvider.SLACK })).app)
  }

  async putChatApp(input: ChatAppInput) {
    const res = await this.chat.putChatApp({ provider: ChatProvider.SLACK, botToken: input.botToken ?? '', signingSecret: input.signingSecret ?? '', enabled: input.enabled, matchByEmail: input.matchByEmail })
    return appView(res.app)
  }

  async deleteChatApp() {
    await this.chat.deleteChatApp({ provider: ChatProvider.SLACK })
  }

  async identities() {
    const res = await this.chat.listMyChatIdentities({})
    return { identities: res.identities.map(i => ({ teamId: i.teamId, externalUserId: i.externalUserId, displayName: i.displayName, linkedVia: i.linkedVia, linkedAt: i.linkedAt })), available: res.available }
  }

  async startLink() {
    const res = await this.chat.startChatLink({ provider: ChatProvider.SLACK })
    return { code: res.code, expiresAt: res.expiresAt }
  }

  async unlink(id: ChatIdentityView) {
    await this.chat.unlinkChatIdentity({ provider: ChatProvider.SLACK, teamId: id.teamId, externalUserId: id.externalUserId })
  }
}
