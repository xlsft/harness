// RemoteApi: starting sessions on the user's other machines and chat apps (docs/design/chat.md). Machines, their recent
// folders and the start itself go through harness.relay.v1.RelayService, the chat app and linked identities through
// harness.chat.v1.ChatService; the client forwards both to the server with the user's credentials. The server and the
// target machine decide what may start where; this only shows it. Two implementations: connect.ts and mock.ts.

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

export interface ChatAppView {
  configured: boolean
  enabled: boolean
  teamId: string
  teamName: string
  botUserId: string
  requestUrl: string
  matchByEmail: boolean
  lastEventAt: string
  lastError: string
  disabledByPolicy: boolean
}

export interface ChatAppInput {
  // Empty keeps the stored value.
  botToken?: string
  signingSecret?: string
  enabled: boolean
  matchByEmail: boolean
}

export interface ChatIdentityView {
  teamId: string
  externalUserId: string
  displayName: string
  linkedVia: string
  linkedAt: string
}

export interface RemoteApi {
  machines(): Promise<MachineView[]>
  folders(hostId: string): Promise<FolderView[]>
  // Returns the new session's id; it shows up as a remote session of that machine.
  start(input: RemoteStartInput): Promise<string>

  chatApp(): Promise<ChatAppView>
  putChatApp(input: ChatAppInput): Promise<ChatAppView>
  deleteChatApp(): Promise<void>
  identities(): Promise<{ identities: ChatIdentityView[], available: boolean }>
  startLink(): Promise<{ code: string, expiresAt: string }>
  unlink(id: ChatIdentityView): Promise<void>
}

/** The modes a remote start may use: never bypassPermissions. */
export const REMOTE_MODES = ['default', 'acceptEdits', 'plan', 'auto', 'dontAsk'] as const
