// RemoteApi: starting sessions on the user's other machines and chat apps (docs/design/chat.md). Machines, their recent
// folders and the start itself go through harness.relay.v1.RelayService, the chat apps, personal bots and linked
// identities through harness.chat.v1.ChatService; the client forwards both to the server with the user's credentials.
// The server and the target machine decide what may start where; this only shows it. Two implementations: connect.ts
// and mock.ts.

export interface MachineView {
  hostId: string
  deviceId: string
  name: string
  online: boolean
  // Takes remote starts in its recent folders (its own setting), or is an always-on daemon host.
  remoteStarts: boolean
  daemon: boolean
  lastSeenAt: string
}

export interface FolderView {
  path: string
  name: string
  branch: string
}

export interface RemoteStartInput {
  hostId: string
  folder: string
  // A permission mode name ('default', 'acceptEdits', 'plan', 'auto', 'dontAsk').
  mode: string
  message: string
}

export type ChatKind = 'slack' | 'telegram'

// How a Telegram bot gets its updates: 'auto' is a webhook when the server's public URL is HTTPS, polling otherwise.
export type DeliveryKind = 'auto' | 'webhook' | 'polling'

export interface ChatAppView {
  provider: ChatKind
  configured: boolean
  enabled: boolean
  // Slack: the workspace. Telegram: the bot's id and username.
  teamId: string
  teamName: string
  botUserId: string
  requestUrl: string
  matchByEmail: boolean
  lastEventAt: string
  lastError: string
  disabledByPolicy: boolean
  // Telegram only.
  delivery: DeliveryKind
  activeDelivery: DeliveryKind
}

export interface ChatAppInput {
  provider?: ChatKind
  // Empty keeps the stored value.
  botToken?: string
  signingSecret?: string
  enabled: boolean
  matchByEmail: boolean
  delivery?: DeliveryKind
}

export interface ChatIdentityView {
  provider: ChatKind
  teamId: string
  externalUserId: string
  displayName: string
  linkedVia: string
  linkedAt: string
}

export interface ChatIdentities {
  identities: ChatIdentityView[]
  // Some chat app can be used.
  available: boolean
  providers: ChatKind[]
  // The organization's Telegram bot, without @ ('' when none).
  telegramBot: string
}

export interface ChatLink {
  code: string
  expiresAt: string
  // Telegram: a t.me link that sends the code to the bot.
  deepLink: string
}

// A Telegram bot of the user's own; only its owner sees it. Never carries the token.
export interface ChatBotView {
  id: string
  enabled: boolean
  botUserId: string
  botUsername: string
  delivery: DeliveryKind
  activeDelivery: DeliveryKind
  requestUrl: string
  lastEventAt: string
  lastError: string
}

export interface ChatBotInput {
  // Empty keeps the stored token.
  botToken?: string
  enabled: boolean
  delivery: DeliveryKind
}

export interface RemoteApi {
  machines(): Promise<MachineView[]>
  folders(hostId: string): Promise<FolderView[]>
  // Returns the new session's id; it shows up as a remote session of that machine.
  start(input: RemoteStartInput): Promise<string>

  chatApp(provider?: ChatKind): Promise<ChatAppView>
  putChatApp(input: ChatAppInput): Promise<ChatAppView>
  deleteChatApp(provider?: ChatKind): Promise<void>
  identities(): Promise<ChatIdentities>
  startLink(provider?: ChatKind, personalBot?: boolean): Promise<ChatLink>
  unlink(id: ChatIdentityView): Promise<void>

  myBot(): Promise<{ bot?: ChatBotView, allowed: boolean }>
  putMyBot(input: ChatBotInput): Promise<ChatBotView>
  deleteMyBot(): Promise<void>
}

/** The modes a remote start may use: never bypassPermissions. */
export const REMOTE_MODES = ['default', 'acceptEdits', 'plan', 'auto', 'dontAsk'] as const
